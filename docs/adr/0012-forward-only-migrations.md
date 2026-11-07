# 0012. Forward-only database migrations

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

ADR 0004 chose numbered goose migrations, embedded in the binary and applied at startup. goose
supports a `-- +goose Down` section per migration for rolling back. The first migration
(`internal/store/migrations/0001_init.sql`) was drafted with a down section that dropped every
table.

Forces:

- The database is the user's local history (tasks, agents, sessions, worktrees, events). There is no
  server-side backup, so dropping a table loses data for good.
- The binary only ever migrates **up** (`Provider.Up` in `store.Open`); nothing in the product runs
  a down migration. A down section would be code that is never exercised by the product and only
  reachable through manual tooling.
- The development environment has a guard hook that blocks destructive SQL (`DROP`, `TRUNCATE`)
  from the shell. It blocked writing the drafted down section through a heredoc. Nothing ran; the
  hook pointed at the question of whether the down section should exist at all.

## Options considered

1. **Up and down migrations.** Pros: the goose convention; easy local rollback while iterating on a
   schema. Cons: down sections are destructive by design, rarely tested, and drift from reality as
   data accumulates; one wrong command drops the user's history.
2. **Forward-only migrations.** Pros: no destructive path exists in the tooling; every schema
   change, including a fix for a bad migration, is a new numbered file that runs the same way
   everywhere. Cons: undoing a change means writing a new corrective migration; during development
   a broken local schema is fixed by deleting the dev database rather than migrating down.
3. **Recreate the schema on version mismatch.** Pros: trivial. Cons: loses all history on every
   upgrade; unacceptable once real users have data.

## Decision

Option 2. Migrations contain only `-- +goose Up`. Schema changes go into a new numbered migration
plus `queries.sql`, followed by `make generate` (also stated in `CLAUDE.md`). Applied migrations are
never edited.

## Consequences

- Today no code path in the repo drops or truncates tables.
- A mistake in a released migration is fixed by a follow-up migration, not by rolling back.
- Tests and the e2e run use temporary or disposable databases, so they never need a down path.
- An older binary opening a database migrated by a newer one was left open here. It is decided in
  ADR 0013: the older binary refuses to start, and every migration is preceded by a backup that
  the older binary can open.
- SQLite's limited `ALTER TABLE` means some future changes need the copy-table pattern inside an
  up migration (create the new table, copy the rows, drop the old one, rename). Its drop happens
  only after the data is copied, in one transaction; such a migration gets a test that checks the
  rows survive.
