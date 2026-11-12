package engine

import (
	"context"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// A task's run info: times, branch, last commit, changes, sessions and the
// worker's reported verification (Phase 19).
func TestTaskRuns(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	task, w := tf.worker(t, "Tags table")
	typeLine(t, tf.e, w.ID, "write tags.go package tags")
	typeLine(t, tf.e, w.ID, "write tags_test.go package tags")
	typeLine(t, tf.e, w.ID, "commitall")
	screen(t, tf.e, w.ID, "committed all")
	if _, err := tf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "added the table",
		Verification: "make check: pass"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "x", Verification: string(make([]rune, MaxVerificationLen+1))}); err == nil {
		t.Fatal("an over-long verification accepted")
	}
	runs, err := tf.e.TaskRuns(ctx, task.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	r := runs[0]
	if r.AgentID != w.ID || r.Kind != KindWorker || r.Branch != w.Branch || r.Base == "" || r.StartedAt.IsZero() {
		t.Fatalf("run %+v", r)
	}
	if r.Last == nil || r.Last.Subject != "feat: work" || len(r.Last.SHA) != 40 {
		t.Fatalf("last commit %+v", r.Last)
	}
	if r.Changes == nil || r.Changes.Files != 2 || r.Changes.Added != 2 || r.Changes.Removed != 0 {
		t.Fatalf("changes %+v", r.Changes)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].ID != w.SessionID || r.Sessions[0].EndedAt != nil {
		t.Fatalf("sessions %+v", r.Sessions)
	}
	if len(r.Reports) != 1 || r.Reports[0].Summary != "added the table" || r.Reports[0].Verification != "make check: pass" {
		t.Fatalf("reports %+v", r.Reports)
	}
	if r.Working+r.Waiting < 0 {
		t.Fatalf("durations %d %d", r.Working, r.Waiting)
	}
	typeLine(t, tf.e, w.ID, "stop")
	tf.log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	runs, _ = tf.e.TaskRuns(ctx, task.ID)
	if s := runs[0].Sessions[0]; s.EndedAt == nil || s.ExitReason != ExitReported || runs[0].ExitReason != ExitReported {
		t.Fatalf("after exit %+v %+v", runs[0], s)
	}
	if _, err := tf.e.TaskRuns(ctx, "nope"); err == nil {
		t.Fatal("unknown task")
	}
}

func TestStateDurations(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	ev := func(sec int, st hooks.State) db.ListAgentStateEventsRow {
		return db.ListAgentStateEventsRow{State: string(st), At: t0.Add(time.Duration(sec) * time.Second)}
	}
	evs := []db.ListAgentStateEventsRow{ev(0, hooks.Starting), ev(5, hooks.Idle), ev(10, hooks.Working), ev(70, hooks.WaitingPermission),
		ev(80, hooks.Working), ev(100, hooks.Exited), ev(400, hooks.Starting), ev(402, hooks.Working)}
	working, waiting := stateDurations(evs, t0.Add(412*time.Second))
	if working != 60+20+10 || waiting != 5+5+10+2 {
		t.Fatalf("working %d waiting %d", working, waiting)
	}
}
