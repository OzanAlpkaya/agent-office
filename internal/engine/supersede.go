package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// The sub-task limit and superseded sub-tasks (ADR 0062). A goal has at most
// the project's max_sub_tasks open sub-tasks (to do, in progress, blocked);
// reported, done and superseded ones do not count, and MaxTotalSubTasks bounds
// all of them. A sub-task another one replaces is closed as done with
// superseded_by set, instead of staying open without an agent.

// supersededBySpec is superseded_by for a sub-task a revised plan dropped.
func supersededBySpec(version int64) string { return fmt.Sprintf("spec:%d", version) }

// subTaskRoom refuses a new sub-task of goalID when the goal has its limit of
// open sub-tasks (freed of them close with it) or MaxTotalSubTasks in all.
func (e *Engine) subTaskRoom(ctx context.Context, goalID string, freed int) error {
	total, err := e.q.CountChildTasks(ctx, nullString(goalID))
	if err != nil {
		return err
	}
	if total >= MaxTotalSubTasks {
		return fmt.Errorf("%w: the goal has %d sub-tasks in all; at most %d. Tell the user with message_user",
			ErrLimit, total, MaxTotalSubTasks)
	}
	open, err := e.q.CountOpenChildTasks(ctx, nullString(goalID))
	if err != nil {
		return err
	}
	if limit := e.maxSubTasks(); int(open)-freed >= limit {
		return fmt.Errorf("%w: the goal has %d open sub-tasks (to do, in progress or blocked); at most %d. Wait for "+
			"reports, or close a sub-task this one replaces with supersedes", ErrLimit, open, limit)
	}
	return nil
}

// supersedable checks the sub-tasks a new one is to replace: sub-tasks of
// goal, not done, with no running agent. It returns their ids, deduplicated.
func (e *Engine) supersedable(ctx context.Context, goal db.Task, ids []string) ([]string, error) {
	var out []string
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		if len(out) >= MaxProjectSubTasks {
			return nil, invalid("supersedes: at most %d sub-tasks", MaxProjectSubTasks)
		}
		t, err := e.getTask(ctx, id)
		if err != nil || t.ParentTaskID.String != goal.ID {
			return nil, invalid("supersedes: %q is not a sub-task of your goal", id)
		}
		if t.Status == StatusDone {
			return nil, invalid("supersedes: sub-task %s is already done", id)
		}
		if e.taskHasRunningAgent(id) {
			return nil, invalid("supersedes: sub-task %s has a running agent; stop it with stop_agent first", id)
		}
		out = append(out, id)
	}
	return out, nil
}

// dependsWithout drops from deps the sub-tasks the new one supersedes, which
// would make it wait on itself, and takes over their own dependencies
// (review finding).
func (e *Engine) dependsWithout(ctx context.Context, deps, replaced []string) ([]string, error) {
	if len(replaced) == 0 {
		return deps, nil
	}
	out := []string{}
	add := func(id string) {
		if !slices.Contains(out, id) && !slices.Contains(replaced, id) {
			out = append(out, id)
		}
	}
	for _, d := range deps {
		if !slices.Contains(replaced, d) {
			add(d)
			continue
		}
		t, err := e.getTask(ctx, d)
		if err != nil {
			return nil, err
		}
		for _, x := range jsonList(t.DependsOn) {
			add(x)
		}
	}
	return out, nil
}

// dependsOnTask reports whether t depends on id, directly or through a
// superseded sub-task that id replaced.
func (e *Engine) dependsOnTask(ctx context.Context, t db.Task, id string) bool {
	for _, d := range jsonList(t.DependsOn) {
		if d == id {
			return true
		}
		if dt, err := e.getTask(ctx, d); err == nil && isSuperseded(dt) {
			if r, ok := e.replacementOf(ctx, dt); ok && r.ID == id {
				return true
			}
		}
	}
	return false
}

// tasksWithRunningAgents is every task an agent runs on or is being assigned to.
func (e *Engine) tasksWithRunningAgents() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]bool{}
	for id := range e.assigning {
		out[id] = true
	}
	for _, a := range e.agents {
		if a.info.TaskID != "" && a.running() {
			out[a.info.TaskID] = true
		}
	}
	return out
}

func (e *Engine) taskHasRunningAgent(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.assigning[taskID] {
		return true
	}
	for _, a := range e.agents {
		if a.info.TaskID == taskID && a.running() {
			return true
		}
	}
	return false
}

// supersede closes sub-task id as replaced by by (a task id or "spec:<n>").
func (e *Engine) supersede(ctx context.Context, id, by string) error {
	row, err := e.q.SupersedeTask(ctx, db.SupersedeTaskParams{SupersededBy: by, UpdatedAt: now(), ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil // done meanwhile
	}
	if err != nil {
		return err
	}
	v := taskFromRow(row)
	e.publish(Update{Type: "task", Task: &v})
	e.log.Info("sub-task superseded", "task_id", id, "by", by)
	return nil
}

// isSuperseded reports whether a task was closed as replaced.
func isSuperseded(t db.Task) bool { return t.SupersededBy != "" }

// replacementOf follows a superseded sub-task to the task that replaced it,
// through at most a few hops; ok is false when nothing replaces it (a plan
// revision dropped it, or the replacement is gone).
func (e *Engine) replacementOf(ctx context.Context, t db.Task) (db.Task, bool) {
	for range 8 {
		if !isSuperseded(t) {
			return t, true
		}
		if strings.HasPrefix(t.SupersededBy, "spec:") {
			return t, false
		}
		next, err := e.getTask(ctx, t.SupersededBy)
		if err != nil {
			return t, false
		}
		t = next
	}
	return t, false
}
