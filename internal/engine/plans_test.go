package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// planFixture is a goal with an Architect that submitted samplePlan.
func planFixture(t *testing.T) (*fixture, *Engine, *updateLog, Task, AgentInfo, mcp.SubmitPlanOutput) {
	t.Helper()
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 5
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	arch, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	out, err := e.Team().SubmitPlan(ctx, arch.ID, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	return f, e, log, goal, arch, out
}

// Request changes: a running Architect gets the user's words; an exited one
// starts again with them in its prompt (Phase 19, ADR 0031 amended).
func TestRequestPlanChanges(t *testing.T) {
	_, e, log, goal, arch, out := planFixture(t)
	ctx := context.Background()
	if _, err := e.RequestSpecChanges(ctx, out.SpecID, "  "); !isInvalid(err) {
		t.Fatalf("empty request: %v", err)
	}
	if _, err := e.RequestSpecChanges(ctx, out.SpecID, strings.Repeat("x", maxChangeRequest+1)); !isInvalid(err) {
		t.Fatalf("long request: %v", err)
	}
	s, err := e.RequestSpecChanges(ctx, out.SpecID, "Split the API sub-task in two.")
	if err != nil || s.ChangeRequest != "Split the API sub-task in two." || s.ChangeRequestedAt == nil || s.Status != SpecProposed {
		t.Fatalf("request %+v %v", s, err)
	}
	typeLine(t, e, arch.ID, "stop") // its turn ends: the user's words are delivered on the Stop hook
	screen(t, e, arch.ID, "Split the API sub-task in two.")
	screen(t, e, arch.ID, "submit_plan")

	// Approved: the Architect ends; a request on the approved plan starts it again.
	res, err := e.ApproveSpec(ctx, out.SpecID)
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(arch.ID, hooks.Exited))
	if res.Lead != nil {
		log.wait(t, startWait, agentState(res.Lead.ID, hooks.Idle))
	}
	mark := log.mark()
	if _, err := e.RequestSpecChanges(ctx, out.SpecID, "Add a CLI sub-task."); err != nil {
		t.Fatal(err)
	}
	log.waitFrom(t, startWait, mark, agentState(arch.ID, hooks.Idle))
	screen(t, e, arch.ID, "The user asks for changes to version 1 of the plan (approved)")
	screen(t, e, arch.ID, "Add a CLI sub-task.")
	if a, _ := e.Get(arch.ID); a.TaskID != goal.ID {
		t.Fatalf("relaunched architect %+v", a)
	}
	// A superseded version cannot be asked about.
	s2, err := e.Team().SubmitPlan(ctx, arch.ID, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApproveSpec(ctx, s2.SpecID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RequestSpecChanges(ctx, out.SpecID, "again"); !isInvalid(err) {
		t.Fatalf("request on a superseded version: %v", err)
	}
}

// An Architect started again for an escalation ends once it answered and
// nothing waits for it, instead of holding a slot (Phase 19).
func TestArchitectEndsAfterAnsweringEscalation(t *testing.T) {
	old := architectIdleGrace
	architectIdleGrace = 200 * time.Millisecond
	t.Cleanup(func() { architectIdleGrace = old })
	_, e, log, goal, arch, out := planFixture(t)
	ctx := context.Background()
	tm := e.Team()
	// Before approval an idle Architect stays: the plan waits for the user.
	typeLine(t, e, arch.ID, "turn")
	time.Sleep(3 * architectIdleGrace)
	if a, _ := e.Get(arch.ID); a.State == string(hooks.Exited) {
		t.Fatal("an architect with a proposed plan ended")
	}
	res, err := e.ApproveSpec(ctx, out.SpecID)
	if err != nil || res.Lead == nil {
		t.Fatalf("approve %+v %v", res, err)
	}
	lead := res.Lead.ID
	log.wait(t, 5*time.Second, agentState(arch.ID, hooks.Exited))
	log.wait(t, startWait, agentState(lead, hooks.Idle))
	if e.architectRestorable(ctx, goal.ID) {
		t.Fatal("a restore would resume an architect whose plan is approved")
	}

	mark := log.mark()
	if _, err := tm.SendMessage(ctx, lead, mcp.SendMessageInput{To: "architect", Body: "handler.go does not exist"}); err != nil {
		t.Fatal(err)
	}
	log.waitFrom(t, startWait, mark, agentState(arch.ID, hooks.Idle))
	typeLine(t, e, arch.ID, "turn") // first turn ends: the lead's message follows, so it stays
	screen(t, e, arch.ID, "handler.go does not exist")
	if a, _ := e.Get(arch.ID); a.State == string(hooks.Exited) {
		t.Fatal("the architect ended before it got the lead's message")
	}
	if _, err := tm.SendMessage(ctx, arch.ID, mcp.SendMessageInput{To: "lead", Body: "use handlers.go"}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, e, arch.ID, "stop") // it answered; its turn ends
	log.waitFrom(t, 10*time.Second, mark, agentState(arch.ID, hooks.Exited))
	if a, _ := e.Get(arch.ID); a.ExitReason != ExitPlanApproved {
		t.Fatalf("exit reason %q", a.ExitReason)
	}
}

func isInvalid(err error) bool { return errors.Is(err, ErrInvalid) }
