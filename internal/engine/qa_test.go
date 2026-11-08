package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// QA merges the reported branches on its integration branch, reports pass or
// fail with bugs as sub-tasks, and gates the goal's report (DoD).
func TestQAGatesTheGoal(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 4
	f.opts.QA = true
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	tf := &teamFixture{f: f, e: e, log: log, team: e.Team()}
	ctx := context.Background()
	tm := tf.team
	tf.goal, _ = e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	var err error
	if tf.lead, err = e.Start(ctx, StartRequest{TaskID: tf.goal.ID, Role: RoleTechLead}); err != nil {
		t.Fatal(err)
	}
	lead := tf.lead.ID
	log.wait(t, startWait, agentState(lead, hooks.Idle))
	created, err := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "Tags table", Acceptance: []string{"tags are stored"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	out1, _ := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: created.ID})
	log.wait(t, startWait, agentState(out1.AgentID, hooks.Idle))
	t2, w2 := tf.worker(t, "Tag filter")

	report := func(id, file string) {
		t.Helper()
		typeLine(t, e, id, "write "+file+" package x")
		typeLine(t, e, id, "commitall")
		screen(t, e, id, "committed all")
		if _, err := tm.ReportDone(ctx, id, mcp.ReportDoneInput{Summary: "done"}); err != nil {
			t.Fatal(err)
		}
		typeLine(t, e, id, "stop")
		log.wait(t, startWait, agentState(id, hooks.Exited))
	}
	report(out1.AgentID, "store.go")
	if _, err := tm.StartQA(ctx, lead); !isUser(err, ErrInvalid) || !strings.Contains(err.Error(), t2.ID) {
		t.Fatalf("QA while a sub-task is in progress: %v", err)
	}
	report(w2.ID, "filter.go")
	if m := messagesTo(t, e, lead); len(m) == 0 || !strings.Contains(m[0].Body, "Every sub-task of the goal is reported: call start_qa") {
		t.Fatalf("last report to the lead: %+v", m)
	}
	if _, err := tm.StartQA(ctx, out1.AgentID); !isUser(err, ErrForbidden) {
		t.Fatalf("start_qa by a worker: %v", err)
	}
	if _, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "all done"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "start_qa") {
		t.Fatalf("goal report before QA: %v", err)
	}

	runQA := func() mcp.StartQAOutput {
		t.Helper()
		qa, err := tm.StartQA(ctx, lead)
		if err != nil {
			t.Fatal(err)
		}
		if len(qa.Branches) != 2 {
			t.Fatalf("branches %v", qa.Branches)
		}
		log.wait(t, startWait, agentState(qa.AgentID, hooks.Idle))
		screen(t, e, qa.AgentID, "Merge these branches into your branch")
		screen(t, e, qa.AgentID, "tags are stored")
		for _, b := range qa.Branches {
			typeLine(t, e, qa.AgentID, "merge "+b)
		}
		typeLine(t, e, qa.AgentID, "merges-done") // lines are handled in order
		if scr := screen(t, e, qa.AgentID, "INPUT:merges-done"); strings.Count(scr, "merge done (err=false)") != len(qa.Branches) {
			t.Fatalf("merges:\n%s", scr)
		}
		return qa
	}
	qa := runQA()
	if a, _ := e.Get(qa.AgentID); a.Kind != KindQA {
		t.Fatalf("QA agent %+v", a)
	}
	if _, err := tm.StartQA(ctx, lead); err == nil {
		t.Fatal("a second QA at the same time")
	}
	if _, err := tm.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "fail", Summary: "x"}); !isUser(err, ErrInvalid) {
		t.Fatalf("fail without bugs: %v", err)
	}
	res, err := tm.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "fail", Summary: "filter ignores case",
		Bugs: []mcp.QABug{{Title: "Filter is case-sensitive", Description: "?tag=Go misses go", TaskID: t2.ID}}})
	if err != nil || res.Status != QAFailed || len(res.BugTasks) != 1 {
		t.Fatalf("report_qa %+v %v", res, err)
	}
	bug, _ := e.getTask(ctx, res.BugTasks[0])
	if bug.Title != "Bug: Filter is case-sensitive" || bug.ParentTaskID.String != tf.goal.ID || bug.CreatedBy != qa.AgentID ||
		!strings.Contains(bug.Description, t2.ID) {
		t.Fatalf("bug task %+v", bug)
	}
	if m := messagesTo(t, e, lead); len(m) == 0 || !strings.Contains(m[0].Body, "FAIL") || !strings.Contains(m[0].Body, bug.ID) {
		t.Fatalf("lead's QA message %+v", m)
	}
	runs, _ := e.QARuns(ctx)
	if len(runs) != 1 || runs[0].Status != QAFailed || runs[0].Head == "" || len(runs[0].Merged) != 2 ||
		runs[0].Bugs[0].BugTaskID != bug.ID {
		t.Fatalf("runs %+v", runs)
	}
	typeLine(t, e, qa.AgentID, "stop")
	log.wait(t, startWait, agentState(qa.AgentID, hooks.Exited))
	if _, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "x"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "failed") {
		t.Fatalf("goal report after a failed QA: %v", err)
	}

	// A passing run on the same work, but the bug sub-task is still open.
	qa2 := runQA()
	if _, err := tm.ReportQA(ctx, qa2.AgentID, mcp.ReportQAInput{Verdict: "pass", Summary: "all good"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "x"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), bug.ID) {
		t.Fatalf("goal report with an open bug: %v", err)
	}
	done := StatusDone
	if _, err := e.UpdateTask(ctx, bug.ID, TaskPatch{Status: &done}); err != nil { // the user closes it
		t.Fatal(err)
	}
	if out, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "shipped"}); err != nil || out.Status != StatusReview {
		t.Fatalf("goal report after QA passed: %+v %v", out, err)
	}
}

// A QA agent that ends without reporting leaves its run as an error.
func TestQAExitWithoutReport(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm := tf.e, tf.team
	_, w := tf.worker(t, "Tags")
	typeLine(t, e, w.ID, "commit")
	screen(t, e, w.ID, "committed")
	if _, err := tm.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "x"}); err != nil {
		t.Fatal(err)
	}
	qa, err := tm.StartQA(ctx, tf.lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Idle))
	if err := e.Stop(qa.AgentID); err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, 5*time.Second, func(u Update) bool { return u.Type == "qa_run" && u.QARun.Status == QAError })
}
