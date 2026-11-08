package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Restoring agents after a restart (ADR 0035). The stop records what each
// running agent was doing (agents.resume_state): a clean shutdown from the
// live state, a crash through the row's last state at the next start. The
// next start resumes those agents' conversations once, then forgets the
// states, so only the latest stop counts.

// resumeStatesLocked returns the live state of every running agent, for a
// clean shutdown. A worker in a review or conflict round is left out: the
// round lives with the running agent (ADR 0022), so it waits for Send to
// worker or Resolve conflict instead.
func (e *Engine) resumeStatesLocked() map[string]string {
	out := map[string]string{}
	for _, id := range e.order {
		a := e.agents[id]
		if !a.running() || a.stopAfterTurn || a.followup != nil {
			continue
		}
		out[id] = string(a.status.State)
	}
	return out
}

func (e *Engine) saveResumeStates(states map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, st := range states {
		if err := e.q.SetResumeState(ctx, db.SetResumeStateParams{ResumeState: st, ID: id}); err != nil {
			e.log.Warn("record the state to restore", "agent_id", id, "err", err)
		}
	}
}

// restorePrompt is what a restored agent that was in the middle of a turn
// reads first. An idle one gets no prompt, so it spends nothing until
// someone writes to it.
const restorePrompt = "[agent-office] agent-office was restarted while you were working, which cut your last turn off. " +
	"Nothing else changed: same task, workspace and branch. Check where you were (for example git status, git log and " +
	"your task) and continue your task from there."

// restoreOrder puts the agents that coordinate first, so their workers'
// reports find them running.
var restoreOrder = map[string]int{KindArchitect: 0, KindLead: 1, KindAssistant: 2, KindWorker: 3}

// restore resumes the agents the previous run's stop interrupted, within
// --max-agents, and tells the user what came back and what did not.
func (e *Engine) restore(ctx context.Context) {
	rows, err := e.q.ListVisibleAgents(ctx, e.project.ID)
	if err != nil {
		e.log.Warn("restore: load agents", "err", err)
		return
	}
	var cands []db.ListVisibleAgentsRow
	for _, r := range rows {
		if r.ResumeState != "" {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.q.ClearResumeStates(cctx, e.project.ID); err != nil {
			e.log.Warn("restore: clear states", "err", err)
		}
	}()
	if !e.opts.RestoreAgents {
		e.log.Info("agents of the previous run not restored (--restore-agents=false)", "count", len(cands))
		return
	}
	e.restartImports.Wait() // their work is in the main repository before they continue
	slices.SortStableFunc(cands, func(a, b db.ListVisibleAgentsRow) int {
		ka, oka := restoreOrder[a.Kind]
		kb, okb := restoreOrder[b.Kind]
		if !oka {
			ka = len(restoreOrder)
		}
		if !okb {
			kb = len(restoreOrder)
		}
		if c := cmp.Compare(ka, kb); c != 0 {
			return c
		}
		return a.StartedAt.Compare(b.StartedAt)
	})
	var restored, left []string
	for _, r := range cands {
		name := fmt.Sprintf("%s (%s)", agentName(r.ID), r.Role)
		if why := e.whyNotRestored(r); why != "" {
			left = append(left, name+": "+why)
			e.log.Info("agent not restored", "agent_id", r.ID, "reason", why)
			continue
		}
		prompt := ""
		if st := hooks.State(r.ResumeState); st == hooks.Working || st == hooks.WaitingPermission {
			prompt = restorePrompt
		}
		if _, err := e.relaunchWith(ctx, r.ID, relaunchSpec{prompt: prompt, resume: true}); err != nil {
			why := firstLine(err.Error())
			if errors.Is(err, ErrTooManyAgents) {
				why = "no free agent slot (--max-agents); resume it by hand"
			}
			left = append(left, name+": "+why)
			e.log.Warn("agent not restored", "agent_id", r.ID, "err", err)
			continue
		}
		state := "idle"
		if prompt != "" {
			state = "continuing its interrupted turn"
		}
		restored = append(restored, name+", "+state)
	}
	e.log.Info("agents of the previous run restored", "restored", len(restored), "left", len(left))
	var b strings.Builder
	b.WriteString("agent-office restarted. ")
	if len(restored) > 0 {
		fmt.Fprintf(&b, "Resumed with their conversations: %s.", strings.Join(restored, "; "))
	}
	if len(left) > 0 {
		if len(restored) > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Not resumed: %s.", strings.Join(left, "; "))
	}
	ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.addInbox(ictx, "", "", "", "", InboxInfo, b.String()); err != nil {
		e.log.Warn("restore notice", "err", err)
	}
}

// whyNotRestored says why an agent the stop interrupted stays exited, or "".
func (e *Engine) whyNotRestored(r db.ListVisibleAgentsRow) string {
	switch {
	case !resumable(r.Kind):
		return "its run ended with the daemon; start a new one"
	case r.ResumeState == string(hooks.Starting):
		return "it had not started its conversation yet"
	case e.noRestore[r.ID] != "":
		return e.noRestore[r.ID]
	case (r.Sandbox == SandboxPodman) != (e.opts.Sandbox != nil):
		return fmt.Sprintf("it ran in %s mode and this daemon runs in the other", r.Sandbox)
	case r.WorktreeStatus == WorktreeRemoved:
		return "its workspace was removed"
	}
	if _, err := os.Stat(r.WorktreePath); err != nil {
		return "its workspace is gone"
	}
	return ""
}
