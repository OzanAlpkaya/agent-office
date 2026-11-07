package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "agent-office.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func seedProject(t *testing.T, s *Store) db.Project {
	t.Helper()
	p, err := s.Q.UpsertProject(context.Background(), db.UpsertProjectParams{
		ID: "p1", Name: "linkshelf", RepoPath: "/repo/linkshelf", Slug: "linkshelf-1234abcd",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenMigratesAndIsPrivate(t *testing.T) {
	s, path := open(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode %v", fi.Mode().Perm())
	}
	var mode string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	s.Close()
	// Re-opening applies nothing new and keeps data.
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestTaskConstraintsAndTimes(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	seedProject(t, s)
	now := time.Date(2026, 11, 7, 10, 0, 0, 123456789, time.UTC)
	task, err := s.Q.CreateTask(ctx, db.CreateTaskParams{ID: "t1", ProjectID: "p1", Title: "Sayfalama ekle",
		Description: "GET /bookmarks", Status: "todo", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if !task.CreatedAt.Equal(now) {
		t.Fatalf("time round-trip: got %v want %v", task.CreatedAt, now)
	}
	bad := []db.CreateTaskParams{
		{ID: "t2", ProjectID: "p1", Title: "", Status: "todo", CreatedAt: now, UpdatedAt: now},
		{ID: "t3", ProjectID: "p1", Title: strings.Repeat("x", 201), Status: "todo", CreatedAt: now, UpdatedAt: now},
		{ID: "t4", ProjectID: "p1", Title: "x", Status: "doing", CreatedAt: now, UpdatedAt: now},
		{ID: "t5", ProjectID: "missing", Title: "x", Status: "todo", CreatedAt: now, UpdatedAt: now}, // foreign key
	}
	for _, p := range bad {
		if _, err := s.Q.CreateTask(ctx, p); err == nil {
			t.Errorf("CreateTask(%s) accepted invalid row", p.ID)
		}
	}
}

func TestAgentLifecycleAndRestart(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	seedProject(t, s)
	now := time.Now().UTC()
	err := s.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1", ProjectID: "p1", State: "starting", Sandbox: "host",
			InheritUserSettings: true, StartedAt: now}); err != nil {
			return err
		}
		return q.CreateWorktree(ctx, db.CreateWorktreeParams{AgentID: "a1", ProjectID: "p1", Path: "/wt/a1", Kind: "worktree",
			Branch: "agent-office/a1", BaseCommit: "abc", CreatedAt: now})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Q.UpdateAgentState(ctx, db.UpdateAgentStateParams{State: "working", Activity: "Bash", ID: "a1"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Q.MarkUnfinishedAgentsExited(ctx, sql.NullTime{Time: now, Valid: true})
	if err != nil || n != 1 {
		t.Fatalf("restart marked %d, %v", n, err)
	}
	rows, err := s.Q.ListVisibleAgents(ctx, "p1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("visible agents %+v %v", rows, err)
	}
	a := rows[0]
	if a.State != "exited" || a.ExitReason != "daemon_restart" || a.WorktreePath != "/wt/a1" || !a.InheritUserSettings {
		t.Fatalf("after restart: %+v", a)
	}
	_ = s.Q.MarkAgentRemoved(ctx, db.MarkAgentRemovedParams{RemovedAt: sql.NullTime{Time: now, Valid: true}, ID: "a1"})
	if rows, _ := s.Q.ListVisibleAgents(ctx, "p1"); len(rows) != 0 {
		t.Fatal("removed agent still visible")
	}
}

func TestTxRollsBack(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	seedProject(t, s)
	boom := errors.New("boom")
	err := s.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1", ProjectID: "p1", State: "starting", Sandbox: "host", StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx error %v", err)
	}
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM agents").Scan(&n)
	if n != 0 {
		t.Fatal("rolled-back insert persisted")
	}
}

func TestConcurrentWriters(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	seedProject(t, s)
	if err := s.Q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1", ProjectID: "p1", State: "idle", Sandbox: "host", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Q.InsertEvent(ctx, db.InsertEventParams{AgentID: "a1", SessionID: "s", Kind: "k",
				State: "idle", At: time.Now().UTC(), Tool: strings.Repeat("x", i%3)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent insert: %v", err) // SQLITE_BUSY would show up here without busy_timeout
		}
	}
	ev, err := s.Q.ListEvents(ctx, db.ListEventsParams{AgentID: "a1", Limit: 500})
	if err != nil || len(ev) != 200 {
		t.Fatalf("events %d %v", len(ev), err)
	}
}

func TestImportsConstraints(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	seedProject(t, s)
	now := time.Now().UTC()
	if err := s.Q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1", ProjectID: "p1", State: "exited", Sandbox: "podman", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	imp, err := s.Q.InsertImport(ctx, db.InsertImportParams{AgentID: "a1", Trigger: "exit", Outcome: "diverged",
		Branch: "agent-office/x-rewrite-2", Head: "abc", Previous: "def", Commits: 2, Flagged: `[{"commit":"abc"}]`, At: now})
	if err != nil || imp.ID == 0 {
		t.Fatalf("insert %+v %v", imp, err)
	}
	if last, err := s.Q.LatestImport(ctx, "a1"); err != nil || last.Outcome != "diverged" || last.Commits != 2 {
		t.Fatalf("latest %+v %v", last, err)
	}
	for _, bad := range []db.InsertImportParams{
		{AgentID: "a1", Trigger: "cron", Outcome: "created", At: now},
		{AgentID: "a1", Trigger: "exit", Outcome: "forced", At: now},
		{AgentID: "missing", Trigger: "exit", Outcome: "created", At: now},
	} {
		if _, err := s.Q.InsertImport(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
