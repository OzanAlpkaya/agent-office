package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

func withStall(d time.Duration) func(*Options) {
	return func(o *Options) { o.StallAfter = d }
}

// endFirstTurn finishes the fake lead's first turn, as a real lead's ends
// after it planned: messages are typed into it from then on (ADR 0018).
func endFirstTurn(t *testing.T, tf *teamFixture) {
	t.Helper()
	typeLine(t, tf.e, tf.lead.ID, "stop")
	eventually(t, "the lead's first turn ends", func() bool {
		tf.e.mu.Lock()
		defer tf.e.mu.Unlock()
		return tf.e.agents[tf.lead.ID].turns > 0
	})
}

// stallNudges counts the watchdog's messages to the lead.
func stallNudges(t *testing.T, e *Engine, lead string) []Message {
	t.Helper()
	var out []Message
	for _, m := range messagesTo(t, e, lead) {
		if m.From == "" && strings.HasPrefix(m.Body, "[agent-office] Status check") {
			out = append(out, m)
		}
	}
	return out
}

// TestStallWatchdogWakesTheLead is incident 5 (ADR 0052): the lead is idle,
// its worker was stopped and a message waits for it; nobody moves. The lead
// gets a summary naming the stopped worker, the tool to resume it and the
// waiting message; nudges stop after maxNudges and the user is told.
func TestStallWatchdogWakesTheLead(t *testing.T) {
	tf := newTeamWith(t, withStall(250*time.Millisecond))
	e, ctx := tf.e, context.Background()
	endFirstTurn(t, tf)
	task, w := tf.worker(t, "Tags API")
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.team.SendMessage(ctx, tf.lead.ID, mcp.SendMessageInput{To: w.ID, Body: "use the v2 schema"}); err != nil {
		t.Fatal(err)
	}
	var first string
	eventually(t, "the lead is woken", func() bool {
		for _, m := range stallNudges(t, e, tf.lead.ID) {
			if strings.Contains(m.Body, "Stopped by the user") {
				first = m.Body
				return true
			}
		}
		return false
	})
	for _, want := range []string{
		w.Name + " (" + w.ID + ") (worker, sub-task " + task.ID + "): exited: Stopped by the user",
		`resume_agent(agent_id="` + w.ID + `")`, `reclaim_task(task_id="` + task.ID + `")`,
		"Undelivered messages: 1 to " + w.Name, "(tech-lead, you): idle",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("summary lacks %q:\n%s", want, first)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		items, _ := e.Inbox(ctx)
		if len(items) > 0 && strings.Contains(items[0].Body, "has not moved") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the user was not told; nudges %d", len(stallNudges(t, e, tf.lead.ID)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := len(stallNudges(t, e, tf.lead.ID))
	if n > maxNudges+1 { // one more if the worker's stop came after a first nudge
		t.Fatalf("%d nudges", n)
	}
	time.Sleep(time.Second)
	if m := len(stallNudges(t, e, tf.lead.ID)); m != n {
		t.Fatalf("nudged again after the user was told: %d -> %d", n, m)
	}
}

// TestStallWatchdogWaitsForTheUser: a question to the user is not a stall;
// once it is answered, the goal can stall again.
func TestStallWatchdogWaitsForTheUser(t *testing.T) {
	tf := newTeamWith(t, withStall(150*time.Millisecond))
	e, ctx := tf.e, context.Background()
	endFirstTurn(t, tf)
	q, err := tf.team.MessageUser(ctx, tf.lead.ID, mcp.MessageUserInput{Kind: "question", Body: "Which database?"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := len(stallNudges(t, e, tf.lead.ID)); n != 0 {
		t.Fatalf("nudged while a question waits on the user: %d", n)
	}
	if _, err := e.ReplyInbox(ctx, q.MessageID, "SQLite"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a nudge after the answer", func() bool { return len(stallNudges(t, e, tf.lead.ID)) > 0 })
}

// TestStallWatchdogIgnoresReportedGoals: a reported goal waits for the user's
// review, not for its team.
func TestStallWatchdogIgnoresReportedGoals(t *testing.T) {
	tf := newTeamWith(t, withStall(100*time.Millisecond))
	e, ctx := tf.e, context.Background()
	g, err := e.getTask(ctx, tf.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.setTaskStatus(ctx, g, StatusReview); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	if n := len(stallNudges(t, e, tf.lead.ID)); n != 0 {
		t.Fatalf("nudged a reported goal: %d", n)
	}
}
