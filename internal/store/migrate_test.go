package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// embeddedLatest is the newest embedded schema version. Migrations are
// numbered 1..n without gaps, so it is the number of files.
func embeddedLatest(t *testing.T) int64 {
	t.Helper()
	m, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil || len(m) == 0 {
		t.Fatalf("embedded migrations: %v %v", m, err)
	}
	return int64(len(m))
}

// sourcesWithNext returns the embedded migrations plus a fake next migration,
// so the upgrade path is tested independently of the real migrations.
func sourcesWithNext(t *testing.T) (fs.FS, int64) {
	t.Helper()
	mfs := fstest.MapFS{}
	err := fs.WalkDir(migrations, "migrations", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := migrations.ReadFile(p)
		if err != nil {
			return err
		}
		mfs[filepath.Base(p)] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next := embeddedLatest(t) + 1
	mfs[fmt.Sprintf("%04d_test_next.sql", next)] = &fstest.MapFile{Data: []byte("-- +goose Up\nALTER TABLE projects ADD COLUMN note TEXT NOT NULL DEFAULT '';\n")}
	return mfs, next
}

func schemaVersion(t *testing.T, path string) int64 {
	t.Helper()
	s, err := openWith(context.Background(), path, mustSub(t))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer s.Close()
	return s.Migration.To
}

func mustSub(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// An older binary must not run against a schema it does not know (ADR 0012's
// open gap). goose alone treats such a database as up to date.
func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s, path := open(t)
	seedProject(t, s)
	if _, err := s.DB.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, 1)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path)
	if s2 != nil {
		s2.Close()
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.DB != 999 || tooNew.Supports != embeddedLatest(t) || tooNew.Path != path {
		t.Fatalf("error details: %+v", tooNew)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused database was modified")
	}
	if m, _ := filepath.Glob(path + ".pre-v*"); len(m) != 0 {
		t.Fatalf("refused database was backed up: %v", m)
	}
}

func TestOpenNewDatabaseNeedsNoBackup(t *testing.T) {
	s, path := open(t)
	latest := embeddedLatest(t)
	if s.Migration.From != 0 || s.Migration.To != latest || s.Migration.Backup != "" {
		t.Fatalf("migration %+v", s.Migration)
	}
	s.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Migration.From != latest || s2.Migration.To != latest || s2.Migration.Backup != "" {
		t.Fatalf("up-to-date reopen: %+v", s2.Migration)
	}
	if m, _ := filepath.Glob(path + ".pre-v*"); len(m) != 0 {
		t.Fatalf("unexpected backups: %v", m)
	}
}

func TestOpenBacksUpBeforeMigrating(t *testing.T) {
	ctx := context.Background()
	s, path := open(t)
	seedProject(t, s)
	s.Close()

	sources, next := sourcesWithNext(t)
	want := fmt.Sprintf("%s.pre-v%d", path, next)
	// A leftover from an earlier attempt is replaced by a fresh, complete copy.
	if err := os.WriteFile(want, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	s2, err := openWith(ctx, path, sources)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Migration.From != next-1 || s2.Migration.To != next || s2.Migration.Backup != want {
		t.Fatalf("migration %+v", s2.Migration)
	}
	var note string
	if err := s2.DB.QueryRow(`SELECT note FROM projects WHERE id = 'p1'`).Scan(&note); err != nil {
		t.Fatalf("migrated db: %v", err)
	}
	s2.Close()

	fi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", fi.Mode().Perm())
	}
	// The backup is the old schema with the old data: an older binary can open it.
	if v := schemaVersion(t, want); v != next-1 {
		t.Fatalf("backup schema version %d", v)
	}
	b, err := openWith(ctx, want, mustSub(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Q.UpsertProject(ctx, db.UpsertProjectParams{ID: "p1", Name: "linkshelf",
		RepoPath: "/repo/linkshelf", Slug: "linkshelf-1234abcd", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("backup is not usable by the old schema: %v", err)
	}
	var n int
	if err := b.DB.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup rows %d %v", n, err)
	}
	if tmp, _ := filepath.Glob(want + ".tmp-*"); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
}

// sourcesUpTo returns the embedded migrations with versions <= v.
func sourcesUpTo(t *testing.T, v int64) fs.FS {
	t.Helper()
	mfs := fstest.MapFS{}
	for i := int64(1); i <= v; i++ {
		m, err := fs.Glob(migrations, fmt.Sprintf("migrations/%04d_*.sql", i))
		if err != nil || len(m) != 1 {
			t.Fatalf("migration %d: %v %v", i, m, err)
		}
		b, _ := migrations.ReadFile(m[0])
		mfs[filepath.Base(m[0])] = &fstest.MapFile{Data: b}
	}
	return mfs
}

// Migration 0002 (Phase 3) on a database with Phase 2 rows: the rows survive
// and read as host-mode agents with linked worktrees.
func TestMigration0002KeepsPhase2Rows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-office.db")
	v1, err := openWith(ctx, path, sourcesUpTo(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO projects (id, name, repo_path, slug, created_at) VALUES ('p1', 'linkshelf', '/repo', 'linkshelf-1', ?)`,
		`INSERT INTO agents (id, project_id, model, inherit_user_settings, state, started_at) VALUES ('a1', 'p1', 'haiku', 1, 'exited', ?)`,
		`INSERT INTO worktrees (agent_id, project_id, path, branch, base_commit, status, created_at) VALUES ('a1', 'p1', '/wt/a1', 'agent-office/x', 'abc', 'kept', ?)`,
	} {
		if _, err := v1.DB.ExecContext(ctx, q, ts); err != nil {
			t.Fatal(err)
		}
	}
	v1.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Migration.From != 1 || s.Migration.To < 2 || s.Migration.Backup == "" {
		t.Fatalf("migration %+v", s.Migration)
	}
	rows, err := s.Q.ListVisibleAgents(ctx, "p1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	r := rows[0]
	if r.ID != "a1" || r.Model != "haiku" || !r.InheritUserSettings || r.WorktreePath != "/wt/a1" ||
		r.WorktreeStatus != "kept" || r.Sandbox != "host" || r.WorktreeKind != "worktree" || !r.StartedAt.Equal(ts) {
		t.Fatalf("row after migration %+v", r)
	}
	for _, q := range []string{
		`UPDATE agents SET sandbox = 'docker' WHERE id = 'a1'`,
		`UPDATE worktrees SET kind = 'copy' WHERE agent_id = 'a1'`,
	} {
		if _, err := s.DB.ExecContext(ctx, q); err == nil {
			t.Errorf("CHECK constraint missing: %s", q)
		}
	}
}

// Migration 0004 (Phase 4) on a database with Phase 3 rows: agents read as
// workers, tasks as top-level user tasks, and the rebuilt imports table keeps
// every row, its ids and its id sequence.
func TestMigration0004KeepsPhase3Rows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-office.db")
	v3, err := openWith(ctx, path, sourcesUpTo(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO projects (id, name, repo_path, slug, created_at) VALUES ('p1', 'linkshelf', '/repo', 'linkshelf-1', ?)`,
		`INSERT INTO tasks (id, project_id, title, status, created_at, updated_at) VALUES ('t1', 'p1', 'Add tags', 'review', ?, ?1)`,
		`INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at, sandbox) VALUES ('a1', 'p1', 't1', 'haiku', 0, 'exited', ?, 'podman')`,
		`INSERT INTO worktrees (agent_id, project_id, path, branch, base_commit, status, created_at, kind) VALUES ('a1', 'p1', '/ws/a1', 'agent-office/x', 'abc', 'active', ?, 'clone')`,
		`INSERT INTO imports (id, agent_id, trigger, outcome, branch, head, commits, flagged, at) VALUES (7, 'a1', 'exit', 'created', 'agent-office/x', 'def', 2, '[]', ?)`,
		`INSERT INTO imports (id, agent_id, trigger, outcome, at) VALUES (9, 'a1', 'manual', 'up_to_date', ?)`,
	} {
		if _, err := v3.DB.ExecContext(ctx, q, ts); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	v3.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Migration.From != 3 || s.Migration.To < 4 || s.Migration.Backup == "" {
		t.Fatalf("migration %+v", s.Migration)
	}
	rows, err := s.Q.ListVisibleAgents(ctx, "p1")
	if err != nil || len(rows) != 1 || rows[0].Role != "worker" || rows[0].Sandbox != "podman" {
		t.Fatalf("agents %+v %v", rows, err)
	}
	task, err := s.Q.GetTask(ctx, "t1")
	if err != nil || task.ParentTaskID.Valid || task.CreatedBy != "" || task.Status != "review" {
		t.Fatalf("task %+v %v", task, err)
	}
	imps, err := s.Q.ListImports(ctx, db.ListImportsParams{AgentID: "a1", Limit: 10})
	if err != nil || len(imps) != 2 {
		t.Fatalf("imports %+v %v", imps, err)
	}
	if imps[0].ID != 9 || imps[1].ID != 7 || imps[1].Commits != 2 || imps[1].Head != "def" || !imps[1].At.Equal(ts) {
		t.Fatalf("imports after rebuild %+v", imps)
	}
	added, err := s.Q.InsertImport(ctx, db.InsertImportParams{AgentID: "a1", Trigger: "report_done",
		Outcome: "up_to_date", Flagged: "[]", At: ts})
	if err != nil || added.ID <= 9 {
		t.Fatalf("new import %+v %v (ids must continue after the old ones)", added, err)
	}
	for _, q := range []string{
		`UPDATE agents SET role = 'boss' WHERE id = 'a1'`,
		`UPDATE imports SET trigger = 'push' WHERE id = 7`,
		`INSERT INTO access_requests (agent_id, kind, value, reason, created_at) VALUES ('a1', 'root', 'x', 'y', '2026-01-01')`,
		`INSERT INTO messages (project_id, to_agent_id, body, created_at) VALUES ('p1', 'a1', '', '2026-01-01')`,
	} {
		if _, err := s.DB.ExecContext(ctx, q); err == nil {
			t.Errorf("CHECK constraint missing: %s", q)
		}
	}
	var idx int
	if err := s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'imports_agent'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("imports index %d %v", idx, err)
	}
}
