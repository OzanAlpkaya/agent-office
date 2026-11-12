package engine

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// The background wait state (ADR 0064). A Claude Code turn can end while a
// shell it started with run_in_background still runs (a worker waiting on
// check.sh); Claude Code wakes the model when it finishes. Until then the agent
// is shown as "waiting on background task", not idle. The check runs only for
// a session that started such a command, compares the agent's processes with
// a snapshot taken when it started, and any failure leaves the agent idle.

// StateWaitingBackground is the shown state of an idle agent whose background
// command still runs. The engine's own state stays idle: messages are still
// delivered to it.
const StateWaitingBackground = "waiting_background"

// bgPollEvery is how often a waiting agent's processes are checked; tests
// shorten it.
var bgPollEvery = 5 * time.Second

// agentProcs returns an agent's processes: below its CLI on the host, the
// container's cgroup in a sandbox; ok is false when they cannot be read.
func (e *Engine) agentProcs(a *agentProc) (map[int]procInfo, bool) {
	read := e.readProcs
	if read == nil {
		read = readProcs
	}
	if a.sandbox != nil {
		dir, _ := a.sandbox.cgroupDir.Load().(string)
		if dir == "" {
			return nil, false
		}
		pids, err := cgroupPids(dir)
		if err != nil {
			return nil, false
		}
		tab, err := read()
		if err != nil {
			return nil, false
		}
		out := map[int]procInfo{}
		for _, pid := range pids {
			if p, ok := tab[pid]; ok {
				out[pid] = p
			}
		}
		return out, true
	}
	if a.term == nil {
		return nil, false
	}
	tab, err := read()
	if err != nil {
		return nil, false
	}
	return descendants(tab, a.term.Pid()), true
}

// cgroupPids lists the pids in a cgroup and the cgroups below it.
func cgroupPids(dir string) ([]int, error) {
	var out []int
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "cgroup.procs" {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil // the cgroup went away meanwhile
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			if pid, err := strconv.Atoi(strings.TrimSpace(s.Text())); err == nil {
				out = append(out, pid)
			}
		}
		return nil
	})
	return out, err
}

// ownBin is agent-office's binary name as it appears in an agent's tree.
func (e *Engine) ownBin() string { return filepath.Base(e.opts.AgentOfficeBin) }

// noteBackgroundLocked records a background command's start: the agent's
// processes before it runs (the PreToolUse hook blocks the tool), so what it
// starts can be told apart from the CLI's own helpers.
func (e *Engine) noteBackgroundLocked(a *agentProc, ev agent.Event) {
	if !ev.Background || ev.Subagent || ev.Kind != agent.ToolStarted {
		return
	}
	a.bgSeen = true
	if a.bgBaseline != nil {
		return
	}
	procs, ok := e.agentProcs(a)
	if !ok {
		return
	}
	a.bgBaseline = map[int]bool{}
	for pid := range procs {
		a.bgBaseline[pid] = true
	}
}

// backgroundRunning reports whether a started background command still runs.
func (e *Engine) backgroundRunning(a *agentProc) bool {
	if a.bgBaseline == nil {
		return false
	}
	procs, ok := e.agentProcs(a)
	if !ok {
		return false
	}
	bin := e.ownBin()
	for pid, p := range procs {
		if !a.bgBaseline[pid] && !ownHelper(p, bin) {
			return true
		}
	}
	return false
}

// afterTurnLocked starts the background check when a turn ended idle in a
// session that started a background command, and ends the wait when the
// agent moves on.
func (e *Engine) afterTurnLocked(ctx context.Context, a *agentProc, ev agent.Event) {
	if a.bgWaiting && (a.status.State != hooks.Idle || !a.running()) {
		a.bgWaiting = false
		e.publishAgentLocked(a)
	}
	if ev.Kind != agent.TurnFinished || ev.Subagent || !a.bgSeen || a.bgWaiting || a.bgChecking ||
		a.status.State != hooks.Idle {
		return
	}
	if !e.backgroundRunning(a) {
		return
	}
	a.bgWaiting = true
	e.recordStateLocked(ctx, a, StateWaitingBackground, "background_wait")
	e.publishAgentLocked(a)
	a.bgChecking = true
	e.wg.Add(1)
	go e.pollBackground(a)
}

// pollBackground ends the wait once the background command is gone.
func (e *Engine) pollBackground(a *agentProc) {
	defer e.wg.Done()
	t := time.NewTicker(bgPollEvery)
	defer t.Stop()
	for {
		select {
		case <-a.exited:
			e.mu.Lock()
			a.bgChecking = false
			e.mu.Unlock()
			return
		case <-t.C:
		}
		e.mu.Lock()
		if !a.bgWaiting || e.agents[a.info.ID] != a {
			a.bgChecking = false
			e.mu.Unlock()
			return
		}
		if !e.backgroundRunning(a) {
			a.bgWaiting, a.bgChecking = false, false
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			e.recordStateLocked(ctx, a, string(hooks.Idle), "background_done")
			cancel()
			e.publishAgentLocked(a)
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
	}
}

// recordStateLocked stores a reduced event for a shown state the hooks did
// not cause, so the run info's waiting time includes it.
func (e *Engine) recordStateLocked(ctx context.Context, a *agentProc, state, detail string) {
	if err := e.q.InsertEvent(ctx, db.InsertEventParams{AgentID: a.info.ID, SessionID: a.info.SessionID,
		Kind: string(agent.Other), Detail: detail, State: state, At: now()}); err != nil {
		e.log.Warn("persist event", "agent_id", a.info.ID, "err", err)
	}
}
