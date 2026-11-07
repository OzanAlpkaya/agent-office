# 0013. Refuse newer database schemas; back up before migrating

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Migrations are forward-only (ADR 0012), embedded in the binary and applied by `store.Open`. ADR
0012 left one gap open, to be closed before the second migration ships (Phase 3 adds one): what
happens when an **older** binary opens a database that a **newer** binary has already migrated.
That happens after a downgrade, or with two installed versions sharing one data dir.

A test against goose v3.29.0 showed the current behaviour: a database at schema version 999
opened by a binary that knows only version 1 opens **without any error**. goose's `Up` sees no
pending migrations and returns. The older binary then runs its sqlc queries against a schema it
does not know.

Forces:

- The database is the user's only copy of their history (ADR 0012). Nothing may damage it
  silently.
- With forward-only migrations there is no in-tool way back to an older schema.
- The older binary cannot know what the newer migrations changed (new NOT NULL columns, new
  CHECK values, rebuilt tables), so it cannot judge whether its queries are still safe.

## Options considered

1. **Tolerate unknown versions** (goose's default). Pros: downgrades "just work" when the change
   was purely additive. Cons: when it was not, the failure appears later and far from the cause
   (a failing insert, or rows written without the newer schema's invariants), possibly after
   writing data the newer binary then has to deal with.
2. **Refuse to start** when the database version is ahead of the newest embedded migration. Pros:
   fails at startup with a clear message, before anything is written. Cons: a downgrade needs a
   manual step.
3. **Record a minimum compatible version** per migration so additive changes stay readable by
   older binaries. Pros: smooth downgrades. Cons: every migration needs a correct compatibility
   judgement, which is easy to get wrong and hard to test. Too much for a single-user local tool.

## Decision

Option 2, plus a backup that makes the manual step easy:

- `store.Open` reads the database version (`Provider.GetDBVersion`) and the newest embedded
  migration (`Provider.ListSources`) before migrating. If the database is ahead, it returns
  `*SchemaTooNewError` (matching `ErrSchemaTooNew` with `errors.Is`), naming the path and both
  versions. Nothing is written; the daemon exits with that message.
- Before an existing database is migrated, `store.Open` writes a consistent copy with
  `VACUUM INTO` to `<db>.pre-v<N>`, where N is the version being migrated to. The copy has the old
  schema and data, so the previous binary can open it. It is written to a fresh 0600 temp file and
  renamed into place, so a crash never leaves a half-written backup under the final name. A new
  (empty) database is not backed up. The daemon logs the versions and the backup path.
- Downgrading means: stop the daemon, replace `agent-office.db` with the matching
  `agent-office.db.pre-v<N>` (and remove its `-wal`/`-shm` files), start the older binary. Changes
  made after the upgrade are lost; this is documented, not automated.

## Consequences

- An older binary can never write to a newer schema. Tested with a database marked as version 999:
  the open fails and the file's bytes are unchanged.
- Each upgrade leaves one backup per target version next to the database. They are small at
  this data size; cleanup is a later concern (Phase 8 retention).
- Tests that need a "next" migration inject one through an `fs.FS`, so they do not depend on the
  real migrations.
- A crash between `VACUUM INTO` and the rename can leave a `*.tmp-*` file in the data dir. It is
  harmless and can be deleted.
