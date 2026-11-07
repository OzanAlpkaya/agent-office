package engine

import (
	"context"
	"fmt"
	"os"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// relaunch starts an exited worker again in its own workspace, on its own
// branch and base, with a new session and prompt (ADR 0022). Its task moves
// back to in_progress. The workspace is never touched on failure: it holds the
// agent's work.
func (e *Engine) relaunch(ctx context.Context, id, prompt string) (AgentInfo, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	var err error
	switch {
	case e.closing:
		err = ErrShuttingDown
	case !ok:
		err = fmt.Errorf("agent %s: %w", id, ErrNotFound)
	case a.running() || a.status.State != hooks.Exited:
		err = fmt.Errorf("agent %s: %w", id, ErrStillRunning)
	case a.busy:
		err = invalid("agent %s is being removed or relaunched", id)
	case a.info.Role != RoleWorker:
		err = invalid("only workers are relaunched, not a %s", a.info.Role)
	case a.info.WorktreeStatus == WorktreeRemoved:
		err = invalid("agent %s's workspace was removed", id)
	case (a.info.Sandbox == SandboxPodman) != (e.opts.Sandbox != nil):
		err = invalid("agent %s ran in %s mode; this daemon runs in the other mode", id, a.info.Sandbox)
	case e.runningLocked()+e.starting >= e.opts.MaxAgents:
		err = ErrTooManyAgents
	}
	if err != nil {
		e.mu.Unlock()
		return AgentInfo{}, err
	}
	a.busy = true
	e.starting++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.starting--
		a.busy = false
		e.mu.Unlock()
	}()
	if a.term != nil {
		<-a.exited
	}
	// The exit import (sandbox) has finished before the branch moves on.
	a.importMu.Lock()
	defer a.importMu.Unlock()
	info := a.info
	if _, err := os.Stat(info.WorktreePath); err != nil {
		return AgentInfo{}, invalid("agent %s's workspace is gone: %v", id, err)
	}
	var prep sandboxPrep
	if info.Sandbox == SandboxPodman {
		if prep, err = e.prepareSandboxStart(ctx, agentName(id)); err != nil {
			return AgentInfo{}, err
		}
	}

	sid, started := newUUID(), now()
	var task *Task
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.ReopenAgent(ctx, id); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sid, AgentID: id, StartedAt: started}); err != nil {
			return err
		}
		if info.TaskID == "" {
			return nil
		}
		t, err := q.GetTask(ctx, info.TaskID)
		if err != nil {
			return err
		}
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

	l := launchInfo{id: id, sid: sid, role: info.Role, branch: info.Branch, path: info.WorktreePath, base: a.baseCommit,
		taskID: info.TaskID, started: started, prompt: prompt, prev: a}
	var out AgentInfo
	if info.Sandbox == SandboxPodman {
		out, err = e.launchSandboxed(l, prep)
	} else {
		out, err = e.launch(l)
	}
	if err != nil {
		bg := context.Background()
		_ = e.q.MarkAgentExited(bg, db.MarkAgentExitedParams{ExitedAt: nullTime(now()), ExitReason: ExitStartFailed, ID: id})
		_ = e.q.EndSessions(bg, db.EndSessionsParams{EndedAt: nullTime(now()), AgentID: id})
		return AgentInfo{}, fmt.Errorf("relaunch agent %s: %w", id, err)
	}
	if task != nil {
		e.publish(Update{Type: "task", Task: task})
	}
	e.log.Info("agent relaunched", "agent_id", id, "session_id", sid, "task_id", info.TaskID, "branch", info.Branch)
	return out, nil
}
