# 0028. Correlation ids, log format and retention

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0011 (event retention), ADR 0013 (backup cleanup)

## Context

The Phase 8 DoD asks that logs can be followed across daemon, agent and hook events through a
correlation id. The daemon logs with `slog` text to stderr; agent lines mostly carry `agent_id`,
some `session_id`, MCP calls are not logged, API proxy lines carry neither the session nor
Anthropic's request id. Separately, ADR 0011 left the unbounded `events` table and ADR 0013 the
growing migration backups to Phase 8.

## Options considered

**Correlation id**

1. **A new trace id per run**, passed to the agent. Cons: Claude Code would have to carry it into
   hooks and requests, which we do not control.
2. **The agent session id** (the CLI's `--session-id`). Pros: already in every hook payload, the
   transcript's file name, the usage rows (ADR 0026) and the UI; one per CLI process.
3. **The agent id.** Cons: spans relaunches (ADR 0022), so two sessions' lines mix.

**Format**

1. **Text only.** Cons: hard to filter by field.
2. **Text by default, JSON on request.**

## Decision

Option 2 and option 2.

- Every log line about an agent's run comes from one per-session logger carrying `agent_id`,
  `session_id` and `task_id` (when it has a task): start and exit, hook events, team MCP calls
  (tool name, outcome, duration; never arguments or results), API proxy requests (method, path,
  status, duration and Anthropic's `request-id`, which the transcript records as `requestId`),
  egress decisions, message delivery and imports. A relaunch gets a new logger with the new session.
- `--log-format text|json` (`AGENT_OFFICE_LOG_FORMAT`, default `text`). Following one session:
  `agent-office serve --log-format json 2>&1 | jq 'select(.session_id == "<id>")'`.
- UI HTTP requests get a random `request_id`, logged with method, path, status and duration, and
  returned as `X-Request-Id`.
- **Retention:** reduced events older than `--event-retention-days` (default 30;
  `AGENT_OFFICE_EVENT_RETENTION_DAYS`; 0 keeps everything) are deleted at startup and once a day.
  Usage rows and session counters are kept: they are small and are the cost history. Of the
  `agent-office.db.pre-v<N>` backups, the newest 3 are kept after a migration.

## Consequences

- One `jq` filter shows a session's whole story; the request id links a daemon line to the
  transcript and to Anthropic support.
- Nothing new is logged that could hold content: MCP arguments, hook payloads and API bodies stay
  out of logs (tested with sensitive markers).
- Older event history (the activity list) disappears after 30 days by default; counts per session
  survive in `sessions`.
- Downgrading past more than three schema versions needs a backup taken by hand.
