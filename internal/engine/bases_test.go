package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// baseOf is the commit an agent's workspace started from.
func baseOf(t *testing.T, e *Engine, id string) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok {
		t.Fatalf("no agent %s", id)
	}
	return a.baseCommit
}

// workAndReport has worker id write files, commit and report, then stop.
func workAndReport(t *testing.T, tf *teamFixture, id string, writes ...string) {
	t.Helper()
	for _, w := range writes {
		typeLine(t, tf.e, id, "write "+w)
	}
	typeLine(t, tf.e, id, "commitall")
	screen(t, tf.e, id, "committed all")
	if _, err := tf.team.ReportDone(context.Background(), id, mcp.ReportDoneInput{Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, tf.e, id, "stop")
	tf.log.wait(t, startWait, agentState(id, hooks.Exited))
}

// runQA starts QA, merges every branch it gets, and reports a pass.
func runQA(t *testing.T, tf *teamFixture) mcp.StartQAOutput {
	t.Helper()
	ctx := context.Background()
	qa, err := tf.team.StartQA(ctx, tf.lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Idle))
	for i, b := range qa.Branches {
		typeLine(t, tf.e, qa.AgentID, "merge "+b)
		// The fake reads its input in order: the marker shows once the merge is done.
		marker := fmt.Sprintf("merged-%d", i)
		typeLine(t, tf.e, qa.AgentID, marker)
		if out := screen(t, tf.e, qa.AgentID, "INPUT:"+marker); strings.Contains(out, "merge done (err=true)") {
			t.Fatalf("a QA merge failed:\n%s", out)
		}
	}
	if _, err := tf.team.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "pass", Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, tf.e, qa.AgentID, "stop")
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Exited))
	return qa
}

func mergeGoal(t *testing.T, tf *teamFixture) GoalMerge {
	t.Helper()
	ctx := context.Background()
	p, err := tf.e.MergePreview(ctx, tf.goal.ID)
	if err != nil || !p.Ready {
		t.Fatalf("merge preview %+v %v", p, err)
	}
	m, err := tf.e.MergeGoal(ctx, tf.goal.ID, MergeRequest{TargetHead: p.TargetHead, SourceHead: p.SourceHead, Message: p.Message})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A sub-task the lead adds after QA ran starts from QA's integration head, not
// the goal's first commit (ADR 0063).
func TestFollowUpStartsFromFinishedWork(t *testing.T) {
	tf := passedGoal(t, nil)
	ctx := context.Background()
	runs, _ := tf.e.q.ListGoalQARuns(ctx, tf.goal.ID)
	qaHead := runs[0].Head
	_, w := tf.worker(t, "Polish: tag colours")
	if b := baseOf(t, tf.e, w.ID); b != qaHead {
		t.Fatalf("follow-up base %s, want QA's head %s", short(b), short(qaHead))
	}
	if !strings.Contains(gitIn(t, w.WorktreePath, "ls-files"), "tags.go") {
		t.Fatal("the follow-up's workspace lacks the goal's work")
	}
}

// Items 5 and 6 of Phase 19 end to end with fake agents: after the goal is
// squash-merged into main, a follow-up round (a new sub-task, and a sub-task
// taken up again on its pre-squash branch) is tested by QA from main and
// merges into main without conflicts (ADR 0063).
func TestFollowUpAfterSquashMergeMergesCleanly(t *testing.T) {
	tf := passedGoal(t, nil)
	e, repo, ctx := tf.e, tf.f.repo, context.Background()
	first := mergeGoal(t, tf)
	mainAfter := gitIn(t, repo, "rev-parse", "main")
	if first.Commit != mainAfter {
		t.Fatalf("merge commit %s, main %s", first.Commit, mainAfter)
	}
	children, _ := e.q.ListChildTasks(ctx, nullString(tf.goal.ID))
	tagsTask := children[0]

	// 1. A new sub-task starts from main, which has the squash.
	newTask, w2 := tf.worker(t, "Polish: CLI output")
	if b := baseOf(t, e, w2.ID); b != mainAfter {
		t.Fatalf("new sub-task's base %s, want main %s", short(b), short(mainAfter))
	}
	workAndReport(t, tf, w2.ID, "cli.go package cli")

	// 2. The tags sub-task again: it continues on its pre-squash branch and
	// changes the file the squash added, which a plain merge into main reports
	// as a conflict.
	if _, err := e.UpdateTask(ctx, tagsTask.ID, TaskPatch{Status: Ptr(StatusTodo)}); err != nil {
		t.Fatal(err)
	}
	out, err := tf.team.AssignTask(ctx, tf.lead.ID, mcp.AssignTaskInput{TaskID: tagsTask.ID})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(out.AgentID, hooks.Idle))
	workAndReport(t, tf, out.AgentID, "tags.go package tags // with colours")
	_, head, _ := e.branchOfTask(ctx, tagsTask.ID)
	if res, err := e.git.MergeTree(ctx, repo, mainAfter, head); err != nil || res.Clean {
		t.Fatalf("the pre-squash branch should conflict with main in a plain merge: %+v %v", res, err)
	}
	if ts, _ := e.getTask(ctx, tagsTask.ID); len(taskFromRow(ts).Conflicts) != 0 {
		t.Fatalf("the squash shows as a conflict: %+v", taskFromRow(ts).Conflicts)
	}

	// 3. QA starts from main, merges the new work only, the old branch ported.
	qa := runQA(t, tf)
	if b := baseOf(t, e, qa.AgentID); b != mainAfter {
		t.Fatalf("QA base %s, want main %s", short(b), short(mainAfter))
	}
	run, _ := e.q.LatestQARun(ctx, tf.goal.ID)
	merged := qaRunFromRow(run).Merged
	if len(merged) != 2 {
		t.Fatalf("QA merged %+v", merged)
	}
	ports := 0
	for _, b := range merged {
		if b.Port {
			ports++
			if b.TaskID != tagsTask.ID || !strings.HasPrefix(b.Branch, "agent-office/port-") || b.Merge == b.Head {
				t.Fatalf("port %+v", b)
			}
		} else if b.TaskID != newTask.ID {
			t.Fatalf("merged %+v", b)
		}
	}
	if ports != 1 {
		t.Fatalf("%d ports, want 1: %+v", ports, merged)
	}

	// 4. The lead reports; the second merge into main is clean.
	if _, err := tf.team.ReportDone(ctx, tf.lead.ID, mcp.ReportDoneInput{Summary: "polished", CommitMessage: "feat: polish"}); err != nil {
		t.Fatal(err)
	}
	second := mergeGoal(t, tf)
	if second.Commit == first.Commit || gitIn(t, repo, "rev-parse", "main") != second.Commit {
		t.Fatalf("second merge %+v", second)
	}
	if got := gitIn(t, repo, "show", "main:tags.go"); got != "package tags // with colours" {
		t.Fatalf("tags.go on main %q", got)
	}
	if got := gitIn(t, repo, "show", "main:cli.go"); got != "package cli" {
		t.Fatalf("cli.go on main %q", got)
	}
	if gitIn(t, repo, "status", "--porcelain") != "" {
		t.Fatal("the checkout is not clean after the merge")
	}
}
