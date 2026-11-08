package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Tests for the findings of the Phase 12 review.

// A sub-task being assigned counts as running for the file-overlap check, so
// two assignments at once cannot both pass it.
func TestOverlapCountsAssignmentsInFlight(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	a, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "A", Files: []string{"api/"}})
	b, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "B", Files: []string{"api/x.go"}})
	e.mu.Lock()
	e.assigning[a.ID] = true // A is being started right now
	e.mu.Unlock()
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: b.ID}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "api/x.go") {
		t.Fatalf("overlap with an assignment in flight: %v", err)
	}
	// And a plan approval treats it as touched.
	if !e.touchedTasks()[a.ID] {
		t.Fatal("a task being assigned is not touched")
	}
}

// The goal is reported only when every sub-task is reported or closed.
func TestGoalNeedsEverySubTask(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	left, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "Never assigned"})
	if _, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "done"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), left.ID) {
		t.Fatalf("goal with an unstarted sub-task: %v", err)
	}
	done := StatusDone
	if _, err := e.UpdateTask(ctx, left.ID, TaskPatch{Status: &done}); err != nil { // the user closes it
		t.Fatal(err)
	}
	if _, err := tm.ReportDone(ctx, lead, mcp.ReportDoneInput{Summary: "done"}); err != nil {
		t.Fatalf("after the user closed it: %v", err)
	}
}

// A message to the user of kind briefing is the assistant's only.
func TestBriefingKindIsTheAssistants(t *testing.T) {
	tf := newTeam(t)
	if _, err := tf.team.MessageUser(context.Background(), tf.lead.ID, mcp.MessageUserInput{Kind: "briefing",
		Body: "all good"}); !isUser(err, ErrInvalid) {
		t.Fatalf("briefing from the lead: %v", err)
	}
}

// QA's pass must contain every branch it was given.
func TestQAPassNeedsTheBranches(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm := tf.e, tf.team
	_, w := tf.worker(t, "Tags")
	typeLine(t, e, w.ID, "commit")
	screen(t, e, w.ID, "committed")
	if _, err := tm.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "x"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, e, w.ID, "stop")
	tf.log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	qa, err := tm.StartQA(ctx, tf.lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(qa.AgentID, hooks.Idle))
	typeLine(t, e, qa.AgentID, "write qa-notes.txt nothing merged")
	typeLine(t, e, qa.AgentID, "commitall")
	screen(t, e, qa.AgentID, "committed all")
	if _, err := tm.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "pass", Summary: "ok"}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), qa.Branches[0]) {
		t.Fatalf("pass without the branch: %v", err)
	}
	typeLine(t, e, qa.AgentID, "merge "+qa.Branches[0])
	screen(t, e, qa.AgentID, "merge done (err=false)")
	if out, err := tm.ReportQA(ctx, qa.AgentID, mcp.ReportQAInput{Verdict: "pass", Summary: "ok"}); err != nil ||
		out.Status != QAPassed {
		t.Fatalf("pass after merging: %+v %v", out, err)
	}
}

// Plan versions per goal are rate-limited, and an escalation starts an
// exited architect again only a few times.
func TestPlanAndEscalationLimits(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	arch, err := e.Start(ctx, StartRequest{TaskID: tf.goal.ID, Role: RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	for i := 0; i < maxPlansPerWindow; i++ {
		if _, err := tm.SubmitPlan(ctx, arch.ID, samplePlan()); err != nil {
			t.Fatalf("plan %d: %v", i, err)
		}
	}
	if _, err := tm.SubmitPlan(ctx, arch.ID, samplePlan()); !isUser(err, ErrLimit) {
		t.Fatalf("one plan too many: %v", err)
	}
	// Escalations: the architect is started again at most maxArchitectRelaunches times.
	for i := 0; i <= maxArchitectRelaunches; i++ {
		mark := tf.log.mark()
		if err := e.Stop(arch.ID); err != nil {
			t.Fatal(err)
		}
		_, err := tm.SendMessage(ctx, lead, mcp.SendMessageInput{To: "architect", Body: "the plan breaks"})
		if i == maxArchitectRelaunches {
			if !isUser(err, ErrInvalid) || !strings.Contains(err.Error(), "message_user") {
				t.Fatalf("relaunch %d: %v", i+1, err)
			}
			break
		}
		if err != nil {
			t.Fatalf("relaunch %d: %v", i+1, err)
		}
		tf.log.waitFrom(t, startWait, mark, func(u Update) bool {
			return u.Type == "agent" && u.Agent.ID == arch.ID && u.Agent.State == string(hooks.Idle) && u.Agent.ExitedAt == nil
		})
		time.Sleep(50 * time.Millisecond)
	}
}
