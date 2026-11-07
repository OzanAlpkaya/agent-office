# Phase 8 plan: Observability & cost

- **Status:** In progress on `phase-8/observability-cost`. Written 2026-11-08 with the recommended
  option for every open question and executed straight away, as Ozan asked ("write the Phase 8
  plan using your recommended options, then execute it immediately without asking me anything").
  Every decision is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 8. Progress: [PROGRESS.md](../../PROGRESS.md).
  Phase order from here on (Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13.
- **Prerequisites:** none. `make e2e` at the end of the phase spends a few cents (haiku).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

The user sees what every agent costs while it works, per agent, task, goal (team run), session and
model, in tokens and as an estimated price from a price table they can change. Spend has a hard,
deterministic limit when they want one: a budget per session and per goal, enforced by the daemon
on the API socket, outside the model's control. Logs of one agent session can be followed from the
daemon through its hooks, team tool calls and API requests by one correlation id.

## What exists and what it means

- **Sandboxed agents** call Anthropic only through their per-agent API socket (ADR 0016), which
  forwards only `POST /v1/messages`. Every response passes the daemon. ADR 0016 already planned to
  read token usage there ("numbers only, never content"). Their transcripts live in the agent's
  config dir, which belongs to the sandbox's subordinate uid; Claude Code writes them 0600, so the
  daemon cannot read them without a helper container (ADR 0014).
- **Host-mode agents** talk to Anthropic directly, so the only source is their session transcript.
  Every hook payload carries `transcript_path` (Spike C); subagents write
  `<session>/subagents/agent-*.jsonl`.
- **Transcript format** (Spike C, rechecked today on a real 341-record session, keys and numbers
  only): `assistant` records carry `message.id`, `message.model` and `message.usage`
  (`input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens` with a
  `cache_creation.ephemeral_5m/1h_input_tokens` breakdown, `speed`, `inference_geo`, ...), repeated
  on every line of a multi-block message, so lines must be deduplicated by `message.id`. A
  `cost-state` record holds Claude Code's own `totalCostUSD` and per-model totals. Checked: the
  live price table (below) reproduces that session's `cost-state` to the cent, per model. But the
  `assistant` records alone came to slightly less than `cost-state` (internal requests such as
  title generation are not written as assistant records).
- **Streaming responses** (Messages API docs, fetched today): `message_start` carries the message
  id, model and initial `usage`; `message_delta` carries `usage` whose counts are **cumulative**,
  and newer responses repeat the input and cache fields there.
- **Prices** (platform.claude.com pricing page, fetched 2026-11-08): per model base input, 5-minute
  and 1-hour cache writes, cache reads, output. Haiku 5.5 has a higher tier for prompts over
  100 000 tokens (cache reads and writes count); fast mode (Opus 5.5, 5, 4.8) costs 2× on every
  category; US-only inference (`inference_geo: "us"`, 4.6 and later) costs 1.1×.
- **Logs:** `slog` text to stderr; most agent lines carry `agent_id`, a few `session_id`.
- **Retention:** events grow without bound (ADR 0011) and migration backups pile up (ADR 0013);
  both were left for Phase 8.

## Out of scope

- Billing the user's real account (subscription or API invoices); costs here are estimates.
- Token budgets for host-mode agents (they reach Anthropic directly; the daemon cannot refuse a
  request). Their usage is still shown.
- Prometheus/OpenTelemetry exporters, log shipping, a log viewer in the UI.
- Charts beyond simple inline bars; historical trend analysis.
- Resuming sessions (Phase 9): only the "Resume this session" line is explained (backlog item).

## Approach

1. **Package `internal/usage`** (pure, no engine dependency):
   - `Usage` (input, output, cache read, cache write 5m and 1h, model, speed, geo) and `Message`
     (message id, request id, usage).
   - `TranscriptReader`: reads a transcript JSONL incrementally (offset per file), complete lines
     only, decodes only the fields it needs from `assistant` and `cost-state` records, ignores
     unknown types and fields, caps line size, and returns messages deduplicated by id (the last
     line of a message wins). Never keeps or returns content.
   - `Meter`: wraps an API response body as it streams through, without changing or delaying a
     byte. For `text/event-stream` it scans `data:` lines of `message_start` and `message_delta`
     (cumulative: a field present in a delta replaces the earlier value); for `application/json`
     it parses `id`, `model`, `usage` from a size-capped copy at the end. Numbers only.
   - `Prices`: a price table with an `as_of` date and source, entries per model id (date suffixes
     like `-20251001` match the base id), optional long-context tier, fast-mode and US-geo
     multipliers. Built-in table embedded as YAML from the pricing page (as of 2026-11-08); a user
     file (`$XDG_CONFIG_HOME/agent-office/prices.yaml`, `AGENT_OFFICE_PRICES_FILE`) overrides or
     adds models; strict YAML, user-owned, every problem reported at once (pack loader style).
     `Cost(Usage) (usd float64, known bool)`: an unknown model has no price, never zero.
2. **Storage (migration 0007):**
   - `usage_messages`: one row per API message, unique on `(session_id, message_id)`, with
     agent, task, source (`api` / `transcript`), request id, model, speed, geo and the five token
     counts. Upserts make re-reading a transcript idempotent and let `message_delta` update the
     row written at `message_start`.
   - `sessions` gains `turns`, `tool_calls` (counted from the main agent's hooks) and
     `cli_cost_usd` (Claude Code's own `cost-state` figure, host mode).
   - Cost is computed when read, not stored, so changing the price table re-prices history.
3. **Collection in the engine:**
   - Sandboxed: the API proxy gets a `Meter` per response and an `OnUsage` callback; the engine
     upserts the row with the agent's current session and task. The proxy drops the client's
     `Accept-Encoding` so the Go transport decompresses and the meter sees plain text.
   - Host: the hook handler remembers the session's `transcript_path` (validated: absolute,
     `<session-id>.jsonl`, a regular file owned by the user, opened without following symlinks;
     never logged or stored). After `Stop`, `SubagentStop`, `SessionEnd`, and at most every 10 s
     during a turn, a reader goroutine picks up new lines of the transcript and its subagent files;
     a final read runs after the process exits.
   - Live: a throttled `usage` update per agent (at most every 2 s) carries the session's totals and
     cost, so cards and the dashboard follow without polling.
4. **Budgets (sandboxed agents, deterministic):** `--budget-session-usd` (per agent session) and
   `--budget-goal-usd` (everything spent under one goal: lead, workers of its sub-tasks, reviewers
   of their PRs), both default 0 = off. Before forwarding a request, the API proxy asks the engine;
   a spent budget gets a 403 in the API's error shape ("agent-office: this session's budget of
   $X is spent"), which Claude Code shows and does not retry, plus a sandbox event in the UI. With
   a budget set, a request whose model has no price is refused (fail closed); at startup, a budget
   without a price for `--model` is a startup error. Requests already in flight finish, so a budget
   can be exceeded by at most the requests running at that moment (documented).
5. **API:** `GET /api/usage` returns totals and rows by goal, task, agent, session and model, with
   tokens, cost, turns, tool calls, duration and budget status, plus the price table's `as_of`,
   source and unpriced models. `GET /api/status` lists the budgets and price table source.
6. **UI:** a **Usage** tab next to Board and Office: totals at the top (estimated cost, tokens by
   kind, sessions), then tables by goal → task, by agent, by session and by model, each row with an
   inline bar of its share of the cost; "no price" shown as such, never as $0; host sessions show
   Claude Code's own figure next to agent-office's. Agent cards and task cards get a small
   "tokens · $" line. Exited agents' terminals get a note under the terminal: the "Resume this
   session with claude --resume …" line printed by Claude Code cannot be used from agent-office yet
   (the session belongs to the agent's sandbox and config); resuming comes in Phase 9.
7. **Correlation and logs:** the **session id** is the correlation id: it names the CLI process,
   arrives in every hook payload, names the transcript, and keys usage rows. Every log line about
   an agent's run comes from one logger with `agent_id`, `session_id` and `task_id`: launch, hooks
   (event, state), team MCP calls (tool, outcome, duration; never arguments), API proxy requests
   (status, duration, Anthropic's `request-id` header, which is also the transcript's `requestId`),
   egress decisions, message delivery, imports and exit. `--log-format text|json`
   (`AGENT_OFFICE_LOG_FORMAT`, default text). UI HTTP requests get a `request_id` in their log
   line and an `X-Request-Id` response header.
8. **Retention:** events older than `--event-retention-days` (default 30, 0 keeps everything) are
   deleted at startup and daily; the newest 3 migration backups are kept. Usage rows and sessions
   are kept (small, and they are the cost history).

## Changes

- `internal/usage/` (new): `usage.go`, `transcript.go`, `meter.go`, `prices.go`, `prices.yaml`,
  tests and `testdata/` (redacted transcript and matching SSE/JSON fixtures).
- `internal/sandbox/apiproxy.go`: meter, budget check, `request-id` and encoding handling.
- `internal/agent`: `TranscriptPath` on `Event` (path only).
- `internal/engine`: usage collection, transcript reader loop, budgets, per-session logger,
  session counters, retention, `Usage` query; `internal/mcp`: call logging.
- `internal/store`: migration `0007_usage.sql`, `queries.sql`, `make generate`.
- `internal/api`: `GET /api/usage`, request ids; `cmd/agent-office`: flags, log format, price
  file, budgets.
- `web/src`: `api.ts` types, `Usage.tsx` (tab), cards, terminal note, styles; Vitest for the
  aggregation helpers; Playwright for the tab against the mocked daemon.
- `test/e2e`: host run compares agent-office's totals with the transcript and Claude Code's
  `cost-state`; sandbox run compares metered rows per message id with the transcript read through
  `podman unshare`.
- Docs: ADRs 0026 (usage and cost), 0027 (budgets), 0028 (correlation ids, log format, retention);
  architecture, threat model, `.env.example`, `docs/plan.md`, `PROGRESS.md`, this file.

## Data / API changes

- Migration 0007: `usage_messages` (new), `sessions.turns`, `sessions.tool_calls`,
  `sessions.cli_cost_usd`. Forward-only, additive.
- `GET /api/usage` (new); `Update` type `usage` (new); `Status.usage` (prices, budgets).
- New flags/env: `--prices` / `AGENT_OFFICE_PRICES_FILE`, `--budget-session-usd` /
  `AGENT_OFFICE_BUDGET_SESSION_USD`, `--budget-goal-usd` / `AGENT_OFFICE_BUDGET_GOAL_USD`,
  `--event-retention-days` / `AGENT_OFFICE_EVENT_RETENTION_DAYS`, `--log-format` /
  `AGENT_OFFICE_LOG_FORMAT`.

## Risks & edge cases

- **Undocumented transcript format.** The parser reads numbers only, ignores what it does not
  know, and is tested against fixtures with today's shapes. A change shows as missing usage, not a
  crash; the sandbox path does not depend on it.
- **Content in transcripts and API streams.** Only numeric fields and ids are decoded; nothing
  else is kept, logged or stored. A test puts sensitive markers in the fixtures and checks they
  reach no row, log or update.
- **Meter must not disturb the stream.** It reads what the client reads, never buffers SSE, and a
  parse error only stops metering that response (logged once). Tested with a slow streaming
  upstream: bytes reach the client before the stream ends.
- **Transcript path from a hook payload.** In host mode the agent runs as the user anyway; still,
  the path is validated, opened with `O_NOFOLLOW`, size-capped per read and never followed outside
  the session's own files.
- **Estimates differ from billing.** Price table may lag; subscription users are not billed per
  token. The UI says "estimated" and shows the table's date.
- **Budget overshoot** by in-flight requests; documented. **Unknown models** with a budget fail
  closed.
- **Event retention deletes history.** Only the reduced events table, after 30 days by default;
  usage and session counters survive it.

## Test plan

- Unit: transcript reader (dedup, unknown records, partial last line, incremental reads, subagent
  files, oversize lines), meter (SSE cumulative deltas, JSON, gzip off, broken streams, no content),
  prices (matching, long-context tier, fast, geo, unknown model, user file validation), cost of the
  fixture equals its `cost-state`.
- **Fixture match (DoD):** the same messages as a transcript and as API responses give identical
  per-message usage and totals; the engine's usage for a fake agent equals the fixture's.
- Engine: a fake host agent's transcript becomes rows and a live update; a sandbox-less API proxy
  test with a fake upstream records usage and enforces both budgets; turns and tool calls counted;
  retention.
- Logs (DoD): a fake agent session (hooks, an MCP call, an API request) logged as JSON; every
  record about it carries the same `session_id`, and none contains the fixtures' sensitive
  markers.
- API and UI: `/api/usage` shape; Vitest for aggregation; Playwright for the Usage tab and cards.
- `make check` on every commit; `make ui-test`, `make sandbox-test` and `make e2e` at the end.

## Steps (each one or more commits with `make check` green)

1. Branch, this plan, phase order in `PROGRESS.md`.
2. ADRs 0026–0028.
3. `internal/usage` with fixtures and tests.
4. Migration 0007, queries, store tests.
5. Engine: API metering (sandbox), transcript reading (host), counters, live updates.
6. Budgets.
7. Correlation ids, JSON logs, MCP call logs, request ids; retention.
8. API and CLI flags; `.env.example`.
9. UI: Usage tab, cards, resume note; tests.
10. e2e checks; live check; docs; `PROGRESS.md`.

## Open questions (answered with the recommended option)

1. **Where does usage come from?** The API socket for sandboxed agents and the transcript for
   host-mode agents, stored the same way (one row per API message). Reading sandboxed transcripts
   would need a helper container per read (slow, and it puts the daemon back into reading files an
   untrusted agent controls); the API socket sees every request, including ones the transcript
   omits, and is where budgets must live anyway. Host agents have no proxy, so the transcript is
   the only source there.
2. **Store cost or compute it?** Store token counts per message; compute cost when read, from the
   current price table. A price fix then re-prices history, and nothing stale is persisted.
3. **Where do prices come from?** A built-in table from the pricing page with its date, plus a
   user-owned override file merged per model. Fetching prices at run time would add network access
   and a dependency on a page format.
4. **Budgets in this phase?** Yes, minimal and off by default: per session and per goal, estimated
   USD, sandboxed agents only, refused at the API socket. The threat model and ADRs 0016–0018 and
   0022 assigned money limits to Phase 8; the roadmap DoD does not list them, so they stay opt-in.
   Default off because a subscription user's "cost" is notional and a surprise refusal mid-task is
   worse than no limit; turning them on is one flag.
5. **Which correlation id?** The session id (with agent and task ids on every line). It already
   exists everywhere an agent's run is visible: hooks, transcripts, the UI. A new trace id would
   have to be threaded through Claude Code, which we do not control.
6. **The "Resume this session" line: hide or explain?** Explain, below the terminal of exited
   agents. Filtering it out of the PTY stream means matching TUI output, which ADR 0008 rejected as
   fragile.
7. **Event retention?** 30 days, configurable, 0 = keep; backups: the newest 3.
8. **Dashboard style?** Tables with inline bars in the existing UI, no chart library: the data is
   a few dozen rows, and exact numbers matter more than shapes here.

## Changes during the phase

(appended during the phase)
