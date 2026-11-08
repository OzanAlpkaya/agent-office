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
		`UPDATE agents SET kind = 'boss' WHERE id = 'a1'`,
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

// Migration 0005 (Phase 5) on a database with Phase 4 rows: agents keep their
// roles and every row that references them (the role column is replaced, not
// the table, so no foreign-key cascade runs); 'reviewer' becomes a valid role.
func TestMigration0005KeepsPhase4Rows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-office.db")
	v4, err := openWith(ctx, path, sourcesUpTo(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO projects (id, name, repo_path, slug, created_at) VALUES ('p1', 'linkshelf', '/repo', 'linkshelf-1', ?)`,
		`INSERT INTO tasks (id, project_id, title, status, created_at, updated_at) VALUES ('g1', 'p1', 'Goal', 'review', ?, ?1)`,
		`INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('l1', 'p1', 'g1', 'haiku', 0, 'exited', ?, 'podman', 'lead')`,
		`INSERT INTO agents (id, project_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('w1', 'p1', 'haiku', 0, 'idle', ?, 'host', 'worker')`,
		`INSERT INTO sessions (id, agent_id, started_at) VALUES ('s1', 'l1', ?)`,
		`INSERT INTO worktrees (agent_id, project_id, path, branch, base_commit, status, created_at, kind) VALUES ('l1', 'p1', '/ws/l1', 'agent-office/g', 'abc', 'active', ?, 'clone')`,
		`INSERT INTO events (agent_id, session_id, kind, state, at) VALUES ('l1', 's1', 'Stop', 'idle', ?)`,
		`INSERT INTO imports (agent_id, trigger, outcome, at) VALUES ('l1', 'report_done', 'no_commits', ?)`,
		`INSERT INTO messages (project_id, from_agent_id, to_agent_id, body, created_at) VALUES ('p1', 'w1', 'l1', 'hi', ?)`,
		`INSERT INTO access_requests (agent_id, kind, value, reason, created_at) VALUES ('w1', 'domain', 'example.com', 'docs', ?)`,
	} {
		if _, err := v4.DB.ExecContext(ctx, q, ts); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	v4.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Migration.From != 4 || s.Migration.To < 5 || s.Migration.Backup == "" {
		t.Fatalf("migration %+v", s.Migration)
	}
	rows, err := s.Q.ListVisibleAgents(ctx, "p1")
	if err != nil || len(rows) != 1 || rows[0].ID != "l1" || rows[0].Kind != "lead" || rows[0].Role != "tech-lead" || rows[0].Model != "haiku" ||
		rows[0].Sandbox != "podman" || !rows[0].StartedAt.Equal(ts) {
		t.Fatalf("agents with worktrees %+v %v", rows, err)
	}
	var role string
	if err := s.DB.QueryRow(`SELECT role FROM agents WHERE id = 'w1'`).Scan(&role); err != nil || role != "worker" {
		t.Fatalf("worker role %q %v", role, err)
	}
	for table, want := range map[string]int{"sessions": 1, "worktrees": 1, "events": 1, "imports": 1, "messages": 1,
		"access_requests": 1, "agents": 2} {
		var n int
		if err := s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Errorf("%s: %d rows (%v), want %d", table, n, err, want)
		}
	}
	var fk int
	if err := s.DB.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("foreign key violations %d %v", fk, err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET role = 'reviewer' WHERE id = 'w1'`); err != nil {
		t.Fatalf("reviewer role refused: %v", err)
	}
	pr, err := s.Q.UpsertPullRequest(ctx, db.UpsertPullRequestParams{ProjectID: "p1", AgentID: "l1",
		Branch: "agent-office/g", CreatedAt: ts, UpdatedAt: ts})
	if err != nil || pr.State != "pending" || pr.AcceptedFlagged != "[]" {
		t.Fatalf("pull request %+v %v", pr, err)
	}
	again, err := s.Q.UpsertPullRequest(ctx, db.UpsertPullRequestParams{ProjectID: "p1", AgentID: "l1",
		Branch: "agent-office/g", CreatedAt: ts, UpdatedAt: ts.Add(time.Hour)})
	if err != nil || again.ID != pr.ID {
		t.Fatalf("upsert is not get-or-create: %+v %v", again, err)
	}
	for _, q := range []string{
		`UPDATE agents SET kind = 'boss' WHERE id = 'w1'`,
		`UPDATE agents SET role = '' WHERE id = 'w1'`,
		`UPDATE pull_requests SET state = 'approved'`,
		`INSERT INTO reviews (pr_id, reviewer, head, status, created_at, updated_at) VALUES (1, 'bot', 'h', 'queued', '2026-01-01', '2026-01-01')`,
		`INSERT INTO reviews (pr_id, reviewer, head, status, verdict, created_at, updated_at) VALUES (1, 'internal', 'h', 'done', 'lgtm', '2026-01-01', '2026-01-01')`,
	} {
		if _, err := s.DB.ExecContext(ctx, q); err == nil {
			t.Errorf("CHECK constraint missing: %s", q)
		}
	}
}

// Migration 0006 (Phase 6) on a database with a Phase 5 review: the row keeps
// its fields and gains an empty reply; a reply is length-checked.
func TestMigration0006KeepsReviews(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-office.db")
	v5, err := openWith(ctx, path, sourcesUpTo(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO projects (id, name, repo_path, slug, created_at) VALUES ('p1', 'linkshelf', '/repo', 'linkshelf-1', ?)`,
		`INSERT INTO agents (id, project_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('w1', 'p1', 'haiku', 0, 'exited', ?, 'podman', 'worker')`,
		`INSERT INTO pull_requests (project_id, agent_id, branch, number, state, created_at, updated_at) VALUES ('p1', 'w1', 'b', 3, 'open', ?, ?1)`,
		`INSERT INTO reviews (pr_id, reviewer, head, status, verdict, summary, followup, created_at, updated_at) VALUES (1, 'human', 'h1', 'done', 'comment', 'add a line', 'started', ?, ?1)`,
	} {
		if _, err := v5.DB.ExecContext(ctx, q, ts); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	v5.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Migration.From != 5 || s.Migration.To < 6 {
		t.Fatalf("migration %+v", s.Migration)
	}
	rv, err := s.Q.GetReview(ctx, 1)
	if err != nil || rv.Summary != "add a line" || rv.Followup != "started" || rv.Reply != "" || rv.ReplyUrl != "" {
		t.Fatalf("review %+v %v", rv, err)
	}
	rv, err = s.Q.SetReviewReply(ctx, db.SetReviewReplyParams{Reply: "already there", ReplyUrl: "https://x/1", UpdatedAt: ts, ID: 1})
	if err != nil || rv.Reply != "already there" || rv.ReplyUrl != "https://x/1" {
		t.Fatalf("reply %+v %v", rv, err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE reviews SET reply = ? WHERE id = 1`, string(bytes.Repeat([]byte("x"), 4001))); err == nil {
		t.Error("CHECK constraint missing on reviews.reply")
	}
}

// Migration 0008 (Phase 12) on a database with Phase 8 rows: every agent keeps
// its behaviour as kind, leads are renamed to the tech-lead role, tasks get
// empty plan fields, and the new CHECK constraints hold.
func TestMigration0008KeepsPhase8Rows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-office.db")
	v7, err := openWith(ctx, path, sourcesUpTo(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO projects (id, name, repo_path, slug, created_at) VALUES ('p1', 'linkshelf', '/repo', 'linkshelf-1', ?)`,
		`INSERT INTO tasks (id, project_id, title, status, created_at, updated_at) VALUES ('g1', 'p1', 'Goal', 'review', ?, ?1)`,
		`INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('l1', 'p1', 'g1', 'haiku', 0, 'exited', ?, 'podman', 'lead')`,
		`INSERT INTO agents (id, project_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('w1', 'p1', 'haiku', 0, 'exited', ?, 'podman', 'worker')`,
		`INSERT INTO agents (id, project_id, model, inherit_user_settings, state, started_at, sandbox, role) VALUES ('r1', 'p1', 'haiku', 0, 'exited', ?, 'podman', 'reviewer')`,
		`INSERT INTO sessions (id, agent_id, started_at, turns) VALUES ('s1', 'l1', ?, 3)`,
		`INSERT INTO messages (project_id, from_agent_id, to_agent_id, body, created_at) VALUES ('p1', 'w1', 'l1', 'done', ?)`,
		`INSERT INTO pull_requests (project_id, agent_id, branch, created_at, updated_at) VALUES ('p1', 'w1', 'b', ?, ?1)`,
	} {
		if _, err := v7.DB.ExecContext(ctx, q, ts); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	v7.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Migration.From != 7 || s.Migration.To < 8 {
		t.Fatalf("migration %+v", s.Migration)
	}
	for id, want := range map[string][2]string{"l1": {"lead", "tech-lead"}, "w1": {"worker", "worker"}, "r1": {"reviewer", "reviewer"}} {
		var kind, role string
		if err := s.DB.QueryRow(`SELECT kind, role FROM agents WHERE id = ?`, id).Scan(&kind, &role); err != nil ||
			kind != want[0] || role != want[1] {
			t.Errorf("%s: kind %q role %q %v", id, kind, role, err)
		}
	}
	var turns int
	if err := s.DB.QueryRow(`SELECT turns FROM sessions WHERE id = 's1'`).Scan(&turns); err != nil || turns != 3 {
		t.Fatalf("the lead's session survived: %d %v", turns, err)
	}
	g, err := s.Q.GetTask(ctx, "g1")
	if err != nil || g.DependsOn != "[]" || g.Files != "[]" || g.Acceptance != "[]" || g.Conflicts != "[]" || g.Role != "" {
		t.Fatalf("task %+v %v", g, err)
	}
	m, err := s.Q.ListAgentMessages(ctx, db.ListAgentMessagesParams{AgentID: "l1", Lim: 10})
	if err != nil || len(m) != 1 || m[0].FromUser {
		t.Fatalf("messages %+v %v", m, err)
	}
	pr, err := s.Q.GetPullRequest(ctx, 1)
	if err != nil || pr.ConflictRounds != 0 {
		t.Fatalf("pull request %+v %v", pr, err)
	}
	for _, q := range []string{
		`UPDATE agents SET kind = 'boss' WHERE id = 'w1'`,
		`INSERT INTO inbox (project_id, kind, body, created_at) VALUES ('p1', 'spam', 'x', '2026-01-01')`,
		`INSERT INTO inbox (project_id, kind, body, created_at) VALUES ('p1', 'info', '', '2026-01-01')`,
		`INSERT INTO specs (goal_id, version, status, plan, markdown, created_at) VALUES ('g1', 1, 'draft', '{}', '', '2026-01-01')`,
		`INSERT INTO qa_runs (goal_id, status, created_at, updated_at) VALUES ('g1', 'maybe', '2026-01-01', '2026-01-01')`,
		`INSERT INTO memory (project_id, kind, title, body, status, created_at, updated_at) VALUES ('p1', 'rumour', 't', 'b', 'accepted', '2026-01-01', '2026-01-01')`,
		`INSERT INTO memory (project_id, kind, title, body, status, created_at, updated_at) VALUES ('p1', 'lesson', 't', 'b', 'maybe', '2026-01-01', '2026-01-01')`,
	} {
		if _, err := s.DB.ExecContext(ctx, q); err == nil {
			t.Errorf("CHECK constraint missing: %s", q)
		}
	}
	// One spec version per goal and number.
	ins := `INSERT INTO specs (goal_id, version, status, plan, markdown, created_at) VALUES ('g1', 1, 'proposed', '{}', '', '2026-01-01')`
	if _, err := s.DB.ExecContext(ctx, ins); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, ins); err == nil {
		t.Error("a second version 1 was accepted")
	}
}
