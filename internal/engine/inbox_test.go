package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Who may talk to the user follows the escalation chain (ADR 0030); the
// user's reply reaches the agent as the user's words (DoD).
func TestMessageUserAndChain(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	_, w := tf.worker(t, "Add tag column")

	// A worker with a running lead asks its lead.
	if _, err := tm.MessageUser(ctx, w.ID, mcp.MessageUserInput{Kind: "question", Body: "?"}); !isUser(err, ErrForbidden) ||
		!strings.Contains(err.Error(), `to="lead"`) {
		t.Fatalf("worker with a lead: %v", err)
	}
	// A lead without an architect may talk to the user.
	out, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "question", Body: "Which tag colours?"})
	if err != nil || out.MessageID == 0 {
		t.Fatalf("lead: %+v %v", out, err)
	}
	if _, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "gossip", Body: "x"}); !isUser(err, ErrInvalid) {
		t.Fatalf("bad kind: %v", err)
	}
	if _, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "info", Body: " \n "}); !isUser(err, ErrInvalid) {
		t.Fatalf("empty body: %v", err)
	}
	items, err := e.Inbox(ctx)
	if err != nil || len(items) != 1 || items[0].AgentID != lead || items[0].GoalID != tf.goal.ID ||
		items[0].Role != RoleTechLead || items[0].Kind != InboxQuestion || items[0].ReadAt != nil {
		t.Fatalf("inbox %+v %v", items, err)
	}
	tf.log.wait(t, time.Second, func(u Update) bool { return u.Type == "inbox" && u.Inbox.ID == items[0].ID })

	// The user answers: the lead gets it as the user's words, not as another
	// agent's information.
	mark := tf.log.mark()
	typeLine(t, e, lead, "turn") // the lead finishes its first turn and is idle
	tf.log.waitFrom(t, 5*time.Second, mark, agentState(lead, hooks.Working))
	tf.log.waitFrom(t, 5*time.Second, mark, agentState(lead, hooks.Idle))
	it, err := e.ReplyInbox(ctx, items[0].ID, "Blue and green.")
	if err != nil || it.Reply != "Blue and green." || it.AnsweredAt == nil || it.ReadAt == nil {
		t.Fatalf("reply %+v %v", it, err)
	}
	got := screen(t, e, lead, "INPUT:[agent-office] New message(s) for you from the user")
	if !strings.Contains(got, "From the user: > Blue and green.") {
		t.Fatalf("delivery: %s", got)
	}
	if m := messagesTo(t, e, lead); len(m) == 0 || !m[0].FromUser || m[0].From != "" {
		t.Fatalf("stored message %+v", m)
	}
	if _, err := e.MarkInboxRead(ctx, 999); err != ErrNotFound {
		t.Fatalf("unknown item: %v", err)
	}

	// The unread cap stops one agent from flooding the user.
	for i := 0; i < maxInboxUnread; i++ { // the answered item was read
		if _, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "info", Body: "note"}); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	if _, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "info", Body: "one more"}); !isUser(err, ErrLimit) {
		t.Fatalf("over the unread cap: %v", err)
	}

	// A worker whose lead is gone may leave the user a message on its card.
	if err := e.Stop(lead); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.MessageUser(ctx, w.ID, mcp.MessageUserInput{Kind: "blocked", Body: "My lead left."}); err != nil {
		t.Fatalf("worker without a lead: %v", err)
	}
	// Replies to an agent that is not running are refused.
	items, _ = e.Inbox(ctx)
	var leadItem int64
	for _, x := range items {
		if x.AgentID == lead {
			leadItem = x.ID
		}
	}
	if _, err := e.ReplyInbox(ctx, leadItem, "late"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("reply to an exited agent: %v", err)
	}
}

// With an architect on the goal, the lead escalates to it and the architect
// answers the lead; only the architect talks to the user (ADR 0030).
func TestArchitectChain(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	arch, err := e.Start(ctx, StartRequest{TaskID: tf.goal.ID, Role: RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	if _, err := tm.MessageUser(ctx, lead, mcp.MessageUserInput{Kind: "question", Body: "?"}); !isUser(err, ErrForbidden) ||
		!strings.Contains(err.Error(), `to="architect"`) {
		t.Fatalf("lead with an architect: %v", err)
	}
	if out, err := tm.SendMessage(ctx, lead, mcp.SendMessageInput{To: "architect", Body: "the plan breaks"}); err != nil ||
		out.To != arch.ID {
		t.Fatalf("lead to architect: %+v %v", out, err)
	}
	if out, err := tm.SendMessage(ctx, arch.ID, mcp.SendMessageInput{To: "lead", Body: "do b first"}); err != nil || out.To != lead {
		t.Fatalf("architect to lead: %+v %v", out, err)
	}
	if _, err := tm.SendMessage(ctx, arch.ID, mcp.SendMessageInput{To: "someone", Body: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("architect to another agent: %v", err)
	}
	if _, err := tm.MessageUser(ctx, arch.ID, mcp.MessageUserInput{Kind: "question", Body: "REST or gRPC?"}); err != nil {
		t.Fatalf("architect to the user: %v", err)
	}
	// The assistant has no task and talks to the user only.
	as, err := e.Start(ctx, StartRequest{Role: RoleAssistant})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(as.ID, hooks.Idle))
	if _, err := tm.SendMessage(ctx, as.ID, mcp.SendMessageInput{To: lead, Body: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("assistant to an agent: %v", err)
	}
	if _, err := tm.MessageUser(ctx, as.ID, mcp.MessageUserInput{Kind: "briefing", Body: "All quiet."}); err != nil {
		t.Fatalf("assistant briefing: %v", err)
	}
	if _, err := e.MessageAgent(ctx, as.ID, "brief me"); err != nil {
		t.Fatalf("user to assistant: %v", err)
	}
}
