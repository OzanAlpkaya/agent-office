package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Bases for follow-up work (ADR 0063). New sub-tasks that are not from the
// approved plan (polish rounds, QA bugs, the lead's own fixes) start from the
// goal's latest finished work, not the plan commit. Once the goal was
// squash-merged into the target branch, follow-up work starts from the target,
// and work built on the pre-squash history is ported onto it: three-way merged
// with the already-merged part as the base, so only its new changes move and
// the squash does not show up as conflicts.

// mergedCut is what of a goal is in the target branch: its recorded merges
// whose commits the target contains.
type mergedCut struct {
	target     string   // the target branch
	targetHead string   // its head now
	commit     string   // the latest merge commit in it
	heads      []string // what those merges covered: QA heads and the sub-task heads QA merged
}

// cutOf returns the goal's merged cut, or nil when no merge of it is in the
// target branch.
func (e *Engine) cutOf(ctx context.Context, goalID string) (*mergedCut, error) {
	merges, err := e.q.ListGoalMerges(ctx, goalID)
	if err != nil || len(merges) == 0 {
		return nil, err
	}
	repo, target := e.project.RepoPath, e.mergeTarget()
	head, err := e.git.RevParse(ctx, repo, "refs/heads/"+target)
	if err != nil {
		return nil, nil // no target branch: nothing is merged into it
	}
	c := &mergedCut{target: target, targetHead: head}
	for _, m := range merges { // newest first
		if m.Target != target || !e.isAncestor(ctx, m.CommitSha, head) {
			continue
		}
		if c.commit == "" {
			c.commit = m.CommitSha
		}
		c.heads = append(c.heads, m.SourceHead)
		if m.QaRunID.Valid {
			if run, err := e.q.GetQARun(ctx, m.QaRunID.Int64); err == nil {
				for _, b := range qaRunFromRow(run).Merged {
					c.heads = append(c.heads, b.Head)
				}
			}
		}
	}
	if c.commit == "" {
		return nil, nil
	}
	return c, nil
}

// merged reports whether head is covered by a merge of the cut.
func (e *Engine) merged(ctx context.Context, c *mergedCut, head string) bool {
	if c == nil {
		return false
	}
	for _, m := range c.heads {
		if e.isAncestor(ctx, head, m) {
			return true
		}
	}
	return false
}

// needsPort reports whether head is built on the goal's pre-squash history:
// not merged, and not containing the latest merge commit.
func (e *Engine) needsPort(ctx context.Context, c *mergedCut, head string) bool {
	return c != nil && !e.merged(ctx, c, head) && !e.isAncestor(ctx, c.commit, head)
}

// portBase is the newest common ancestor of head and the cut's merged heads:
// the part of head that is already in the target.
func (e *Engine) portBase(ctx context.Context, c *mergedCut, head string) (string, error) {
	best := ""
	for _, m := range c.heads {
		mb, err := e.git.MergeBase(ctx, e.project.RepoPath, head, m)
		if err != nil {
			return "", err
		}
		if mb != "" && (best == "" || e.isAncestor(ctx, best, mb)) {
			best = mb
		}
	}
	if best == "" {
		return "", invalid("no common history with the merged work")
	}
	return best, nil
}

// portTree is the tree of head's changes since the merged cut applied onto
// onto, with its conflicts.
func (e *Engine) portTree(ctx context.Context, c *mergedCut, onto, head string) (git.MergeResult, error) {
	base, err := e.portBase(ctx, c, head)
	if err != nil {
		return git.MergeResult{}, err
	}
	return e.git.MergeTreeBase(ctx, e.project.RepoPath, base, onto, head)
}

// port applies head's changes since the merged cut onto onto and commits the
// result with onto as its only parent. A conflict is an error naming the files.
func (e *Engine) port(ctx context.Context, c *mergedCut, onto, head, what string) (string, error) {
	repo := e.project.RepoPath
	res, err := e.portTree(ctx, c, onto, head)
	if err != nil {
		return "", err
	}
	if !res.Clean {
		return "", invalid("the new changes of %s conflict with %s in %s", what, c.target, strings.Join(res.Conflicts, ", "))
	}
	return e.git.CommitTree(ctx, repo, res.Tree, []string{onto},
		fmt.Sprintf("agent-office: %s on %s\n\nIts changes since the goal's squash merge, ported onto %s.\n\n"+
			"Agent-Office-Ported-From: %s\n", what, c.target, short(onto), head))
}

// foldOnto adds head to base without a working tree: base itself when it
// contains head, head when it contains base, a port when head is built on the
// squashed history, else a merge commit. ok is false when it conflicts.
func (e *Engine) foldOnto(ctx context.Context, c *mergedCut, base, head, what string) (next string, ok bool, err error) {
	repo := e.project.RepoPath
	switch {
	case e.isAncestor(ctx, head, base), e.merged(ctx, c, head):
		return base, true, nil
	case e.needsPort(ctx, c, head):
		p, err := e.port(ctx, c, base, head, what)
		if errors.Is(err, ErrInvalid) {
			return base, false, nil
		}
		return p, err == nil, err
	case e.isAncestor(ctx, base, head):
		return head, true, nil
	}
	res, err := e.git.MergeTree(ctx, repo, base, head)
	if err != nil {
		return base, false, err
	}
	if !res.Clean {
		return base, false, nil
	}
	m, err := e.git.CommitTree(ctx, repo, res.Tree, []string{base, head},
		fmt.Sprintf("agent-office: base with %s\n", what))
	return m, err == nil, err
}

// latestFinishedQA is the goal's newest QA run that ended with a head.
func (e *Engine) latestFinishedQA(ctx context.Context, goalID string) (db.QaRun, bool) {
	runs, err := e.q.ListGoalQARuns(ctx, goalID)
	if err != nil {
		return db.QaRun{}, false
	}
	for _, r := range runs {
		if (r.Status == QAPassed || r.Status == QAFailed) && r.Head != "" {
			return r, true
		}
	}
	return db.QaRun{}, false
}

// goalWork is the goal's latest finished work (ADR 0063): the target branch
// once the goal was merged into it, else the latest finished QA run's head,
// else the approved spec (else HEAD); with every reported sub-task not in it
// yet folded on top. A sub-task whose work does not fold cleanly is left out
// and logged.
func (e *Engine) goalWork(ctx context.Context, goalID string) (string, error) {
	repo := e.project.RepoPath
	c, err := e.cutOf(ctx, goalID)
	if err != nil {
		return "", err
	}
	base := ""
	switch run, ok := e.latestFinishedQA(ctx, goalID); {
	case c != nil:
		base = c.targetHead
	case ok:
		base = run.Head
	default:
		base = e.goalBase(ctx, goalID)
	}
	if base == "" {
		if base, err = e.git.HeadCommit(ctx, repo); err != nil {
			return "", err
		}
	} else if base, err = e.git.RevParse(ctx, repo, base); err != nil {
		return "", err
	}
	children, err := e.q.ListChildTasks(ctx, nullString(goalID))
	if err != nil {
		return "", err
	}
	var left []string
	for _, b := range e.reportedBranches(ctx, children) {
		next, ok, err := e.foldOnto(ctx, c, base, b.Head, b.Branch)
		if err != nil {
			return "", err
		}
		if !ok {
			left = append(left, b.TaskID)
			continue
		}
		base = next
	}
	if len(left) > 0 {
		e.log.Warn("follow-up base leaves out work that does not merge cleanly", "task_id", goalID, "left_out", left)
	}
	return base, nil
}

// isFollowUp reports whether a sub-task starts from the goal's finished work
// rather than the plan commit: it is not from the approved plan, or the goal
// was merged already (ADR 0063).
func (e *Engine) isFollowUp(ctx context.Context, task db.Task) (bool, *mergedCut, error) {
	c, err := e.cutOf(ctx, task.ParentTaskID.String)
	if err != nil {
		return false, nil, err
	}
	return task.SpecKey == "" || c != nil, c, nil
}

// qaBase is the commit a QA run starts from: the target branch once the goal
// was merged into it (its pre-squash branches are in there as the squash),
// else the approved spec ("" for HEAD).
func (e *Engine) qaBase(ctx context.Context, goalID string) (string, *mergedCut, error) {
	c, err := e.cutOf(ctx, goalID)
	if err != nil {
		return "", nil, err
	}
	if c != nil {
		return c.targetHead, c, nil
	}
	return e.goalBase(ctx, goalID), nil, nil
}

// qaBranches is what a QA run merges: the reported sub-tasks not merged yet,
// each as its own head or, built on the pre-squash history, as a port onto
// the target, on a branch of its own.
func (e *Engine) qaBranches(ctx context.Context, c *mergedCut, children []db.Task) ([]QABranch, error) {
	var out []QABranch
	for _, b := range e.reportedBranches(ctx, children) {
		if e.merged(ctx, c, b.Head) {
			continue
		}
		if e.needsPort(ctx, c, b.Head) {
			p, err := e.port(ctx, c, c.targetHead, b.Head, b.Branch)
			if err != nil {
				return nil, fmt.Errorf("sub-task %s: %w; assign a sub-task that brings it up to date with %s", b.TaskID, err, c.target)
			}
			name := fmt.Sprintf("%sport-%s-%s", git.BranchPrefix, b.TaskID[:min(6, len(b.TaskID))], p[:8])
			if err := e.git.SetBranch(ctx, e.project.RepoPath, name, p, ""); err != nil &&
				!e.branchAt(ctx, name, p) {
				return nil, err
			}
			b.Branch, b.Merge, b.Port = name, p, true
		}
		out = append(out, b)
	}
	return out, nil
}

// branchAt reports whether branch already points at sha (a port made before).
func (e *Engine) branchAt(ctx context.Context, branch, sha string) bool {
	h, err := e.git.RevParse(ctx, e.project.RepoPath, "refs/heads/"+branch)
	return err == nil && h == sha
}

// mergeOf is the commit QA merges for b: its port, or its head.
func (b QABranch) mergeOf() string {
	if b.Merge != "" {
		return b.Merge
	}
	return b.Head
}
