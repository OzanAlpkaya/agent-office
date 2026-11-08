package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Conflicts between parallel work (ADR 0032). Checks run in the main
// repository with git merge-tree, which writes objects only: no checkout of
// the user's or an agent's is touched, and git never runs inside an agent
// repository on the host (ADR 0014).

// maxConflictRounds bounds automatic conflict-resolution relaunches per pull
// request; "Resolve conflict" by hand does not count.
const maxConflictRounds = 2

// baseRefInAgentRepo is where a sandboxed worker finds the new base to merge.
const baseRefInAgentRepo = "refs/remotes/agent-office/base"

// baseHead is the branch pull requests go into and its head in the main
// repository: the fetched remote base with GitHub, else the local base
// branch, else the main repository's HEAD.
func (e *Engine) baseHead(ctx context.Context) (name, sha string, err error) {
	repo := e.project.RepoPath
	if g := e.opts.GitHub; g != nil {
		if sha, err := e.git.RevParse(ctx, repo, "refs/remotes/"+g.Remote+"/"+g.Base); err == nil {
			return g.Remote + "/" + g.Base, sha, nil
		}
		if sha, err := e.git.RevParse(ctx, repo, "refs/heads/"+g.Base); err == nil {
			return g.Base, sha, nil
		}
	}
	sha, err = e.git.HeadCommit(ctx, repo)
	return "HEAD", sha, err
}

// mergeConflicts returns the files that would conflict merging b into a, or
// nil when one contains the other or they merge cleanly.
func (e *Engine) mergeConflicts(ctx context.Context, a, b string) ([]string, error) {
	if e.isAncestor(ctx, a, b) || e.isAncestor(ctx, b, a) {
		return nil, nil
	}
	res, err := e.git.MergeTree(ctx, e.project.RepoPath, a, b)
	if err != nil {
		return nil, err
	}
	return res.Conflicts, nil
}

// checkConflicts checks a reported task's head against the base branch and
// the goal's other open sub-tasks, stores the result on every task involved
// and returns this task's conflicts. Failures are logged, never fatal: the
// work is committed either way.
func (e *Engine) checkConflicts(ctx context.Context, task db.Task, branch, head string) []Conflict {
	log := e.log.With("task_id", task.ID, "branch", branch)
	out := []Conflict{}
	if name, base, err := e.baseHead(ctx); err == nil {
		if files, err := e.mergeConflicts(ctx, base, head); err != nil {
			log.Warn("conflict check against the base branch", "err", err)
		} else if len(files) > 0 {
			out = append(out, Conflict{Branch: name, Files: files})
		}
	}
	if task.ParentTaskID.Valid {
		siblings, err := e.q.ListChildTasks(ctx, task.ParentTaskID)
		if err != nil {
			log.Warn("conflict check: sub-tasks", "err", err)
		}
		deps := map[string][]string{}
		for _, s := range siblings {
			deps[s.ID] = jsonList(s.DependsOn)
		}
		for _, s := range siblings {
			if s.ID == task.ID || (s.Status != StatusInProgress && s.Status != StatusReview) ||
				reaches(deps, task.ID, s.ID) || reaches(deps, s.ID, task.ID) {
				continue
			}
			otherBranch, otherHead, err := e.branchOfTask(ctx, s.ID)
			if err != nil {
				continue // no work in the repository yet
			}
			files, err := e.mergeConflicts(ctx, otherHead, head)
			if err != nil {
				log.Warn("conflict check against a sub-task", "other", s.ID, "err", err)
				continue
			}
			var mine *Conflict
			if len(files) > 0 {
				out = append(out, Conflict{With: s.ID, Branch: otherBranch, Files: files})
				mine = &Conflict{With: task.ID, Branch: branch, Files: files}
			}
			e.setConflictWith(ctx, s, task.ID, mine)
		}
	}
	if _, err := e.saveConflicts(ctx, task.ID, out); err != nil {
		log.Warn("store conflicts", "err", err)
	}
	if len(out) > 0 {
		log.Info("conflicts found", "conflicts", len(out))
	}
	return out
}

// setConflictWith replaces task t's conflict entry for other (nil removes it).
func (e *Engine) setConflictWith(ctx context.Context, t db.Task, other string, c *Conflict) {
	cur := taskFromRow(t).Conflicts
	next := slices.DeleteFunc(slices.Clone(cur), func(x Conflict) bool { return x.With == other })
	if c != nil {
		next = append(next, *c)
	}
	if len(next) == 0 && len(cur) == 0 {
		return
	}
	if _, err := e.saveConflicts(ctx, t.ID, next); err != nil {
		e.log.Warn("store conflicts", "task_id", t.ID, "err", err)
	}
}

// setBaseConflict replaces a task's conflict entry with the base branch.
func (e *Engine) setBaseConflict(ctx context.Context, taskID string, c *Conflict) {
	t, err := e.getTask(ctx, taskID)
	if err != nil {
		return
	}
	e.setConflictWith(ctx, t, "", c)
}

func (e *Engine) saveConflicts(ctx context.Context, taskID string, cs []Conflict) (Task, error) {
	if cs == nil {
		cs = []Conflict{}
	}
	b, _ := json.Marshal(cs)
	row, err := e.q.SetTaskConflicts(ctx, db.SetTaskConflictsParams{Conflicts: string(b),
		ConflictsCheckedAt: nullTime(now()), ID: taskID})
	if err != nil {
		return Task{}, err
	}
	v := taskFromRow(row)
	e.publish(Update{Type: "task", Task: &v})
	return v, nil
}

// describeConflicts is a conflict list for messages and tool results.
func describeConflicts(cs []Conflict) string {
	var parts []string
	for _, c := range cs {
		what := "the base branch " + c.Branch
		if c.With != "" {
			what = "sub-task " + c.With + " (branch " + c.Branch + ")"
		}
		parts = append(parts, fmt.Sprintf("with %s in %s", what, strings.Join(c.Files, ", ")))
	}
	return strings.Join(parts, "; ")
}

// afterMerge runs when a pull request was merged on GitHub: the base moved,
// so every other open pull request is checked against it again, and the
// worker of each conflicting one is relaunched to resolve it, within
// maxConflictRounds (ADR 0032).
func (e *Engine) afterMerge(ctx context.Context, merged db.PullRequest) {
	g := e.opts.GitHub
	log := e.log.With("merged_pr", merged.Number)
	if _, err := e.git.FetchBranch(ctx, e.project.RepoPath, g.Remote, g.Base); err != nil {
		log.Warn("fetch the base branch after a merge", "err", err)
		return
	}
	name, base, err := e.baseHead(ctx)
	if err != nil {
		return
	}
	prs, err := e.q.ListPullRequests(ctx, e.project.ID)
	if err != nil {
		return
	}
	for _, pr := range prs {
		if pr.ID == merged.ID || pr.State != PROpen || pr.Head == "" || !pr.TaskID.Valid {
			continue
		}
		files, err := e.mergeConflicts(ctx, base, pr.Head)
		if err != nil {
			log.Warn("conflict check after a merge", "pr", pr.Number, "err", err)
			continue
		}
		if len(files) == 0 {
			e.setBaseConflict(ctx, pr.TaskID.String, nil)
			continue
		}
		e.setBaseConflict(ctx, pr.TaskID.String, &Conflict{Branch: name, Files: files})
		if _, err := e.q.TakeConflictRound(ctx, db.TakeConflictRoundParams{UpdatedAt: now(), ID: pr.ID,
			MaxRounds: maxConflictRounds}); err != nil {
			log.Info("conflict after a merge: automatic rounds used up; Resolve conflict by hand", "pr", pr.Number)
			continue
		}
		taskID := pr.TaskID.String
		e.background(e.opts.GitHub.SlotWait+time.Minute, func(ctx context.Context) {
			if err := e.resolveConflict(ctx, taskID, true); err != nil {
				log.Warn("relaunch to resolve a conflict", "task_id", taskID, "err", err)
			}
		})
	}
}

// ResolveConflict relaunches the worker of a task whose branch conflicts
// with the base branch, to merge it and resolve the conflict ("Resolve
// conflict"; ADR 0032).
func (e *Engine) ResolveConflict(ctx context.Context, taskID string) error {
	return e.resolveConflict(ctx, taskID, false)
}

func (e *Engine) resolveConflict(ctx context.Context, taskID string, auto bool) error {
	task, err := e.getTask(ctx, taskID)
	if err != nil {
		return err
	}
	branch, head, err := e.branchOfTask(ctx, taskID)
	if err != nil {
		return err
	}
	baseName, base, err := e.baseHead(ctx)
	if err != nil {
		return err
	}
	files, err := e.mergeConflicts(ctx, base, head)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		e.setBaseConflict(ctx, taskID, nil)
		return invalid("branch %s merges cleanly into %s; nothing to resolve", branch, baseName)
	}
	e.setBaseConflict(ctx, taskID, &Conflict{Branch: baseName, Files: files})
	e.mu.Lock()
	var a *agentProc
	for _, id := range e.order {
		if x := e.agents[id]; x.info.TaskID == taskID && x.info.Kind == KindWorker {
			a = x
		}
	}
	var info AgentInfo
	if a != nil {
		info = a.info
	}
	e.mu.Unlock()
	if a == nil {
		return invalid("task %s has no worker to relaunch", taskID)
	}
	if err := e.endIdleWorker(ctx, a, auto); err != nil {
		return err
	}
	ref := base
	if info.Sandbox == SandboxPodman {
		// The new base, as a ref the agent can merge; its objects are in the
		// main repository it borrows from. Set by a helper container: the host
		// does not run git in the agent's repository (ADR 0014).
		if err := e.helper(ctx, info, sandbox.GitIdentity{}, []string{"git", "update-ref", baseRefInAgentRepo, base},
			io.Discard, 4096); err != nil {
			return fmt.Errorf("give the worker the new base: %w", err)
		}
		ref = baseRefInAgentRepo
	}
	var prID int64
	var number int
	if pr, err := e.q.ListPullRequests(ctx, e.project.ID); err == nil {
		for _, p := range pr {
			if p.TaskID.String == taskID && p.State == PROpen {
				prID, number = p.ID, int(p.Number)
			}
		}
	}
	prompt := e.conflictPrompt(e.roleOf(info.Role, info.Kind), task, info.Branch, baseName, ref, base, files)
	if _, err := e.relaunch(ctx, info.ID, prompt, &followupRound{prID: prID, number: number, head: head,
		conflict: true}); err != nil {
		return err
	}
	e.log.Info("worker relaunched to resolve a conflict", "agent_id", info.ID, "task_id", taskID, "base", short(base),
		"files", len(files), "auto", auto)
	return nil
}

// endIdleWorker ends a running worker that is idle and not used by the user,
// so it can be relaunched; it refuses otherwise.
func (e *Engine) endIdleWorker(ctx context.Context, a *agentProc, auto bool) error {
	quiet := time.Minute
	if g := e.opts.GitHub; g != nil {
		quiet = g.FollowUpQuiet
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		e.mu.Lock()
		running := a.running()
		idle := running && a.status.State == hooks.Idle && !a.stopAfterTurn && time.Since(a.lastInput) >= quiet
		if idle {
			a.exitReason = ExitFollowUp
		}
		e.mu.Unlock()
		switch {
		case !running:
			return nil
		case idle:
			return e.stop(a.info.ID, ExitFollowUp)
		case !auto || time.Now().After(deadline):
			return fmt.Errorf("%w: the worker is still working or you used its terminal recently; try again when it is idle",
				ErrStillRunning)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// conflictPrompt is the prompt of a worker relaunched to resolve a conflict.
func (e *Engine) conflictPrompt(def *role.Role, task db.Task, branch, baseName, ref, base string, files []string) string {
	var b strings.Builder
	b.WriteString(e.expandPrompt(def) + "\n\n")
	b.WriteString("Task: " + task.Title + "\n")
	if task.Description != "" {
		b.WriteString("\n" + task.Description + "\n")
	}
	fmt.Fprintf(&b, "\nYou worked on this task before, on git branch %s; this is a new session, so look at your earlier "+
		"commits first (git log). Since then %s moved on (another change was merged), and your branch now conflicts "+
		"with it in: %s.\n\nThis time, and only for this: merge %s (commit %s) into your branch with git merge, resolve "+
		"the conflicts so both changes keep working, run the tests, and commit the merge on %s. Do not rebase, reset or "+
		"push, and do not touch other branches. Then call report_done; it is refused until your branch has a new commit.",
		branch, baseName, strings.Join(files, ", "), ref, short(base), branch)
	b.WriteString(e.workerRules(&task))
	return b.String()
}
