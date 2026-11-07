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
