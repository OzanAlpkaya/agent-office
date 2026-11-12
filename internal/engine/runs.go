package engine

import (
	"context"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Run info for the task modal (Phase 19): what each agent of a task did,
// computed on request from the agent, its sessions, its reduced state events,
// its branch and its reports. Token usage and cost come with the usage report
// the UI already has (ADR 0026).

// AgentRun is one agent's run on a task.
type AgentRun struct {
	AgentID    string          `json:"agent_id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Role       string          `json:"role"`
	Model      string          `json:"model,omitempty"`
	CLI        string          `json:"cli"`
	State      string          `json:"state"`
	StartedAt  time.Time       `json:"started_at"`
	ExitedAt   *time.Time      `json:"exited_at,omitempty"`
	ExitReason string          `json:"exit_reason,omitempty"`
	Working    int64           `json:"working_seconds"` // time in working, from its state events
	Waiting    int64           `json:"waiting_seconds"` // idle, waiting for permission or a background task, starting
	Branch     string          `json:"branch"`
	Base       string          `json:"base,omitempty"`
	Last       *git.CommitInfo `json:"last_commit,omitempty"` // its branch's head, once it has commits
	Changes    *git.DiffStat   `json:"changes,omitempty"`     // its branch against its base
	Sessions   []SessionRun    `json:"sessions"`
	Reports    []TaskReport    `json:"reports"` // its report_done calls on this task
}

// SessionRun is one session of an agent: a start, a resume or a relaunch.
type SessionRun struct {
	ID          string     `json:"id"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ResumedFrom string     `json:"resumed_from,omitempty"`
	ExitReason  string     `json:"exit_reason,omitempty"` // recorded since Phase 19
	Turns       int64      `json:"turns"`
	ToolCalls   int64      `json:"tool_calls"`
}

// TaskReport is a worker's report_done: agent-written text, shown as text.
type TaskReport struct {
	Summary      string    `json:"summary"`
	Verification string    `json:"verification,omitempty"`
	At           time.Time `json:"at"`
}

// TaskRuns lists the runs of every agent that worked on task id, oldest first.
func (e *Engine) TaskRuns(ctx context.Context, id string) ([]AgentRun, error) {
	if _, err := e.getTask(ctx, id); err != nil {
		return nil, err
	}
	type snap struct {
		info AgentInfo
		base string
	}
	var agents []snap
	e.mu.Lock()
	for _, aid := range e.order {
		if a := e.agents[aid]; a.info.TaskID == id {
			agents = append(agents, snap{info: a.view(), base: a.baseCommit})
		}
	}
	e.mu.Unlock()
	reports, err := e.q.ListTaskReports(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]AgentRun, 0, len(agents))
	for _, s := range agents {
		r := AgentRun{AgentID: s.info.ID, Name: s.info.Name, Kind: s.info.Kind, Role: s.info.Role, Model: s.info.Model,
			CLI: s.info.CLI, State: s.info.State, StartedAt: s.info.StartedAt, ExitedAt: s.info.ExitedAt,
			ExitReason: s.info.ExitReason, Branch: s.info.Branch, Base: s.base, Sessions: []SessionRun{}, Reports: []TaskReport{}}
		if imp := s.info.LastImport; s.info.Sandbox == SandboxPodman && imp != nil && imp.Branch != "" {
			r.Branch = imp.Branch // the imported branch in the main repository (ADR 0014)
		}
		e.runGit(ctx, &r)
		if evs, err := e.q.ListAgentStateEvents(ctx, s.info.ID); err == nil {
			r.Working, r.Waiting = stateDurations(evs, now())
		}
		if ss, err := e.q.ListAgentSessions(ctx, s.info.ID); err == nil {
			for i := len(ss) - 1; i >= 0; i-- { // newest first in the query
				r.Sessions = append(r.Sessions, sessionRunFrom(ss[i]))
			}
		}
		for _, rep := range reports {
			if rep.AgentID.String == s.info.ID {
				r.Reports = append(r.Reports, TaskReport{Summary: rep.Summary, Verification: rep.Verification, At: rep.CreatedAt.UTC()})
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// runGit fills in the branch's last commit and its changes against the base,
// when the branch is in the main repository and has commits.
func (e *Engine) runGit(ctx context.Context, r *AgentRun) {
	repo := e.project.RepoPath
	if r.Branch == "" {
		return
	}
	head, err := e.git.RevParse(ctx, repo, "refs/heads/"+r.Branch)
	if err != nil || head == r.Base {
		return
	}
	if c, err := e.git.LastCommit(ctx, repo, head); err == nil {
		r.Last = &c
	}
	if r.Base != "" {
		if st, err := e.git.DiffStat(ctx, repo, r.Base, head); err == nil {
			r.Changes = &st
		}
	}
}

func sessionRunFrom(s db.Session) SessionRun {
	v := SessionRun{ID: s.ID, StartedAt: s.StartedAt.UTC(), ResumedFrom: s.ResumedFrom, ExitReason: s.ExitReason,
		Turns: s.Turns, ToolCalls: s.ToolCalls}
	if s.EndedAt.Valid {
		t := s.EndedAt.Time.UTC()
		v.EndedAt = &t
	}
	return v
}

// stateDurations adds up the time between consecutive state events by the
// state each one entered: working, or waiting (idle, waiting for permission or
// a background task, starting). Time after an exit is not counted; the last
// state of a running agent counts until until.
func stateDurations(evs []db.ListAgentStateEventsRow, until time.Time) (working, waiting int64) {
	for i, ev := range evs {
		end := until
		if i+1 < len(evs) {
			end = evs[i+1].At
		}
		d := int64(end.Sub(ev.At) / time.Second)
		if d <= 0 {
			continue
		}
		switch hooks.State(ev.State) {
		case hooks.Working:
			working += d
		case hooks.Exited:
		default:
			waiting += d
		}
	}
	return working, waiting
}
