package engine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Dependencies and file ownership (ADR 0032), enforced when a sub-task is
// assigned, not left to the prompt: a sub-task starts only after the
// sub-tasks it depends on are reported, from their work; sub-tasks whose
// files overlap do not run at the same time unless one depends on the other.

// subTaskInput validates a lead's create_task against its goal.
func (e *Engine) subTaskInput(ctx context.Context, goal db.Task, in mcp.CreateTaskInput) (TaskInput, error) {
	ti := TaskInput{Title: in.Title, Description: in.Description}
	var err error
	if ti.Files, err = normalizeList(in.Files, maxPlanFiles, 300, "files", validFilePattern); err != nil {
		return TaskInput{}, err
	}
	if ti.Acceptance, err = normalizeList(in.Acceptance, maxPlanAcceptance, maxCriterionLen, "acceptance", nil); err != nil {
		return TaskInput{}, err
	}
	if ti.Role, err = e.workerRole(strings.TrimSpace(in.Role)); err != nil {
		return TaskInput{}, err
	}
	deps, err := normalizeList(in.DependsOn, MaxProjectSubTasks, 32, "depends_on", nil)
	if err != nil {
		return TaskInput{}, err
	}
	for _, d := range deps {
		t, err := e.getTask(ctx, d)
		if err != nil || t.ParentTaskID.String != goal.ID {
			return TaskInput{}, invalid("depends_on: %q is not a sub-task of your goal", d)
		}
	}
	ti.DependsOn = deps
	return ti, nil
}

// nextSteps tells the lead what a report made possible (Phase 12 e2e: haiku
// read a report, listed the tasks and ended its turn without assigning the
// sub-task the report unblocked): the sub-tasks now ready, or that every
// sub-task is reported.
func (e *Engine) nextSteps(ctx context.Context, reported db.Task) string {
	if !reported.ParentTaskID.Valid {
		return ""
	}
	siblings, err := e.q.ListChildTasks(ctx, reported.ParentTaskID)
	if err != nil {
		return ""
	}
	var ready []string
	pending := false
	for _, s := range siblings {
		if s.Status != StatusReview && s.Status != StatusDone {
			pending = true
		}
		if s.Status != StatusTodo || !slices.Contains(jsonList(s.DependsOn), reported.ID) {
			continue
		}
		if unmet, err := e.unmetDependencies(ctx, s); err == nil && len(unmet) == 0 {
			ready = append(ready, fmt.Sprintf("%s %q", s.ID, s.Title))
		}
	}
	switch {
	case len(ready) > 0:
		return "\n\nNow ready to assign (everything they depend on is reported): " + strings.Join(ready, ", ") +
			". Assign them with assign_task."
	case !pending && e.opts.QA:
		return "\n\nEvery sub-task of the goal is reported: call start_qa."
	case !pending:
		return "\n\nEvery sub-task of the goal is reported: report the goal with report_done."
	}
	return ""
}

// unmetDependencies lists the dependencies of task that are not reported yet.
func (e *Engine) unmetDependencies(ctx context.Context, task db.Task) ([]string, error) {
	var out []string
	for _, d := range jsonList(task.DependsOn) {
		dep, err := e.getTask(ctx, d)
		if errors.Is(err, ErrNotFound) {
			continue // deleted (e.g. dropped by a revised plan): nothing to wait for
		}
		if err != nil {
			return nil, err
		}
		if isSuperseded(dep) { // its replacement's work counts (ADR 0062)
			r, ok := e.replacementOf(ctx, dep)
			if !ok {
				continue
			}
			dep = r
		}
		if dep.Status != StatusReview && dep.Status != StatusDone {
			out = append(out, fmt.Sprintf("%s %q (%s)", dep.ID, dep.Title, dep.Status))
		}
	}
	return out, nil
}

// branchOfTask returns the branch and head of the task's latest worker, as
// it is in the main repository (the imported branch for sandboxed agents).
func (e *Engine) branchOfTask(ctx context.Context, taskID string) (branch, head string, err error) {
	e.mu.Lock()
	for _, id := range e.order {
		a := e.agents[id]
		if a.info.TaskID != taskID || a.info.Kind != KindWorker {
			continue
		}
		branch = a.info.Branch
		if a.info.Sandbox == SandboxPodman {
			if imp := a.info.LastImport; imp != nil && imp.Branch != "" {
				branch = imp.Branch
			}
		}
	}
	e.mu.Unlock()
	if branch == "" {
		return "", "", invalid("sub-task %s has no worker branch", taskID)
	}
	head, err = e.git.RevParse(ctx, e.project.RepoPath, "refs/heads/"+branch)
	if err != nil {
		return "", "", invalid("sub-task %s's branch %s is not in the repository yet", taskID, branch)
	}
	return branch, head, nil
}

// subTaskBase is the commit a sub-task's worker starts from (ADR 0032): the
// work of its dependencies (one head, or a merge of several made without a
// working tree), else the goal's approved spec, else "" (HEAD).
func (e *Engine) subTaskBase(ctx context.Context, task db.Task) (string, error) {
	follow, cut, err := e.isFollowUp(ctx, task)
	if err != nil {
		return "", err
	}
	deps := jsonList(task.DependsOn)
	if len(deps) == 0 {
		if follow { // ADR 0063: the goal's latest finished work, or the target once merged
			base, err := e.goalWork(ctx, task.ParentTaskID.String)
			if err == nil {
				e.log.Info("follow-up sub-task starts from the goal's finished work", "task_id", task.ID, "base", short(base))
			}
			return base, err
		}
		return e.goalBase(ctx, task.ParentTaskID.String), nil
	}
	if unmet, err := e.unmetDependencies(ctx, task); err != nil {
		return "", err
	} else if len(unmet) > 0 {
		return "", invalid("sub-task %s waits for the sub-tasks it depends on: %s", task.ID, strings.Join(unmet, ", "))
	}
	base := ""
	if follow {
		if base, err = e.goalWork(ctx, task.ParentTaskID.String); err != nil {
			return "", err
		}
	}
	var merged []string
	for _, d := range deps {
		dt, err := e.getTask(ctx, d)
		if errors.Is(err, ErrNotFound) {
			continue // deleted: no work to start from
		}
		if err != nil {
			return "", err
		}
		if isSuperseded(dt) {
			r, ok := e.replacementOf(ctx, dt)
			if !ok {
				continue // dropped by a plan revision: no work to start from
			}
			d = r.ID
		}
		branch, head, err := e.branchOfTask(ctx, d)
		if err != nil {
			return "", err
		}
		if base == "" {
			base = head
			merged = append(merged, branch)
			continue
		}
		next, ok, err := e.foldOnto(ctx, cut, base, head, fmt.Sprintf("%s for %q", branch, task.Title))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", invalid("the work of sub-task %s's dependencies does not merge cleanly (%s conflicts); "+
				"assign a sub-task that resolves it first", task.ID, branch)
		}
		base = next
		merged = append(merged, branch)
	}
	e.log.Info("sub-task starts from its dependencies", "task_id", task.ID, "branches", merged, "base", short(base),
		"follow_up", follow)
	return base, nil
}

func (e *Engine) isAncestor(ctx context.Context, a, b string) bool {
	ok, err := e.git.IsAncestor(ctx, e.project.RepoPath, a, b)
	return err == nil && ok
}

// checkFileOverlap refuses a sub-task whose declared files overlap those of a
// running sub-task of the same goal, unless one depends on the other.
func (e *Engine) checkFileOverlap(ctx context.Context, task db.Task) error {
	mine := jsonList(task.Files)
	if len(mine) == 0 {
		return nil
	}
	siblings, err := e.q.ListChildTasks(ctx, task.ParentTaskID)
	if err != nil {
		return err
	}
	deps := map[string][]string{}
	for _, s := range siblings {
		deps[s.ID] = jsonList(s.DependsOn)
	}
	e.mu.Lock()
	running := map[string]bool{}
	for _, a := range e.agents {
		if a.info.Kind == KindWorker && a.running() {
			running[a.info.TaskID] = true
		}
	}
	for id := range e.assigning { // being started right now
		running[id] = true
	}
	e.mu.Unlock()
	for _, s := range siblings {
		if s.ID == task.ID || !running[s.ID] || reaches(deps, task.ID, s.ID) || reaches(deps, s.ID, task.ID) {
			continue
		}
		if files := overlapping(mine, jsonList(s.Files)); len(files) > 0 {
			return invalid("sub-task %s touches files of sub-task %s %q, which is running: %s. Make one depend on the "+
				"other, or assign it after %s is reported", task.ID, s.ID, s.Title, strings.Join(files, ", "), s.ID)
		}
	}
	return nil
}

// reaches reports whether from depends on to, directly or not.
func reaches(deps map[string][]string, from, to string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(k string) bool {
		if seen[k] {
			return false
		}
		seen[k] = true
		for _, d := range deps[k] {
			if d == to || walk(d) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// overlapping returns the entries of a that overlap an entry of b.
func overlapping(a, b []string) []string {
	var out []string
	for _, p := range a {
		for _, q := range b {
			if patternsOverlap(p, q) && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// patternsOverlap reports whether two file entries (paths, directories
// ending in '/', globs) can name the same file. It errs on the side of
// overlap: two globs overlap when their literal prefixes do.
func patternsOverlap(p, q string) bool {
	pd, qd := strings.HasSuffix(p, "/"), strings.HasSuffix(q, "/")
	p, q = strings.TrimSuffix(p, "/"), strings.TrimSuffix(q, "/")
	if p == q {
		return true
	}
	under := func(x, dir string) bool { return strings.HasPrefix(x, dir+"/") }
	if (pd && under(q, p)) || (qd && under(p, q)) {
		return true
	}
	pg, qg := hasMeta(p), hasMeta(q)
	if strings.Contains(p, "**") || strings.Contains(q, "**") {
		// path.Match has no "**": compare what comes before the first wildcard.
		a, b := literalPrefix(p), literalPrefix(q)
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	switch {
	case pg && qg:
		a, b := literalPrefix(p), literalPrefix(q)
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	case pg:
		ok, _ := path.Match(p, q)
		return ok || (qd && strings.HasPrefix(literalPrefix(p), q+"/"))
	case qg:
		ok, _ := path.Match(q, p)
		return ok || (pd && strings.HasPrefix(literalPrefix(q), p+"/"))
	}
	return false
}

func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

func literalPrefix(s string) string {
	if i := strings.IndexAny(s, "*?["); i >= 0 {
		return s[:i]
	}
	return s
}
