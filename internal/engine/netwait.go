package engine

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// Waiting for the network (Phase 20, ADR 0067). When the connection drops
// while Claude Code streams an answer (a VPN toggled, Wi-Fi gone), its turn
// ends with "API Error: Connection lost mid-response" (a StopFailure hook)
// and the agent sits at its prompt. The engine shows it as waiting for the
// network, checks every few seconds whether the API answers again, and then
// types one agent-office line into its terminal so the turn goes on, as
// idle message delivery does (ADR 0018). Any hook first ends the wait.

// StateWaitingNetwork is the shown state of an idle agent whose turn ended
// with a lost connection.
const StateWaitingNetwork = "waiting_network"

// netResumeText is what the engine types into the agent once the network is back.
const netResumeText = "agent-office: the connection to the API was lost and is back now. Continue the task from where you stopped."

var (
	netProbeEvery  = 5 * time.Second // tests shorten it
	netMaxResumes  = 5               // automatic resumes per agent per hour
	netResumeAfter = 2 * time.Second // the network back, before typing: let it settle
)

// netProbe reports whether the Anthropic API answers: any HTTP response
// means the network is back. It goes through the API proxy's own transport,
// so HTTPS_PROXY (a VPN's proxy, the e2e's cut proxy) applies. No
// credential is sent.
func netProbe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sandbox.AnthropicAPI.String()+"/", nil)
	if err != nil {
		return err
	}
	tr := sandbox.NewAPITransport()
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (e *Engine) probeNetwork(ctx context.Context) error {
	if e.opts.netProbe != nil {
		return e.opts.netProbe(ctx)
	}
	return netProbe(ctx)
}

// afterNetworkLocked ends a network wait on any hook that moves the agent
// on, and starts one when a Claude Code turn ended with a lost connection.
func (e *Engine) afterNetworkLocked(ctx context.Context, a *agentProc, ev agent.Event) {
	if a.netWaiting && !ev.Subagent && ev.Kind != agent.Idle && ev.Kind != agent.Other {
		a.netWaiting = false
		e.publishAgentLocked(a)
	}
	if ev.Kind != agent.TurnFinished || !ev.NetworkLost || ev.Subagent || a.info.CLI == role.CLICodex ||
		a.status.State != hooks.Idle || a.stopAfterTurn || !a.running() {
		return
	}
	log := a.logger(e)
	cutoff := now().Add(-time.Hour)
	recent := a.netResumes[:0]
	for _, t := range a.netResumes {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	a.netResumes = recent
	if len(recent) >= netMaxResumes {
		log.Warn("connection lost again; not resuming by itself", "resumes_last_hour", len(recent))
		id, task, roleName := a.info.ID, a.info.TaskID, a.info.Role
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := e.addInbox(c, id, task, "", roleName, InboxInfo, fmt.Sprintf("Agent %s lost the API connection %d "+
				"times in an hour; agent-office stopped resuming it. Check the network, then type into its terminal.",
				e.labelOf(id), netMaxResumes)); err != nil {
				e.log.Warn("network notice", "err", err)
			}
		}()
		return
	}
	a.netWaiting = true
	e.recordStateLocked(ctx, a, StateWaitingNetwork, "network_lost")
	e.publishAgentLocked(a)
	log.Info("connection lost mid-turn; waiting for the network")
	if !e.netProbing && e.bg != nil {
		e.netProbing = true
		e.wg.Add(1)
		go e.netLoop(e.bg)
	}
}

// netLoop probes while any agent waits for the network, and resumes the
// waiting agents once the API answers. It ends with the engine.
func (e *Engine) netLoop(ctx context.Context) {
	defer e.wg.Done()
	t := time.NewTicker(netProbeEvery)
	defer t.Stop()
	stop := func() {
		e.mu.Lock()
		e.netProbing = false
		e.mu.Unlock()
	}
	for {
		select {
		case <-ctx.Done():
			stop()
			return
		case <-t.C:
		}
		e.mu.Lock()
		var waiting []string
		for _, id := range e.order {
			if a, ok := e.agents[id]; ok && a.netWaiting && a.running() {
				waiting = append(waiting, id)
			}
		}
		if len(waiting) == 0 || e.closing {
			e.netProbing = false
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := e.probeNetwork(pctx)
		cancel()
		if err != nil {
			continue
		}
		e.log.Info("the network is back; resuming agents", "agents", len(waiting))
		select {
		case <-ctx.Done():
			stop()
			return
		case <-time.After(netResumeAfter):
		}
		for _, id := range waiting {
			e.resumeAfterNetwork(id)
		}
	}
}

// resumeAfterNetwork types the resume line into a still-waiting agent.
func (e *Engine) resumeAfterNetwork(id string) {
	e.mu.Lock()
	a, ok := e.agents[id]
	if !ok || !a.netWaiting || !a.running() || a.status.State != hooks.Idle || a.delivering {
		e.mu.Unlock()
		return
	}
	if quiet := time.Since(a.lastInput); quiet < e.opts.typingQuiet {
		e.mu.Unlock()
		return // the user is typing here: the next probe tries again
	}
	a.netWaiting = false
	a.delivering = true
	term, log := a.term, a.logger(e)
	e.publishAgentLocked(a)
	e.mu.Unlock()

	_, err := term.Write([]byte(ptyLine(netResumeText)))
	if err == nil {
		time.Sleep(e.opts.enterDelay)
		_, err = term.Write([]byte("\r"))
	}
	e.mu.Lock()
	a.delivering = false
	if err == nil {
		a.netResumes = append(a.netResumes, now())
	} else if a.running() && a.status.State == hooks.Idle {
		a.netWaiting = true // nothing reached it and nothing moved it on: try again on the next probe
		e.publishAgentLocked(a)
	}
	e.mu.Unlock()
	if err != nil {
		log.Warn("resume after the network came back failed", "err", err)
		return
	}
	log.Info("resumed after the network came back")
}
