package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// commitOn adds a commit with a file to branch in repo through a temporary
// worktree, and returns the new head.
func commitOn(t *testing.T, repo, branch, file, content string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", wt, branch)
	if err := os.WriteFile(filepath.Join(wt, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", file)
	gitIn(t, wt, "commit", "-q", "-m", "add "+file)
	head := gitIn(t, wt, "rev-parse", "HEAD")
	gitIn(t, repo, "worktree", "remove", wt)
	return head
}

func branchByName(t *testing.T, p BranchPreview, name string) BranchInfo {
	t.Helper()
	for _, b := range p.Branches {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("branch %s not in the preview %+v", name, p.Branches)
	return BranchInfo{}
}

func deleteReq(unmerged []string, bs ...BranchInfo) BranchDelete {
	var r BranchDelete
	for _, b := range bs {
		r.Branches = append(r.Branches, struct {
			Name string `json:"name"`
			Head string `json:"head"`
		}{b.Name, b.Head})
	}
	r.Unmerged = unmerged
	return r
}

// TestGoalBranchCleanup (Phase 18, ADR 0059): a done goal's branches are
// classified against main after its squash merge; checked-out ones are in
// use; an unmerged branch is deleted only when named again; a branch that
// moved is refused; deletions are recorded in the goal's record.
func TestGoalBranchCleanup(t *testing.T) {
	tf := passedGoal(t, nil)
	e, repo, ctx := tf.e, tf.f.repo, context.Background()
	if _, err := e.GoalBranches(ctx, tf.goal.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("branches of a goal in review: %v", err)
	}
	p, err := e.MergePreview(ctx, tf.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: p.TargetHead, SourceHead: p.SourceHead, Message: p.Message}); err != nil {
		t.Fatal(err)
	}
	done := StatusDone
	if _, err := e.UpdateTask(ctx, tf.goal.ID, TaskPatch{Status: &done}); err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(tf.lead.ID, hooks.Exited))
	rec, _ := e.GoalRecord(ctx, tf.goal.ID)
	var workerBranch, qaBranch string
	for _, a := range rec.Agents {
		switch a.Kind {
		case KindWorker:
			workerBranch = a.Branch
		case KindQA:
			qaBranch = a.Branch
		}
	}

	// Exited agents keep their worktrees (host mode): their branches are checked out.
	bp, err := e.GoalBranches(ctx, tf.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b := branchByName(t, bp, workerBranch); !strings.HasPrefix(b.InUse, "checked out in ") || b.Class != BranchSquashed {
		t.Fatalf("worker branch before removal %+v", b)
	}
	res, err := e.DeleteGoalBranches(ctx, tf.goal.ID, deleteReq(nil, branchByName(t, bp, workerBranch)))
	if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "checked out") {
		t.Fatalf("delete a checked-out branch: %+v %v", res, err)
	}

	if _, err := e.RemoveGoalAgents(ctx, tf.goal.ID); err != nil {
		t.Fatal(err)
	}
	bp, err = e.GoalBranches(ctx, tf.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bp.Target != "main" || bp.TargetHead != gitIn(t, repo, "rev-parse", "main") {
		t.Fatalf("preview target %+v", bp)
	}
	w, q := branchByName(t, bp, workerBranch), branchByName(t, bp, qaBranch)
	if w.Class != BranchSquashed || w.InUse != "" || w.AgentName == "" || w.Ahead == 0 {
		t.Fatalf("worker branch: its work is in main through the goal's squash merge: %+v", w)
	}
	if q.Class != BranchSquashed || q.InUse != "" {
		t.Fatalf("QA branch %+v", q)
	}
	for _, b := range bp.Branches {
		if b.Name != workerBranch && b.Name != qaBranch {
			t.Fatalf("a branch that is not the goal's: %+v", b)
		}
	}

	// A commit on the worker's branch after QA: its work is no longer all in main.
	commitOn(t, repo, workerBranch, "later.go", "package later\n")
	bp, _ = e.GoalBranches(ctx, tf.goal.ID)
	moved := branchByName(t, bp, workerBranch)
	if moved.Class != BranchUnmerged || moved.Ahead != 2 {
		t.Fatalf("worker branch after a later commit %+v", moved)
	}
	res, err = e.DeleteGoalBranches(ctx, tf.goal.ID, deleteReq(nil, moved, w))
	if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "not merged: confirm it by name") {
		t.Fatalf("unmerged without confirmation (and the same branch twice): %+v %v", res, err)
	}
	stale := q
	stale.Head = moved.Head
	res, _ = e.DeleteGoalBranches(ctx, tf.goal.ID, deleteReq(nil, stale))
	if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "moved since the preview") {
		t.Fatalf("a stale head: %+v", res)
	}
	res, err = e.DeleteGoalBranches(ctx, tf.goal.ID, deleteReq([]string{workerBranch}, q, moved))
	if err != nil || len(res.Deleted) != 2 || len(res.Refused) != 0 {
		t.Fatalf("delete %+v %v", res, err)
	}
	for _, b := range []string{workerBranch, qaBranch} {
		if ok, _ := e.git.BranchExists(ctx, repo, b); ok {
			t.Fatalf("%s still exists", b)
		}
	}
	rec, _ = e.GoalRecord(ctx, tf.goal.ID)
	if len(rec.DeletedBranches) != 2 || rec.DeletedBranches[0].Name != qaBranch || rec.DeletedBranches[0].Class != BranchSquashed ||
		rec.DeletedBranches[1].Name != workerBranch || rec.DeletedBranches[1].Head != moved.Head || rec.DeletedBranches[1].Class != BranchUnmerged {
		t.Fatalf("deleted branches in the record %+v", rec.DeletedBranches)
	}
	res, _ = e.DeleteGoalBranches(ctx, tf.goal.ID, deleteReq(nil, q))
	if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "already deleted") {
		t.Fatalf("delete again: %+v", res)
	}
	if _, err := e.DeleteGoalBranches(ctx, tf.goal.ID, BranchDelete{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nothing to delete: %v", err)
	}
}

// TestProjectBranchCleanup: the one-time clean-up of a project classifies
// every agent-office branch: an ancestor of main is merged; one whose
// changes main already has (a squash merge outside agent-office) is
// squashed; one with work of its own is unmerged; a running agent's branch
// is in use. Other branches are never listed.
func TestProjectBranchCleanup(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	repo := f.repo
	base := gitIn(t, repo, "rev-parse", "main")
	gitIn(t, repo, "branch", "agent-office/merged", base)
	gitIn(t, repo, "branch", "agent-office/squashed", base)
	gitIn(t, repo, "branch", "agent-office/wip", base)
	gitIn(t, repo, "branch", "feature/mine", base)
	commitOn(t, repo, "agent-office/squashed", "search.go", "package search\n")
	commitOn(t, repo, "agent-office/wip", "wip.go", "package wip\n")
	// main gets the squashed branch's change as its own commit, and moves on.
	if err := os.WriteFile(filepath.Join(repo, "search.go"), []byte("package search\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "search.go")
	gitIn(t, repo, "commit", "-q", "-m", "squashed search")
	a, err := e.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a.ID, hooks.Idle))

	p, err := e.ProjectBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Branches) != 4 {
		t.Fatalf("branches %+v", p.Branches)
	}
	if b := branchByName(t, p, "agent-office/merged"); b.Class != BranchMerged || b.Ahead != 0 || b.InUse != "" {
		t.Fatalf("merged %+v", b)
	}
	if b := branchByName(t, p, "agent-office/squashed"); b.Class != BranchSquashed || b.Ahead != 1 {
		t.Fatalf("squashed %+v", b)
	}
	if b := branchByName(t, p, "agent-office/wip"); b.Class != BranchUnmerged || b.Ahead != 1 {
		t.Fatalf("wip %+v", b)
	}
	live := branchByName(t, p, a.Branch)
	if !strings.Contains(live.InUse, "is running") || live.AgentID != a.ID {
		t.Fatalf("a running agent's branch %+v", live)
	}
	res, err := e.DeleteProjectBranches(ctx, deleteReq([]string{"agent-office/wip"}, branchByName(t, p, "agent-office/merged"),
		branchByName(t, p, "agent-office/squashed"), branchByName(t, p, "agent-office/wip"), live,
		BranchInfo{Name: "feature/mine", Head: base}))
	if err != nil || len(res.Deleted) != 3 || len(res.Refused) != 2 {
		t.Fatalf("project clean-up %+v %v", res, err)
	}
	if ok, _ := e.git.BranchExists(ctx, repo, "feature/mine"); !ok {
		t.Fatal("a branch outside agent-office/ was deleted")
	}
	if ok, _ := e.git.BranchExists(ctx, repo, a.Branch); !ok {
		t.Fatal("the running agent's branch was deleted")
	}
	// The merge target must exist.
	e.opts.MergeTarget = "release"
	if _, err := e.ProjectBranches(ctx); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "release does not exist") {
		t.Fatalf("missing target: %v", err)
	}
}
