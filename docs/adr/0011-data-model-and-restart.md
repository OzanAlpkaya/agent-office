# 0011. Data model and restart semantics

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Phase 2 persists projects, tasks, agents, sessions, worktrees and reduced events in SQLite
(ADR 0004). Agent processes live in the daemon's PTYs and die with it (ADR 0002). The UI must still
show a truthful picture after a restart.

## Decision

**Tables** (goose migrations, sqlc queries; timestamps are UTC):

| Table | Purpose |
|---|---|
| `projects` | One row per repository (top-level path, slug). Phase 2 registers the `--repo` repository. |
| `tasks` | Title (1–200 chars), description (≤ 20 000), status `todo / in_progress / review / done / blocked`. |
| `agents` | One row per agent: project, optional task, model, settings mode, last state/activity, exit info, `removed_at`. |
| `sessions` | One row per CLI session (the `--session-id` UUID) of an agent. |
| `worktrees` | Path, branch, base commit and status `active / kept / removed` per agent. |
| `events` | Reduced hook events only (kind, hook, tool, detail, resulting state); never payload content. |

A task can have several agents over time (retries). The task's agents are the agents whose
`task_id` points to it; there is no back-reference from tasks to agents.

**Automatic status change:** starting an agent for a task sets the task to `in_progress`. All
other task status changes are made by the user.

**Restart:** at startup, every agent not yet `exited` is marked `exited` with reason
`daemon_restart`. Its terminal is gone (there is no scrollback to replay), and its worktree and
branch stay as they were. Tasks are untouched. Resuming sessions (`claude --resume`) is Phase 9.

**Limits:** at most `--max-agents` (default 4) agents run at the same time.

## Consequences

- The schema is the source of truth for state that outlives a process. Live-only data (PTY,
  scrollback, socket) stays in memory.
- Event rows grow without bound for now; retention is a Phase 8 topic.
