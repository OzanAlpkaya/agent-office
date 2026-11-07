package engine

import (
	"context"
	"database/sql"
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
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TaskInput creates a task.
type TaskInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// TaskPatch changes a task; nil fields stay as they are.
type TaskPatch struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func taskFromRow(t db.Task) Task {
	return Task{ID: t.ID, Title: t.Title, Description: t.Description, Status: t.Status,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
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

// CreateTask adds a task in status todo.
func (e *Engine) CreateTask(ctx context.Context, in TaskInput) (Task, error) {
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
		Description: desc, Status: StatusTodo, CreatedAt: t, UpdatedAt: t})
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
	return v, nil
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
