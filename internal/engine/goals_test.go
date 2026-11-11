package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

const leadMessage = "feat: tags for links\n\nAdds a tags table and a filter by tag."

// passedGoal runs a goal to review with fake agents (ADR 0033): a worker
// commits and reports one sub-task, QA merges it and passes, the Tech Lead
// reports with a commit message. configure may change the options first.
func passedGoal(t *testing.T, configure func(*Options)) *teamFixture {
	t.Helper()
	f := newFixture(t)
	f.opts.MaxAgents = 4
	f.opts.QA = true
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	if configure != nil {
		configure(&f.opts)
	}
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	tf := &teamFixture{f: f, e: e, log: record(t, e), team: e.Team()}
	ctx := context.Background()
	tm := tf.team
	tf.goal, _ = e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	var err error
	if tf.lead, err = e.Start(ctx, StartRequest{TaskID: tf.goal.ID, Role: RoleTechLead}); err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(tf.lead.ID, hooks.Idle))
	_, w := tf.worker(t, "Tags table")
	typeLine(t, e, w.ID, "write tags.go package tags")
	typeLine(t, e, w.ID, "commitall")
	screen(t, e, w.ID, "committed all")
	if _, err := tm.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, e, w.ID, "stop")
	tf.log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	qa, err := tm.StartQA(ctx, tf.lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Idle))
	for _, b := range qa.Branches {
		typeLine(t, e, qa.AgentID, "merge "+b)
	}
	typeLine(t, e, qa.AgentID, "merges-done")
	screen(t, e, qa.AgentID, "INPUT:merges-done")
	if _, err := tm.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "pass", Summary: "all good"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, e, qa.AgentID, "stop")
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Exited))
	if _, err := tm.ReportDone(ctx, tf.lead.ID, mcp.ReportDoneInput{Summary: "shipped tags", CommitMessage: leadMessage}); err != nil {
		t.Fatal(err)
	}
	return tf
}

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrMergeRefused) || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want a refusal with %q", err, want)
	}
}

// TestMergeGoalIntoCheckedOutMain is item 7 of Phase 17 (ADR 0055) on a
// throwaway repository with a local bare remote: the preview, every refusal
// the confirmation can meet, the squash merge into the checked-out main, and
// the push.
func TestMergeGoalIntoCheckedOutMain(t *testing.T) {
	tf := passedGoal(t, nil)
	e, repo, ctx := tf.e, tf.f.repo, context.Background()
	rec, err := e.GoalRecord(ctx, tf.goal.ID)
	if err != nil || rec.Report == nil || rec.Report.CommitMessage != leadMessage || rec.Report.Summary != "shipped tags" ||
		len(rec.QARuns) != 1 || rec.QARuns[0].Status != QAPassed || len(rec.SubTasks) != 1 || len(rec.Agents) != 3 {
		t.Fatalf("record %+v %v", rec, err)
	}
	p, err := e.MergePreview(ctx, tf.goal.ID)
	mainHead := gitIn(t, repo, "rev-parse", "main")
	if err != nil || !p.Ready || p.Target != "main" || p.TargetHead != mainHead || p.SourceHead != rec.QARuns[0].Head ||
		p.Branch != rec.QARuns[0].Branch || p.Message != leadMessage || !p.CheckedOut || p.Files != 1 || p.Untested != 0 ||
		p.Remote != "origin" || p.RemoteURL != "" {
		t.Fatalf("preview %+v %v", p, err)
	}
	req := MergeRequest{TargetHead: p.TargetHead, SourceHead: p.SourceHead, Message: p.Message}

	// A dirty tree: refused, in the preview and the merge.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.MergePreview(ctx, tf.goal.ID); p.Ready || !strings.Contains(p.Reason, "uncommitted changes") {
		t.Fatalf("dirty preview %+v", p)
	}
	_, err = e.MergeGoal(ctx, tf.goal.ID, req)
	refusedWith(t, err, "uncommitted changes")
	gitIn(t, repo, "checkout", "--", "README.md")
	// An untracked file is fine.
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The target moved since the confirmation: refused. A non-conflicting
	// commit is allowed after a new confirmation, which says QA did not test it.
	if err := os.WriteFile(filepath.Join(repo, "NOTES.md"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "NOTES.md")
	gitIn(t, repo, "commit", "-q", "-m", "notes")
	_, err = e.MergeGoal(ctx, tf.goal.ID, req)
	refusedWith(t, err, "main moved since the confirmation")
	p, _ = e.MergePreview(ctx, tf.goal.ID)
	if !p.Ready || p.Untested != 1 {
		t.Fatalf("preview after main moved %+v", p)
	}

	// A conflict: main adds the file the goal adds, differently.
	if err := os.WriteFile(filepath.Join(repo, "tags.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "tags.go")
	gitIn(t, repo, "commit", "-q", "-m", "conflicting tags.go")
	if p, _ := e.MergePreview(ctx, tf.goal.ID); p.Ready || !strings.Contains(p.Reason, "conflicts in tags.go") {
		t.Fatalf("conflict preview %+v", p)
	}
	conflicted := gitIn(t, repo, "rev-parse", "main")
	_, err = e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: conflicted, SourceHead: p.SourceHead, Message: "m"})
	refusedWith(t, err, "conflicts in tags.go")
	gitIn(t, repo, "reset", "-q", "--hard", "HEAD~1")

	// A bare remote with main as it is now, then someone else's push.
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "push", "-q", "origin", "main")
	if _, err := e.PushGoal(ctx, tf.goal.ID); !errors.Is(err, ErrMergeRefused) || !strings.Contains(err.Error(), "merge it first") {
		t.Fatalf("push before merge: %v", err)
	}

	// The merge.
	p, _ = e.MergePreview(ctx, tf.goal.ID)
	if !p.Ready || p.RemoteURL != bare {
		t.Fatalf("preview %+v", p)
	}
	edited := "feat: tags\n\nEdited in the confirmation."
	m, err := e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: p.TargetHead, SourceHead: p.SourceHead, Message: edited})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitIn(t, repo, "rev-parse", "main"); head != m.Commit || m.TargetBefore != p.TargetHead || m.Target != "main" {
		t.Fatalf("merge %+v, main at %s", m, head)
	}
	if parents := gitIn(t, repo, "rev-list", "--parents", "-n1", m.Commit); parents != m.Commit+" "+p.TargetHead {
		t.Fatalf("parents %q", parents)
	}
	if msg := gitIn(t, repo, "log", "-1", "--format=%an|%B", m.Commit); !strings.HasPrefix(msg, "Ozan|"+edited) ||
		!strings.Contains(msg, "Agent-Office-Goal: "+tf.goal.ID) {
		t.Fatalf("commit %q", msg)
	}
	if _, err := os.Stat(filepath.Join(repo, "tags.go")); err != nil {
		t.Fatal("the checked-out main's files were not updated")
	}
	if st := gitIn(t, repo, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("tree after merge: %q", st)
	}
	if p, _ := e.MergePreview(ctx, tf.goal.ID); p.Ready || !strings.Contains(p.Reason, "already merged") || p.Merged == nil {
		t.Fatalf("preview after merge %+v", p)
	}
	_, err = e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: m.Commit, SourceHead: p.SourceHead, Message: "again"})
	refusedWith(t, err, "already merged")

	// The remote moved: the push is refused with git's reason, nothing forced.
	other := t.TempDir()
	gitIn(t, other, "clone", "-q", "-b", "main", bare, ".")
	if err := os.WriteFile(filepath.Join(other, "THEIRS.md"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "THEIRS.md")
	gitIn(t, other, "commit", "-q", "-m", "theirs")
	gitIn(t, other, "push", "-q", "origin", "main")
	theirs := gitIn(t, bare, "rev-parse", "main")
	_, err = e.PushGoal(ctx, tf.goal.ID)
	refusedWith(t, err, "git push origin main failed")
	if gitIn(t, bare, "rev-parse", "main") != theirs {
		t.Fatal("the remote was overwritten")
	}
	gitIn(t, bare, "update-ref", "refs/heads/main", p.TargetHead) // they undo it

	pushed, err := e.PushGoal(ctx, tf.goal.ID)
	if err != nil || pushed.PushedAt == nil || pushed.PushRemote != "origin" {
		t.Fatalf("push %+v %v", pushed, err)
	}
	if gitIn(t, bare, "rev-parse", "main") != m.Commit {
		t.Fatal("the remote does not have the merge")
	}
	_, err = e.PushGoal(ctx, tf.goal.ID)
	refusedWith(t, err, "already pushed")
	if rec, _ := e.GoalRecord(ctx, tf.goal.ID); len(rec.Merges) != 1 || rec.Merges[0].PushedAt == nil {
		t.Fatalf("record merges %+v", rec.Merges)
	}
}

// A target that is not checked out moves by compare-and-swap; one checked
// out in another worktree is refused.
func TestMergeGoalIntoBranchNotCheckedOut(t *testing.T) {
	tf := passedGoal(t, func(o *Options) { o.MergeTarget = "release" })
	e, repo, ctx := tf.e, tf.f.repo, context.Background()
	if p, _ := e.MergePreview(ctx, tf.goal.ID); p.Ready || !strings.Contains(p.Reason, "release does not exist") {
		t.Fatalf("missing target %+v", p)
	}
	gitIn(t, repo, "branch", "release", "main")
	wt := filepath.Join(t.TempDir(), "rel")
	gitIn(t, repo, "worktree", "add", "-q", wt, "release")
	if p, _ := e.MergePreview(ctx, tf.goal.ID); p.Ready || !strings.Contains(p.Reason, "checked out in another worktree") {
		t.Fatalf("target in another worktree %+v", p)
	}
	gitIn(t, repo, "worktree", "remove", wt)
	p, err := e.MergePreview(ctx, tf.goal.ID)
	if err != nil || !p.Ready || p.CheckedOut {
		t.Fatalf("preview %+v %v", p, err)
	}
	mainBefore := gitIn(t, repo, "rev-parse", "main")
	m, err := e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: p.TargetHead, SourceHead: p.SourceHead, Message: p.Message})
	if err != nil {
		t.Fatal(err)
	}
	if gitIn(t, repo, "rev-parse", "release") != m.Commit || gitIn(t, repo, "rev-parse", "main") != mainBefore {
		t.Fatal("release did not move, or main did")
	}
	if _, err := e.PushGoal(ctx, tf.goal.ID); !errors.Is(err, ErrMergeRefused) || !strings.Contains(err.Error(), `no remote "origin"`) {
		t.Fatalf("push without a remote: %v", err)
	}
}

// Merge is refused before the Tech Lead reported, and the message is required.
func TestMergeGoalNeedsAReport(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	p, err := tf.e.MergePreview(ctx, tf.goal.ID)
	if err != nil || p.Ready || !strings.Contains(p.Reason, "the goal is in_progress") {
		t.Fatalf("preview of a running goal %+v %v", p, err)
	}
	_, err = tf.e.MergeGoal(ctx, tf.goal.ID, MergeRequest{Message: " "})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty message: %v", err)
	}
	sub, _ := tf.worker(t, "Sub")
	if _, err := tf.e.MergePreview(ctx, sub.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a sub-task is not a goal: %v", err)
	}
	if _, err := tf.e.GoalRecord(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown goal: %v", err)
	}
}

// Closing a goal (ADR 0054): its exited agents are removed in one action,
// work-preserving, and the archive's record still lists them.
func TestRemoveGoalAgents(t *testing.T) {
	tf := passedGoal(t, nil)
	e, ctx := tf.e, context.Background()
	if _, err := e.RemoveGoalAgents(ctx, tf.goal.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cleanup of a goal in review: %v", err)
	}
	done := StatusDone
	if _, err := e.UpdateTask(ctx, tf.goal.ID, TaskPatch{Status: &done}); err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(tf.lead.ID, hooks.Exited))
	res, err := e.RemoveGoalAgents(ctx, tf.goal.ID)
	if err != nil || len(res.Removed) != 3 || len(res.Kept) != 0 {
		t.Fatalf("cleanup %+v %v", res, err)
	}
	if left := e.List(); len(left) != 0 {
		t.Fatalf("agents left: %+v", left)
	}
	rec, err := e.GoalRecord(ctx, tf.goal.ID)
	if err != nil || len(rec.Agents) != 3 {
		t.Fatalf("record %+v %v", rec, err)
	}
	for _, a := range rec.Agents {
		if !a.Removed || a.ExitReason == "" {
			t.Fatalf("agent in the record %+v", a)
		}
	}
	if p, _ := e.MergePreview(ctx, tf.goal.ID); !p.Ready {
		t.Fatalf("a done goal with its agents removed can still be merged: %+v", p)
	}
	if again, err := e.RemoveGoalAgents(ctx, tf.goal.ID); err != nil || len(again.Removed) != 0 {
		t.Fatalf("second cleanup %+v %v", again, err)
	}
}
