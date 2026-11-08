package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Two sub-tasks edit the same lines: report_done finds the conflict with
// merge-tree in the main repository and shows it on both tasks and to the
// lead (DoD). After the other branch reaches the base, the worker is
// relaunched to merge the base and resolve it (DoD).
func TestConflictsOnReportAndResolve(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	repo := tf.f.repo
	gitIn(t, repo, "config", "user.name", "Ozan")
	gitIn(t, repo, "config", "user.email", "o@example.com")
	ta, wa := tf.worker(t, "Handler A")
	tb, wb := tf.worker(t, "Handler B")
	report := func(w AgentInfo, text string) mcp.ReportDoneOutput {
		t.Helper()
		typeLine(t, e, w.ID, "write internal/httpapi/handler.go "+text)
		typeLine(t, e, w.ID, "commitall")
		screen(t, e, w.ID, "committed all")
		out, err := tm.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "done"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := report(wa, "func A() {}"); out.Conflicts != "" {
		t.Fatalf("the first report conflicts with nothing: %+v", out)
	}
	out := report(wb, "func B() {}")
	if !strings.Contains(out.Conflicts, ta.ID) || !strings.Contains(out.Conflicts, "internal/httpapi/handler.go") {
		t.Fatalf("second report: %+v", out)
	}
	for _, c := range []struct{ task, other string }{{tb.ID, ta.ID}, {ta.ID, tb.ID}} {
		got, _ := e.getTask(ctx, c.task)
		cs := taskFromRow(got).Conflicts
		if len(cs) != 1 || cs[0].With != c.other || !slices.Equal(cs[0].Files, []string{"internal/httpapi/handler.go"}) {
			t.Fatalf("conflicts on %s: %+v", c.task, cs)
		}
	}
	if m := messagesTo(t, e, lead); len(m) == 0 || !strings.Contains(m[0].Body, "agent-office found conflicts") {
		t.Fatalf("lead's report: %+v", m)
	}
	ll, _ := tm.ListTasks(ctx, lead)
	found := false
	for _, x := range ll.Tasks {
		found = found || (x.ID == tb.ID && len(x.Conflicts) == 1)
	}
	if !found {
		t.Fatalf("list_tasks shows the conflict: %+v", ll.Tasks)
	}

	// Nothing to resolve against the base yet.
	if err := e.ResolveConflict(ctx, tb.ID); err == nil || !strings.Contains(err.Error(), "merges cleanly") {
		t.Fatalf("resolve without a base conflict: %v", err)
	}
	// A's branch reaches the base (the user merges it): B now conflicts with
	// the base. Its worker has ended after reporting; it is relaunched.
	for _, w := range []AgentInfo{wa, wb} {
		typeLine(t, e, w.ID, "stop")
		tf.log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	}
	gitIn(t, repo, "merge", "-q", "--no-edit", wa.Branch)
	base := gitIn(t, repo, "rev-parse", "HEAD")
	mark := tf.log.mark()
	if err := e.ResolveConflict(ctx, tb.ID); err != nil {
		t.Fatal(err)
	}
	tf.log.waitFrom(t, startWait, mark, agentState(wb.ID, hooks.Idle))
	if got, _ := e.getTask(ctx, tb.ID); got.Status != StatusInProgress {
		t.Fatalf("task after relaunch: %s", got.Status)
	}
	got, _ := e.getTask(ctx, tb.ID)
	if cs := taskFromRow(got).Conflicts; !slices.ContainsFunc(cs, func(c Conflict) bool { return c.With == "" }) {
		t.Fatalf("the base conflict is recorded: %+v", cs)
	}
	// Nothing new yet: refused.
	if _, err := tm.ReportDone(ctx, wb.ID, mcp.ReportDoneInput{Summary: "x"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "merge the base") {
		t.Fatalf("report without the merge: %v", err)
	}
	if _, err := tm.ReportNoChange(ctx, wb.ID, mcp.ReportNoChangeInput{Reason: "x"}); !isUser(err, ErrInvalid) {
		t.Fatalf("report_no_change in a conflict round: %v", err)
	}
	typeLine(t, e, wb.ID, "merge "+base)
	screen(t, e, wb.ID, "merge done")
	typeLine(t, e, wb.ID, "write internal/httpapi/handler.go func A() {} func B() {}")
	typeLine(t, e, wb.ID, "commitall")
	screen(t, e, wb.ID, "committed all")
	out, err := tm.ReportDone(ctx, wb.ID, mcp.ReportDoneInput{Summary: "merged"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = e.getTask(ctx, tb.ID)
	if cs := taskFromRow(got).Conflicts; len(cs) != 0 || out.Conflicts != "" {
		t.Fatalf("after the merge: %+v %q", cs, out.Conflicts)
	}
}

// After a pull request merges on GitHub, the daemon fetches the base, finds
// that another open pull request conflicts with it now, and relaunches that
// worker to resolve it (DoD), within the automatic round limit.
func TestMergeRelaunchesConflictingWorker(t *testing.T) {
	gf := newGHFixture(t, func(o *GitHubOptions) { o.Reviewer = ReviewerNone })
	ctx := context.Background()
	e := gf.e
	start := func(title, text string) (Task, AgentInfo) {
		t.Helper()
		task, _ := e.CreateTask(ctx, TaskInput{Title: title})
		w, err := e.Start(ctx, StartRequest{TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		gf.log.wait(t, startWait, agentState(w.ID, hooks.Idle))
		typeLine(t, e, w.ID, "write handler.go "+text)
		typeLine(t, e, w.ID, "commitall")
		screen(t, e, w.ID, "committed all")
		if _, err := gf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "done"}); err != nil {
			t.Fatal(err)
		}
		return task, w
	}
	_, wa := start("Handler A", "func A() {}")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen && p.Branch == wa.Branch })
	tb, wb := start("Handler B", "func B() {}")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen && p.Branch == wb.Branch })

	// A human merges A on GitHub: the remote's main moves to A's work.
	headA := remoteHead(t, gf.bare, wa.Branch)
	gitIn(t, gf.bare, "update-ref", "refs/heads/main", headA)
	gf.srv.SetMerged(1)
	gf.log.wait(t, 20*time.Second, func(u Update) bool {
		return u.Type == "task" && u.Task.ID == tb.ID && slices.ContainsFunc(u.Task.Conflicts, func(c Conflict) bool {
			return c.With == "" && c.Branch == "origin/main" && slices.Equal(c.Files, []string{"handler.go"})
		})
	})
	// B's worker (idle, the user's own agent) is ended and relaunched to merge.
	gf.log.wait(t, 20*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == wb.ID && u.Agent.ExitReason == ExitFollowUp
	})
	gf.log.wait(t, 20*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == wb.ID && u.Agent.State == string(hooks.Idle) && u.Agent.ExitedAt == nil
	})
	screen(t, e, wb.ID, "origin/main moved on")
	prs, _ := e.q.ListPullRequests(ctx, e.project.ID)
	for _, p := range prs {
		if p.Branch == wb.Branch && p.ConflictRounds != 1 {
			t.Fatalf("conflict rounds %d", p.ConflictRounds)
		}
	}
	if got := gitIn(t, gf.f.repo, "rev-parse", "refs/remotes/origin/main"); got != headA {
		t.Fatalf("fetched base %s, want %s", got, headA)
	}
}
