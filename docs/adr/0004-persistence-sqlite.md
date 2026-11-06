# 0004. Persistence: SQLite (modernc) + sqlc + goose

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

The daemon stores agents, sessions, tasks, worktrees, messages, access requests and reduced events
for a single user on a single machine. It must survive restarts, keep the binary static (no cgo),
and stay simple to operate. Evidence: `docs/research/library-apis.md`.

## Options considered

1. **SQLite via `modernc.org/sqlite`** (pure Go). Pros: no cgo, a static binary, SQL with real
   constraints and transactions, WAL for concurrent readers. Cons: somewhat slower than the C
   driver; a single writer.
2. **SQLite via `mattn/go-sqlite3`.** Pros: faster, the most widely used. Cons: needs cgo, so
   cross-compiles and the static binary get harder.
3. **PostgreSQL.** Pros: familiar, powerful. Cons: a server to install and run, overkill for a
   local tool.
4. **Embedded KV (bbolt/badger) or JSON files.** Pros: trivial. Cons: no relational queries or
   constraints; the task board and history need them.

## Decision

Option 1. Queries are written in SQL and turned into typed Go code by **sqlc** (`engine: sqlite`,
`database/sql`), pinned as a dev tool. Schema changes are numbered **goose** migrations embedded
with `go:embed` and applied at startup.

DSN: `file:<path>?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate`. These
shorthand keys apply to every pooled connection; a `PRAGMA` sent via `Exec` would not.

## Consequences

- Writes are serialized. Fine for this workload; keep transactions short.
- The database lives under the user's data dir (`$XDG_DATA_HOME/agent-office/`) and is gitignored.
- sqlc adds a code-generation step; generated code is committed so `go build` needs no tools.
- Integration tests run against a temporary database file, not mocks.
