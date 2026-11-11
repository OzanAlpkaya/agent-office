package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// errBusy: a removal or another relaunch of the agent is under way.
var errBusy = errors.New("busy")

// relaunchSpec says how an exited agent starts again.
type relaunchSpec struct {
	prompt   string         // initial prompt; empty for none
	followup *followupRound // a relaunch to address a review or conflict (ADR 0022, 0032)
	resume   bool           // continue the latest session's conversation (ADR 0035)
}

// relaunch starts an exited worker again in its own workspace, on its own
// branch and base, with a new session and prompt (ADR 0022). Its task moves
// back to in_progress. The workspace is never touched on failure: it holds the
// agent's work.
func (e *Engine) relaunch(ctx context.Context, id, prompt string, fu *followupRound) (AgentInfo, error) {
	return e.relaunchWith(ctx, id, relaunchSpec{prompt: prompt, followup: fu})
}

// resumable is whether an agent of kind can continue its conversation
// (ADR 0035). QA runs and reviews end with their agent (ADR 0021, 0033).
func resumable(kind string) bool {
	return kind == KindWorker || kind == KindArchitect || kind == KindLead || kind == KindAssistant
}

// relaunchWith is relaunch and resume: same agent id, workspace, branch,
// base, config dir and role, a new session. A resume continues the latest
// session's conversation (claude --resume --fork-session), leaves the task's
// status alone and receives the messages queued for that conversation.
func (e *Engine) relaunchWith(ctx context.Context, id string, r relaunchSpec) (AgentInfo, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	var err error
	oneAtATime := false
	switch {
	case e.closing:
		err = ErrShuttingDown
	case !ok:
		err = fmt.Errorf("agent %s: %w", id, ErrNotFound)
	case a.running() || a.status.State != hooks.Exited:
		err = fmt.Errorf("agent %s: %w", id, ErrStillRunning)
	case a.busy:
		err = fmt.Errorf("%w: %w: agent %s is being removed or relaunched", ErrInvalid, errBusy, id)
	case !r.resume && a.info.Kind != KindWorker && a.info.Kind != KindArchitect:
		err = invalid("only workers and architects are relaunched, not a %s", a.info.Role)
	case r.resume && !resumable(a.info.Kind):
		err = invalid("a %s is not resumed: its run ended with it; start a new one", a.info.Role)
	case a.info.WorktreeStatus == WorktreeRemoved:
		err = invalid("agent %s's workspace was removed", id)
	case (a.info.Sandbox == SandboxPodman) != (e.opts.Sandbox != nil):
		err = invalid("agent %s ran in %s mode; this daemon runs in the other mode", id, a.info.Sandbox)
	case r.resume && single(a.info.Kind) && (e.startingKind[a.info.Kind] || e.runningKindLocked(a.info.Kind) != nil):
		err = invalid("a %s is already running; stop it first", a.info.Kind)
	default:
		err = e.reserveSlotLocked()
	}
	if err != nil {
		e.mu.Unlock()
		return AgentInfo{}, err
	}
	if r.resume && single(a.info.Kind) {
		oneAtATime = true
		e.startingKind[a.info.Kind] = true
	}
	a.busy = true
	e.starting++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.starting--
		a.busy = false
		if oneAtATime {
			delete(e.startingKind, a.info.Kind)
		}
		e.syncSlotsLocked()
		e.mu.Unlock()
	}()
	if a.term != nil {
		<-a.exited
	}
	if a.afterExit != nil {
		<-a.afterExit // the exit import (sandbox) has finished before the branch moves on
	}
	a.importMu.Lock()
	defer a.importMu.Unlock()
	info := a.info
	if _, err := os.Stat(info.WorktreePath); err != nil {
		return AgentInfo{}, invalid("agent %s's workspace is gone: %v", id, err)
	}
	var from db.Session
	if r.resume {
		if from, err = e.resumeSource(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return AgentInfo{}, invalid("agent %s has no session to resume", id)
		} else if err != nil {
			return AgentInfo{}, err
		}
		if (info.Kind == KindLead || info.Kind == KindArchitect) && info.TaskID != "" {
			if t, err := e.q.GetTask(ctx, info.TaskID); err == nil && t.Status == StatusDone {
				return AgentInfo{}, invalid("the goal of this %s is done; start a new one for new work", info.Role)
			}
		}
	}
	if err := e.checkCLI(info.CLI); err != nil {
		return AgentInfo{}, err
	}
	cliResume := ""
	if r.resume && info.CLI == role.CLICodex {
		// Codex continues its own session id (ADR 0049); one that never sent a
		// hook has no conversation to load.
		if cliResume = from.CliSessionID; cliResume == "" {
			return AgentInfo{}, invalid("agent %s has no Codex conversation to resume", id)
		}
	}
	var prep sandboxPrep
	if info.Sandbox == SandboxPodman {
		if prep, err = e.prepareSandboxStart(ctx, agentName(id), info.Kind); err != nil {
			return AgentInfo{}, err
		}
	}

	sid, started := newUUID(), now()
	var task *Task
	prevStatus := ""
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.ReopenAgent(ctx, id); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sid, AgentID: id, StartedAt: started,
			ResumedFrom: from.ID, CliSessionID: cliResume}); err != nil {
			return err
		}
		if info.TaskID == "" || r.resume {
			return nil // a resume continues; status changes come from reports and the user
		}
		t, err := q.GetTask(ctx, info.TaskID)
		if err != nil {
			return err
		}
		prevStatus = t.Status
		if t.Status != StatusInProgress {
			if t, err = q.UpdateTask(ctx, db.UpdateTaskParams{Title: t.Title, Description: t.Description,
				Status: StatusInProgress, UpdatedAt: started, ID: t.ID}); err != nil {
				return err
			}
		}
		v := taskFromRow(t)
		task = &v
		return nil
	})
	if err != nil {
		return AgentInfo{}, fmt.Errorf("record relaunch: %w", err)
	}

	prompt := r.prompt
	if prompt != "" && !r.resume {
		// ADR 0034, 0037; a resumed conversation already has it.
		roleName, files := e.agentScope(ctx, info.Role, info.TaskID)
		def := e.roleOf(info.Role, info.Kind)
		prompt += e.memorySection(ctx, roleName, files, slices.Contains(def.TeamTools, mcp.ToolListMemory))
	}
	l := launchInfo{id: id, sid: sid, role: info.Kind, def: e.roleOf(info.Role, info.Kind), branch: info.Branch,
		path: info.WorktreePath, base: a.baseCommit,
		taskID: info.TaskID, started: started, prompt: prompt, prev: a, followup: r.followup, cli: cliOr(info.CLI),
		cliResume: cliResume}
	if r.resume {
		// Messages queued for the conversation it continues are its own.
		l.resumeFrom, l.since = from.ID, from.StartedAt.UTC()
	} else if held := e.heldSince(a); !held.IsZero() {
		// ADR 0051: what was sent once it stopped taking messages (its report,
		// its exit) is for this session; what its own session missed stays
		// that session's (ADR 0022).
		l.since = held
	}
	// ADR 0051: what waited for it goes into its first prompt, so a session
	// that finishes in its first turn (a report) still gets it.
	var held []Message
	if l.prompt != "" {
		since := l.since
		if since.IsZero() {
			since = started
		}
		l.prompt, held = e.withHeldMessages(ctx, id, since, l.prompt)
	}
	var out AgentInfo
	if info.Sandbox == SandboxPodman {
		out, err = e.launchSandboxed(l, prep)
	} else {
		out, err = e.launch(l)
	}
	if err != nil {
		bg := context.Background()
		e.unclaim(bg, held)
		_ = e.q.MarkAgentExited(bg, db.MarkAgentExitedParams{ExitedAt: nullTime(now()), ExitReason: ExitStartFailed, ID: id})
		_ = e.q.EndSessions(bg, db.EndSessionsParams{EndedAt: nullTime(now()), AgentID: id})
		if r.resume {
			_ = e.q.DeleteSession(bg, sid) // never ran; the next resume continues from.ID again
		}
		if task != nil && prevStatus != "" && prevStatus != StatusInProgress {
			if t, gerr := e.getTask(bg, task.ID); gerr == nil {
				_, _ = e.setTaskStatus(bg, t, prevStatus) // no agent works on it after all
			}
		}
		return AgentInfo{}, fmt.Errorf("relaunch agent %s: %w", id, err)
	}
	if task != nil {
		e.publish(Update{Type: "task", Task: task})
	}
	if r.resume {
		e.log.Info("agent resumed", "agent_id", id, "session_id", sid, "resumed_from", from.ID, "task_id", info.TaskID,
			"prompt", prompt != "")
	} else {
		e.log.Info("agent relaunched", "agent_id", id, "session_id", sid, "task_id", info.TaskID, "branch", info.Branch)
	}
	return out, nil
}

// heldSince is when an exited agent stopped taking messages; for an agent of
// a previous daemon run, its exit.
func (e *Engine) heldSince(a *agentProc) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !a.heldSince.IsZero() {
		return a.heldSince
	}
	if a.info.ExitedAt != nil {
		return a.info.ExitedAt.UTC()
	}
	return time.Time{}
}

// MaxResumePrompt bounds the optional prompt of a resume.
const MaxResumePrompt = MaxMessageLen

// Resume continues an exited agent's latest conversation in its workspace and
// sandbox, with an optional prompt from the user (ADR 0035).
func (e *Engine) Resume(ctx context.Context, id, prompt string) (AgentInfo, error) {
	if prompt != "" {
		var err error
		if prompt, err = normalizeText(prompt, MaxResumePrompt, "prompt"); err != nil {
			return AgentInfo{}, err
		}
	}
	return e.relaunchWith(ctx, id, relaunchSpec{prompt: prompt, resume: true})
}

// resumeSource is the session a resume continues: the agent's latest session,
// except that a resumed session in which no turn finished is skipped for the
// session it resumed. Claude Code writes a fork's transcript only once the
// fork gets a message, so a fork that was only shown (an idle restore, a
// Resume without a prompt) has none, and `--resume` of it fails with "No
// conversation found" (found in Ozan's test: a sandboxed worker restored
// twice; checked against Claude Code 2.1.319). Its parent holds the same
// conversation. Original sessions are never skipped: a first turn cut off
// by a crash still has its prompt there.
func (e *Engine) resumeSource(ctx context.Context, id string) (db.Session, error) {
	s, err := e.q.LatestSession(ctx, id)
	for hops := 0; err == nil && s.Turns == 0 && s.ResumedFrom != "" && hops < 100; hops++ {
		parent, perr := e.q.GetSession(ctx, s.ResumedFrom)
		if perr != nil || parent.AgentID != id {
			break // the chain ends here; resume what we have
		}
		s = parent
	}
	return s, err
}

// resumeFailed reports whether a resumed CLI exited before its first hook:
// one that exits before it could not load the conversation (Claude Code
// prints "No conversation found ..." and exits 1).
func resumeFailed(a *agentProc) bool {
	return a.resumeFrom != "" && !a.sessionStarted
}
