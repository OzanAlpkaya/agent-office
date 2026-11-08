package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Task statuses (ADR 0011).
const (
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusReview     = "review"
	StatusDone       = "done"
	StatusBlocked    = "blocked"
)

var taskStatuses = []string{StatusTodo, StatusInProgress, StatusReview, StatusDone, StatusBlocked}

// Limits mirrored by CHECK constraints in the schema.
const (
	MaxTitleLen       = 200
	MaxDescriptionLen = 20000
)

// Task is the public view of a task.
type Task struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	ParentTaskID string    `json:"parent_task_id,omitempty"` // the goal of a sub-task (ADR 0017)
	CreatedBy    string    `json:"created_by,omitempty"`     // empty: the user; else the creating agent's id
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// A sub-task's plan (ADR 0032).
	DependsOn          []string   `json:"depends_on"` // task ids that must be reported first
	Files              []string   `json:"files"`      // paths, directories ending in '/', globs it touches
	Acceptance         []string   `json:"acceptance"` // criteria QA tests (ADR 0033)
	Role               string     `json:"role,omitempty"`
	SpecKey            string     `json:"spec_key,omitempty"`
	Conflicts          []Conflict `json:"conflicts"` // the last merge-tree check of its branch
	ConflictsCheckedAt *time.Time `json:"conflicts_checked_at,omitempty"`
}

// Conflict is a set of files that would not merge cleanly (ADR 0032): the
// task's branch against the base branch (With empty) or another sub-task's.
type Conflict struct {
	With   string   `json:"with,omitempty"` // the other task's id; empty: the base branch
	Branch string   `json:"branch"`         // the other branch
	Files  []string `json:"files"`
}

// TaskInput creates a task.
type TaskInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`

	// Sub-tasks only (ADR 0032); set by the lead or an approved spec.
	DependsOn  []string `json:"-"`
	Files      []string `json:"-"`
	Acceptance []string `json:"-"`
	Role       string   `json:"-"`
	SpecKey    string   `json:"-"`
}

// TaskPatch changes a task; nil fields stay as they are.
type TaskPatch struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func taskFromRow(t db.Task) Task {
	v := Task{ID: t.ID, Title: t.Title, Description: t.Description, Status: t.Status,
		ParentTaskID: t.ParentTaskID.String, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		DependsOn: jsonList(t.DependsOn), Files: jsonList(t.Files), Acceptance: jsonList(t.Acceptance), Role: t.Role,
		SpecKey: t.SpecKey, Conflicts: []Conflict{}}
	if t.Conflicts != "" {
		_ = json.Unmarshal([]byte(t.Conflicts), &v.Conflicts)
	}
	if v.Conflicts == nil {
		v.Conflicts = []Conflict{}
	}
	if t.ConflictsCheckedAt.Valid {
		c := t.ConflictsCheckedAt.Time.UTC()
		v.ConflictsCheckedAt = &c
	}
	return v
}

// jsonList decodes a JSON array of strings stored in a column; never nil.
func jsonList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// jsonText encodes a list for a JSON column.
func jsonText(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// normalizeTitle trims the title and replaces control characters (including
// newlines) with spaces.
func normalizeTitle(s string) (string, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
	if n := utf8.RuneCountInString(s); n == 0 || n > MaxTitleLen {
		return "", fmt.Errorf("%w: title must be 1-%d characters", ErrInvalid, MaxTitleLen)
	}
	return s, nil
}

func normalizeDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%w: description is not valid UTF-8", ErrInvalid)
	}
	if utf8.RuneCountInString(s) > MaxDescriptionLen {
		return "", fmt.Errorf("%w: description longer than %d characters", ErrInvalid, MaxDescriptionLen)
	}
	return s, nil
}

// CreateTask adds a top-level task in status todo, created by the user.
func (e *Engine) CreateTask(ctx context.Context, in TaskInput) (Task, error) {
	return e.createTask(ctx, in, "", "")
}

// createTask adds a task in status todo; parent and createdBy are empty for
// the user's own tasks.
func (e *Engine) createTask(ctx context.Context, in TaskInput, parent, createdBy string) (Task, error) {
	title, err := normalizeTitle(in.Title)
	if err != nil {
		return Task{}, err
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return Task{}, err
	}
	t := now()
	row, err := e.q.CreateTask(ctx, db.CreateTaskParams{ID: randomHex(6), ProjectID: e.project.ID, Title: title,
		Description: desc, Status: StatusTodo, CreatedAt: t, UpdatedAt: t, ParentTaskID: nullString(parent),
		CreatedBy: createdBy, DependsOn: jsonText(in.DependsOn), Files: jsonText(in.Files),
		Acceptance: jsonText(in.Acceptance), Role: in.Role, SpecKey: in.SpecKey})
	if err != nil {
		return Task{}, err
	}
	v := taskFromRow(row)
	e.publish(Update{Type: "task", Task: &v})
	return v, nil
}

// ListTasks returns the project's tasks in creation order.
func (e *Engine) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := e.q.ListTasks(ctx, e.project.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromRow(r))
	}
	return out, nil
}

func (e *Engine) getTask(ctx context.Context, id string) (db.Task, error) {
	t, err := e.q.GetTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && t.ProjectID != e.project.ID) {
		return db.Task{}, ErrNotFound
	}
	return t, err
}

// UpdateTask applies a patch.
func (e *Engine) UpdateTask(ctx context.Context, id string, p TaskPatch) (Task, error) {
	cur, err := e.getTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	params := db.UpdateTaskParams{Title: cur.Title, Description: cur.Description, Status: cur.Status, UpdatedAt: now(), ID: id}
	if p.Title != nil {
		if params.Title, err = normalizeTitle(*p.Title); err != nil {
			return Task{}, err
		}
	}
	if p.Description != nil {
		if params.Description, err = normalizeDescription(*p.Description); err != nil {
			return Task{}, err
		}
	}
	if p.Status != nil {
		if !slices.Contains(taskStatuses, *p.Status) {
			return Task{}, fmt.Errorf("%w: status must be one of %s", ErrInvalid, strings.Join(taskStatuses, ", "))
		}
		params.Status = *p.Status
	}
	row, err := e.q.UpdateTask(ctx, params)
	if err != nil {
		return Task{}, err
	}
	v := taskFromRow(row)
	e.publish(Update{Type: "task", Task: &v})
	if row.Status == StatusDone && cur.Status != StatusDone && !row.ParentTaskID.Valid {
		e.endGoal(row.ID)
	}
	return v, nil
}

// endGoal ends the agents that coordinate a goal the user marked done: its
// Tech Lead, architect and QA (exit reason goal_done). Workers are left alone:
// they end when they report, and one still working may hold work.
func (e *Engine) endGoal(goalID string) {
	e.mu.Lock()
	var ids []string
	for _, a := range e.agents {
		if a.info.TaskID == goalID && a.running() &&
			(a.info.Kind == KindLead || a.info.Kind == KindArchitect || a.info.Kind == KindQA) {
			ids = append(ids, a.info.ID)
		}
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.log.Info("goal done: ending its agent", "agent_id", id, "task_id", goalID)
		e.wg.Add(1)
		go func() { defer e.wg.Done(); _ = e.stop(id, ExitGoalDone) }()
	}
}

// DeleteTask deletes a task. Agents that worked on it keep running history but
// lose the link. A task with a running agent cannot be deleted.
func (e *Engine) DeleteTask(ctx context.Context, id string) error {
	if _, err := e.getTask(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	for _, a := range e.agents {
		if a.info.TaskID == id && a.status.State != hooks.Exited {
			e.mu.Unlock()
			return ErrTaskHasRunning
		}
	}
	e.mu.Unlock()
	if _, err := e.q.RemoveTask(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	for _, a := range e.agents {
		if a.info.TaskID == id {
			a.info.TaskID = ""
			e.publishAgentLocked(a)
		}
	}
	e.publishLocked(Update{Type: "task_deleted", ID: id})
	e.mu.Unlock()
	return nil
}

// taskPrompt is the first prompt an agent gets when it starts from a task.
// The git rules are instructions, not enforcement: until the sandbox (Phase 3)
// withholds push credentials, a host agent could still ignore them. Pushing and
// pull requests are the daemon's job (Phase 5).
func taskPrompt(title, description, branch string) string {
	var b strings.Builder
	b.WriteString("Task: " + title + "\n")
	if description != "" {
		b.WriteString("\n" + description + "\n")
	}
	b.WriteString("\nYou are working in a dedicated git worktree on branch " + branch +
		". When the task is done, commit your changes on this branch." +
		" Do not push. Do not touch any other branch: do not create, check out, merge, rebase," +
		" reset or delete other branches. agent-office pushes and opens pull requests itself.")
	return b.String()
}
