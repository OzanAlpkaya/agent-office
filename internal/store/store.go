// Package store persists agent-office state in SQLite (ADR 0004, ADR 0011).
// Queries live in queries.sql and are compiled by sqlc into package db.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open database with migrations applied.
type Store struct {
	DB        *sql.DB
	Q         *db.Queries
	Migration Migration // what Open did to the schema, for the caller to log
}

// Migration describes the schema change made by Open.
type Migration struct {
	From, To int64    // schema versions before and after; equal if nothing was pending
	Backup   string   // copy of the database taken before migrating; empty if none was needed
	Pruned   []string // older backups removed after migrating (ADR 0028)
}

// ErrSchemaTooNew means the database was migrated by a newer agent-office
// than this binary (ADR 0013). Use errors.As with *SchemaTooNewError for details.
var ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")

// SchemaTooNewError reports a database whose schema version is ahead of the
// newest migration embedded in this binary. Opening it is refused: this
// binary's queries do not know the newer schema's columns and rules.
type SchemaTooNewError struct {
	Path     string
	DB       int64 // schema version recorded in the database
	Supports int64 // newest version this binary knows
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("store: %s has schema version %d, but this agent-office supports up to %d: "+
		"run a newer agent-office, or restore a backup taken before the upgrade (%s.pre-v*)",
		e.Path, e.DB, e.Supports, e.Path)
}

func (e *SchemaTooNewError) Unwrap() error { return ErrSchemaTooNew }

// Open opens (or creates) the database at path and applies pending migrations.
// It refuses a database whose schema is newer than this binary, and copies the
// database to <path>.pre-v<N> before migrating an existing database to version N.
func Open(ctx context.Context, path string) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return openWith(ctx, path, sub)
}

func openWith(ctx context.Context, path string, sources fs.FS) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: data dir: %w", err)
	}
	// Shorthand DSN keys apply to every pooled connection (a PRAGMA via Exec
	// would only reach one): WAL for concurrent readers, a busy timeout instead
	// of immediate SQLITE_BUSY, foreign keys on, and write locks taken at BEGIN.
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_foreign_keys", "1")
	q.Set("_txlock", "immediate")
	conn, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: chmod: %w", err)
	}
	m, err := migrate(ctx, conn, path, sources)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Store{DB: conn, Q: db.New(conn), Migration: m}, nil
}

// migrate applies pending migrations (ADR 0012, ADR 0013). goose on its own
// treats a database that is ahead of the embedded migrations as up to date, so
// the version check happens here, before anything is written.
func migrate(ctx context.Context, conn *sql.DB, path string, sources fs.FS) (Migration, error) {
	p, err := goose.NewProvider(goose.DialectSQLite3, conn, sources)
	if err != nil {
		return Migration{}, fmt.Errorf("store: migrations: %w", err)
	}
	srcs := p.ListSources()
	if len(srcs) == 0 {
		return Migration{}, errors.New("store: no migrations embedded")
	}
	latest := srcs[len(srcs)-1].Version
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return Migration{}, fmt.Errorf("store: schema version: %w", err)
	}
	if current > latest {
		return Migration{}, &SchemaTooNewError{Path: path, DB: current, Supports: latest}
	}
	m := Migration{From: current, To: current}
	if current == latest {
		return m, nil
	}
	if current > 0 { // a new database has nothing worth keeping yet
		if m.Backup, err = backup(ctx, conn, fmt.Sprintf("%s.pre-v%d", path, latest)); err != nil {
			return Migration{}, err
		}
	}
	if _, err := p.Up(ctx); err != nil {
		return Migration{}, fmt.Errorf("store: migrate: %w", err)
	}
	m.To = latest
	if m.Backup != "" {
		m.Pruned = pruneBackups(path, KeepBackups)
	}
	return m, nil
}

// KeepBackups is how many pre-migration backups stay next to the database
// (ADR 0028).
const KeepBackups = 3

// pruneBackups removes all but the keep newest <path>.pre-v<N> backups (by
// N) and returns what it removed. Only regular files with exactly that name
// shape are touched; errors leave a file in place.
func pruneBackups(path string, keep int) []string {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil
	}
	prefix := filepath.Base(path) + ".pre-v"
	type bk struct {
		v    int
		name string
	}
	var all []bk
	for _, e := range entries {
		n := e.Name()
		rest, ok := strings.CutPrefix(n, prefix)
		if !ok || !e.Type().IsRegular() {
			continue
		}
		v, err := strconv.Atoi(rest)
		if err != nil || v <= 0 || strconv.Itoa(v) != rest {
			continue
		}
		all = append(all, bk{v, n})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
	var removed []string
	for _, b := range all[min(keep, len(all)):] {
		p := filepath.Join(filepath.Dir(path), b.name)
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	return removed
}

// backup writes a consistent copy of the database to dst with VACUUM INTO. The
// copy goes to a fresh 0600 temp file in the same directory (VACUUM INTO
// accepts an empty file) and is then renamed over dst, so dst is either the
// previous backup or a complete new one, never a partial file.
func backup(ctx context.Context, conn *sql.DB, dst string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("store: backup: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("store: backup: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("store: backup to %s: %w", dst, err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("store: backup: %w", err)
	}
	return dst, nil
}

// Tx runs fn in a transaction and commits if it returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(s.Q.WithTx(tx)); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }
