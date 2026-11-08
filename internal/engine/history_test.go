package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// Every session's terminal output is saved (ADR 0036): while it runs, at
// exit, readable after a daemon restart, per session, deleted with the agent
// and by retention.
func TestSessionHistory(t *testing.T) {
	saved := historyEvery
	historyEvery = 50 * time.Millisecond
	t.Cleanup(func() { historyEvery = saved })
	f := newFixture(t)
	f.opts.HistoryDir = t.TempDir()
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e1.Shutdown) // before historyEvery is restored
	log := record(t, e1)
	a, _ := startAndExit(t, e1, log, "Remember me")
	other, err := e1.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	path := e1.historyPath(a.ID, a.SessionID)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved terminal %v %v", fi, err)
	}
	if d, _ := os.Stat(e1.historyDir(a.ID)); d.Mode().Perm() != 0o700 {
		t.Fatalf("history dir mode %v", d.Mode())
	}
	from := log.mark()
	r, err := e1.Resume(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	log.waitFrom(t, 5*time.Second, from, agentState(a.ID, hooks.Idle))
	ss, err := e1.Sessions(ctx, a.ID)
	if err != nil || len(ss) != 2 || ss[0].ID != r.SessionID || !ss[0].Live || ss[0].ResumedFrom != a.SessionID ||
		ss[1].ID != a.SessionID || ss[1].Live || !ss[1].Terminal || ss[1].EndedAt == nil || ss[1].Turns != 1 {
		t.Fatalf("sessions %+v %v", ss, err)
	}
	live, err := e1.SessionTerminal(ctx, a.ID, r.SessionID)
	if err != nil || !strings.Contains(string(live), "RESUMED:"+a.SessionID) {
		t.Fatalf("live terminal %q %v", live, err)
	}
	// The daemon goes away; the next one shows both sessions' output.
	e1.Shutdown()
	e2 := f.engine(t)
	if _, err := e2.Terminal(a.ID); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("live terminal after restart: %v", err)
	}
	ss, _ = e2.Sessions(ctx, a.ID)
	if len(ss) != 2 || !ss[0].Terminal || !ss[1].Terminal || ss[0].Live {
		t.Fatalf("sessions after restart %+v", ss)
	}
	first, err := e2.SessionTerminal(ctx, a.ID, a.SessionID)
	if err != nil || !strings.Contains(string(first), "fake claude ready") || strings.Contains(string(first), "RESUMED:") {
		t.Fatalf("first session %q %v", first, err)
	}
	second, err := e2.SessionTerminal(ctx, a.ID, r.SessionID)
	if err != nil || !strings.Contains(string(second), "RESUMED:"+a.SessionID) {
		t.Fatalf("second session %q %v", second, err)
	}
	for _, sid := range []string{other.SessionID, "00000000-0000-4000-8000-000000000000", "../../etc/passwd"} {
		if _, err := e2.SessionTerminal(ctx, a.ID, sid); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %q of another agent or unknown: %v", sid, err)
		}
	}

	// Retention deletes old files, removal the rest.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(e2.historyPath(other.ID, other.SessionID), old, old); err != nil {
		t.Fatal(err)
	}
	e2.opts.EventRetention = 24 * time.Hour
	e2.pruneHistory()
	if _, err := os.Stat(e2.historyDir(other.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old history kept: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recent history pruned: %v", err)
	}
	if _, err := e2.Remove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e2.historyDir(a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history kept after removal: %v", err)
	}
}
