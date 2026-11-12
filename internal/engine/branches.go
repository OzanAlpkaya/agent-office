package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Branch clean-up (Phase 18, ADR 0059). agent-office branches pile up: every
// worker, QA run and lead has one, and a goal's squash merge (ADR 0055) does
// not make them ancestors of the target branch. A branch is offered for
// deletion by whether its work is in the target:
//   - merged: its head is an ancestor of the target;
//   - squashed: its head is in a recorded goal merge whose commit is in the
//     target, or merging it into the target would change nothing;
//   - unmerged: anything else; deleted only when the user names it again.
// A branch checked out in a worktree, or whose agent is running, is in use
// and never deleted. Deleting is a compare-and-swap on the head the preview
// showed, and every deletion is recorded with its head.

// Branch classes.
const (
	BranchMerged   = "merged"
	BranchSquashed = "squashed"
	BranchUnmerged = "unmerged"
)

// BranchInfo is one agent-office branch in a clean-up preview.
type BranchInfo struct {
	Name      string `json:"name"`
	Head      string `json:"head"`
	Class     string `json:"class"`
	Ahead     int    `json:"ahead"`            // commits not in the target
	InUse     string `json:"in_use,omitempty"` // why it cannot be deleted now
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}

// BranchPreview is what a clean-up would delete, compared with the target.
type BranchPreview struct {
	Target     string       `json:"target"`
	TargetHead string       `json:"target_head"`
	Branches   []BranchInfo `json:"branches"`
}

// BranchDelete is the user's confirmation: branches at the heads the preview
// showed, and the unmerged ones named a second time.
type BranchDelete struct {
	Branches []struct {
		Name string `json:"name"`
		Head string `json:"head"`
	} `json:"branches"`
	Unmerged []string `json:"unmerged"`
}

// DeletedBranch is a branch the user deleted, kept for the archive.
type DeletedBranch struct {
	ID        int64  `json:"id"`
	GoalID    string `json:"goal_id,omitempty"`
	Name      string `json:"name"`
	Head      string `json:"head"`
	Class     string `json:"class"`
	DeletedAt string `json:"deleted_at"`
}

// BranchRefusal says why a requested branch was not deleted.
type BranchRefusal struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// BranchDeleteResult is what a clean-up did.
type BranchDeleteResult struct {
	Deleted []DeletedBranch `json:"deleted"`
	Refused []BranchRefusal `json:"refused"`
}

const maxBranchDelete = 500

func deletedBranchFromRow(r db.DeletedBranch) DeletedBranch {
	return DeletedBranch{ID: r.ID, GoalID: r.GoalID.String, Name: r.Name, Head: r.Head, Class: r.Class,
		DeletedAt: r.DeletedAt.UTC().Format("2006-01-02T15:04:05Z")}
}

// GoalBranches previews the clean-up of a done goal's branches: those of
// every agent of the goal and its sub-tasks that still exist.
func (e *Engine) GoalBranches(ctx context.Context, goalID string) (BranchPreview, error) {
	goal, err := e.getGoal(ctx, goalID)
	if err != nil {
		return BranchPreview{}, err
	}
	if goal.Status != StatusDone {
		return BranchPreview{}, invalid("mark the goal done first; its branches may still be needed")
	}
	rows, err := e.q.ListGoalAgents(ctx, db.ListGoalAgentsParams{ProjectID: e.project.ID, GoalID: nullString(goalID)})
	if err != nil {
		return BranchPreview{}, err
	}
	want := map[string]bool{}
	for _, r := range rows {
		want[r.Branch] = true
	}
	return e.branchPreview(ctx, func(name string) bool { return want[name] })
}

// ProjectBranches previews the clean-up of every agent-office branch of the
// project's repository.
func (e *Engine) ProjectBranches(ctx context.Context) (BranchPreview, error) {
	return e.branchPreview(ctx, func(string) bool { return true })
}

// branchPreview classifies the agent-office branches that keep accepts.
func (e *Engine) branchPreview(ctx context.Context, keep func(string) bool) (BranchPreview, error) {
	repo, target := e.project.RepoPath, e.mergeTarget()
	targetHead, err := e.git.RevParse(ctx, repo, "refs/heads/"+target)
	if err != nil {
		return BranchPreview{}, invalid("the merge target %s does not exist in %s; set the project's merge target", target, repo)
	}
	p := BranchPreview{Target: target, TargetHead: targetHead, Branches: []BranchInfo{}}
	all, err := e.git.AgentBranches(ctx, repo)
	if err != nil {
		return BranchPreview{}, err
	}
	// Whose branch is whose, and which are in use.
	owners, err := e.q.ListProjectAgentBranches(ctx, e.project.ID)
	if err != nil {
		return BranchPreview{}, err
	}
	owner := map[string]db.ListProjectAgentBranchesRow{}
	for _, o := range owners {
		owner[o.Branch] = o // the latest agent on a branch (rows are in start order)
	}
	checkedOut := map[string]string{}
	if wts, err := e.git.ListWorktrees(ctx, repo); err == nil {
		for _, w := range wts {
			checkedOut[strings.TrimPrefix(w.Branch, "refs/heads/")] = w.Path
		}
	} else {
		return BranchPreview{}, fmt.Errorf("list worktrees: %w", err)
	}
	running := map[string]string{}
	e.mu.Lock()
	for _, a := range e.agents {
		if a.status.State != hooks.Exited {
			running[a.info.Branch] = a.info.Name
		}
	}
	e.mu.Unlock()
	// Recorded goal merges whose commit is in the target: their sources' commits are in it.
	merges, err := e.q.ListProjectGoalMerges(ctx, db.ListProjectGoalMergesParams{ProjectID: e.project.ID, Limit: 1000})
	if err != nil {
		return BranchPreview{}, err
	}
	var landed []string
	for _, m := range merges {
		if ok, _ := e.git.IsAncestor(ctx, repo, m.CommitSha, targetHead); ok {
			landed = append(landed, m.SourceHead)
		}
	}
	targetTree, err := e.git.TreeOf(ctx, repo, targetHead)
	if err != nil {
		return BranchPreview{}, err
	}
	for _, b := range all {
		if !keep(b.Name) {
			continue
		}
		info := BranchInfo{Name: b.Name, Head: b.Head}
		if o, ok := owner[b.Name]; ok {
			info.AgentID, info.AgentName, info.TaskID = o.ID, o.Name, o.TaskID.String
		}
		switch {
		case running[b.Name] != "":
			info.InUse = fmt.Sprintf("its agent %s is running", running[b.Name])
		case checkedOut[b.Name] != "":
			info.InUse = "checked out in " + checkedOut[b.Name]
		}
		info.Class, info.Ahead = e.classify(ctx, repo, b.Head, targetHead, targetTree, landed)
		p.Branches = append(p.Branches, info)
	}
	return p, nil
}

// classify says whether a branch's work is in the target, and how many of
// its commits are not.
func (e *Engine) classify(ctx context.Context, repo, head, targetHead, targetTree string, landed []string) (string, int) {
	ahead, err := e.git.CommitsAhead(ctx, repo, targetHead, head)
	if err != nil {
		e.log.Warn("branch clean-up: count commits", "head", head, "err", err)
		return BranchUnmerged, 0
	}
	if ahead == 0 {
		return BranchMerged, 0
	}
	for _, src := range landed {
		if ok, _ := e.git.IsAncestor(ctx, repo, head, src); ok {
			return BranchSquashed, ahead
		}
	}
	// Merging it changes nothing: its changes are in the target already (a
	// squash merge made outside agent-office, or cherry-picks).
	if res, err := e.git.MergeTree(ctx, repo, targetHead, head); err == nil && res.Clean && res.Tree == targetTree {
		return BranchSquashed, ahead
	}
	return BranchUnmerged, ahead
}

// DeleteGoalBranches deletes branches of a done goal; DeleteProjectBranches
// any agent-office branch of the project. Each requested branch is checked
// again: one in use, moved since the preview, or unmerged without being
// named in Unmerged is refused, not deleted.
func (e *Engine) DeleteGoalBranches(ctx context.Context, goalID string, req BranchDelete) (BranchDeleteResult, error) {
	return e.deleteBranches(ctx, goalID, req)
}

// DeleteProjectBranches is the project-wide clean-up (see DeleteGoalBranches).
func (e *Engine) DeleteProjectBranches(ctx context.Context, req BranchDelete) (BranchDeleteResult, error) {
	return e.deleteBranches(ctx, "", req)
}

func (e *Engine) deleteBranches(ctx context.Context, goalID string, req BranchDelete) (BranchDeleteResult, error) {
	if len(req.Branches) == 0 {
		return BranchDeleteResult{}, invalid("no branch to delete")
	}
	if len(req.Branches) > maxBranchDelete {
		return BranchDeleteResult{}, invalid("at most %d branches at once", maxBranchDelete)
	}
	// Merges and clean-ups of the same repository do not interleave.
	e.mergeMu.Lock()
	defer e.mergeMu.Unlock()
	var p BranchPreview
	var err error
	if goalID != "" {
		p, err = e.GoalBranches(ctx, goalID)
	} else {
		p, err = e.ProjectBranches(ctx)
	}
	if err != nil {
		return BranchDeleteResult{}, err
	}
	byName := map[string]BranchInfo{}
	for _, b := range p.Branches {
		byName[b.Name] = b
	}
	out := BranchDeleteResult{Deleted: []DeletedBranch{}, Refused: []BranchRefusal{}}
	refuse := func(name, format string, a ...any) {
		out.Refused = append(out.Refused, BranchRefusal{Name: name, Reason: fmt.Sprintf(format, a...)})
	}
	seen := map[string]bool{}
	for _, r := range req.Branches {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		b, ok := byName[r.Name]
		switch {
		case !ok && goalID != "":
			refuse(r.Name, "not a branch of this goal (or already deleted)")
			continue
		case !ok:
			refuse(r.Name, "not an agent-office branch of this project (or already deleted)")
			continue
		case b.InUse != "":
			refuse(r.Name, "%s", b.InUse)
			continue
		case b.Head != r.Head:
			refuse(r.Name, "it moved since the preview (now at %s): check again", short(b.Head))
			continue
		case b.Class == BranchUnmerged && !slices.Contains(req.Unmerged, r.Name):
			refuse(r.Name, "it is not merged: confirm it by name to delete it")
			continue
		}
		if err := e.git.DeleteBranchAt(ctx, e.project.RepoPath, b.Name, b.Head); err != nil {
			if errors.Is(err, git.ErrBranchMoved) {
				refuse(r.Name, "it moved during the clean-up: check again")
			} else {
				refuse(r.Name, "git: %s", firstLine(err.Error()))
			}
			continue
		}
		row, err := e.q.InsertDeletedBranch(context.WithoutCancel(ctx), db.InsertDeletedBranchParams{
			ProjectID: e.project.ID, GoalID: nullString(goalID), Name: b.Name, Head: b.Head, Class: b.Class, DeletedAt: now()})
		if err != nil {
			// The branch is gone; its head must not be: say it loudly.
			e.log.Error("branch deleted but not recorded", "branch", b.Name, "head", b.Head, "class", b.Class, "err", err)
			out.Deleted = append(out.Deleted, DeletedBranch{Name: b.Name, Head: b.Head, Class: b.Class, GoalID: goalID})
			continue
		}
		e.log.Info("branch deleted", "task_id", goalID, "branch", b.Name, "head", b.Head, "class", b.Class)
		out.Deleted = append(out.Deleted, deletedBranchFromRow(row))
	}
	return out, nil
}

// goalDeletedBranches lists a goal's deleted branches for its record.
func (e *Engine) goalDeletedBranches(ctx context.Context, goalID string) ([]DeletedBranch, error) {
	rows, err := e.q.ListGoalDeletedBranches(ctx, nullString(goalID))
	if err != nil {
		return nil, err
	}
	out := make([]DeletedBranch, 0, len(rows))
	for _, r := range rows {
		out = append(out, deletedBranchFromRow(r))
	}
	return out, nil
}
