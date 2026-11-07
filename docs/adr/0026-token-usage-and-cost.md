# 0026. Token usage and cost: metered on the API socket or read from transcripts, priced on read

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0016 (the API socket meters usage), ADR 0011 (`sessions` gains counters)

## Context

Phase 8 shows what agents cost, per agent, task, goal, session and model, as tokens and as an
estimated price from a configurable table (`docs/plans/phase-8.md`). Two kinds of agents exist:

- **Sandboxed agents** reach Anthropic only through their per-agent API socket (ADR 0016). Their
  transcripts live in a config dir owned by the sandbox's subordinate uid; Claude Code writes them
  0600, so the daemon cannot read them without a helper container (ADR 0014), and they are files an
  untrusted agent controls.
- **Host-mode agents** talk to Anthropic directly. Their transcript path arrives in every hook
  payload (`transcript_path`, Spike C).

Measured on a real session today (keys and numbers only): `assistant` records carry
`message.usage` and repeat it on every line of a multi-block message; a `cost-state` record holds
Claude Code's own total, which today's price table reproduced exactly, model by model; the
assistant records alone came to slightly less than `cost-state`, because some internal requests
(title generation and similar) are not written as assistant records. Streaming responses carry
usage in `message_start` and, cumulatively, in `message_delta` (Messages API docs).

## Options considered

**Source**

1. **Transcripts for everyone**, sandboxed ones read through a helper container. Pros: one source;
   matches the session files by definition. Cons: a container per read (seconds), so not live;
   the daemon parses files an untrusted agent can write; misses requests the transcript omits.
2. **The API socket for everyone.** Cons: host-mode agents do not use it.
3. **The API socket for sandboxed agents, the transcript for host-mode agents**, stored in one
   shape. Pros: live, complete for sandboxed agents, numbers read from data the daemon already
   forwards; budgets (ADR 0027) need the socket anyway. Cons: two readers; the sandboxed source is
   a superset of what the transcript records.

**Granularity**

1. **Running totals per session.** Cons: a re-read transcript double counts; no way to compare
   with a transcript message by message.
2. **One row per API message**, unique on session and message id, upserted. Pros: idempotent
   re-reads; `message_delta` updates the `message_start` row; per-message comparison with a
   transcript (by `message.id`) is exact.

**Cost**

1. **Stored with each row.** Cons: a price correction leaves history wrong.
2. **Computed when read** from the current table. Pros: history re-priced by editing one file.

**Prices**

1. **Fetched from Anthropic at run time.** Cons: network access and a page format to depend on.
2. **Built in, from the pricing page with its date, plus a user-owned override file.**

## Decision

Option 3, option 2, option 2 and option 2.

- **`internal/usage`** decodes only numeric fields and ids: from transcripts, `assistant` records
  (`message.id`, `message.model`, `message.usage`, `requestId`) and the last `cost-state`
  (`totalCostUSD`); from API responses, `message_start`/`message_delta` events or the JSON body.
  Unknown record types and fields are ignored, lines are size-capped, nothing else is kept.
- **Rows** (`usage_messages`, migration 0007): session, agent, task, source (`api` or
  `transcript`), message id, request id, model, speed, inference geo, and input, output, cache
  read, cache write 5 m and 1 h tokens. When a usage has no TTL breakdown, its cache writes count as
  5-minute writes. `sessions` gains `turns`, `tool_calls` and `cli_cost_usd` (Claude Code's own
  figure for host sessions, shown next to agent-office's as a cross-check).
- **Sandboxed:** the API proxy wraps each successful response body in a meter that reads exactly
  what the client reads (no buffering of streams, no byte changed); JSON bodies are parsed from a
  capped copy at the end. The proxy removes the client's `Accept-Encoding`, so Go's transport
  negotiates and decompresses and the meter sees plain text. A response the meter cannot parse is
  forwarded unchanged and logged once.
- **Host:** the transcript path from hooks is accepted only as `<session-id>.jsonl`, absolute, a
  regular file of the user opened with `O_NOFOLLOW`, and is never logged or stored. New complete
  lines of it and of `<session-id>/subagents/*.jsonl` are read after `Stop`, `SubagentStop` and
  `SessionEnd`, at most every 10 s during a turn, and once more after the process exits.
- **Prices:** `internal/usage/prices.yaml`, from platform.claude.com/docs/en/about-claude/pricing
  as of 2026-11-08: per model id, input, output, cache write 5 m, cache write 1 h and cache read per
  million tokens; Haiku 5.5's tier for prompts over 100 000 tokens (input, cache read and cache
  write tokens count); a fast-mode multiplier (2× for Opus 5.5, 5 and 4.8) and a US-inference
  multiplier (1.1× for 4.6 and later). Ids with a date suffix (`claude-haiku-4-5-20251001`) match
  their base id. `$XDG_CONFIG_HOME/agent-office/prices.yaml` (`AGENT_OFFICE_PRICES_FILE`) adds or
  replaces models; it must belong to the user and not be writable by others, is parsed strictly,
  and every problem is reported at once. A model without a price shows its tokens and "no price",
  never $0.
- Live `usage` updates per agent, throttled to one every 2 s; `GET /api/usage` aggregates by
  goal, task, agent, session and model.

## Consequences

- Sandboxed usage is live and includes requests the transcript leaves out; host usage lags a turn
  and equals the transcript's assistant records, which can be a little below Claude Code's own
  `cost-state` (shown next to it).
- Usage of the session files is matched per message id in tests and in the e2e run (sandboxed
  transcripts read there through `podman unshare`, test code only).
- Costs are estimates for the API price list. Subscription users are not billed per token; the UI
  says "estimated" and shows the table's date. Server-tool fees (web search) are not priced.
- A transcript format change shows up as missing host usage, not as a crash. The sandboxed path does
  not depend on Claude Code's files.
- Host usage of a session that was running when the daemon died is read up to that point only
  (the path is not persisted).
