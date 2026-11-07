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

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open database with migrations applied.
type Store struct {
	DB *sql.DB
	Q  *db.Queries
}

// Open opens (or creates) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
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
	if err := migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &Store{DB: conn, Q: db.New(conn)}, nil
}

func migrate(ctx context.Context, conn *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, conn, sub)
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
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
