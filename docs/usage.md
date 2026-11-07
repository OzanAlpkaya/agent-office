# Usage, cost, budgets and logs

Phase 8 (ADRs 0026–0028). What agent-office measures, how it prices it, how to limit spend, and how
to follow one agent session through the logs.

## What is measured

Every API message of every agent session: input, output, cache read and cache write tokens (5-minute
and 1-hour), the model, fast mode and the inference region. Nothing else: no prompts, no tool input
or output, no assistant text.

| Agent | Source | When |
|---|---|---|
| Sandboxed (`--sandbox podman`) | The agent's API socket: the daemon reads the `usage` numbers of each response as it streams through | Live, per request |
| Host mode | The session transcript (`~/.claude/projects/…/<session>.jsonl`) and its subagent files | After each turn, at most every 10 s during one, and when the session ends |

Host-mode sessions also show Claude Code's own cost figure (its `cost-state` record) next to
agent-office's. They usually agree; Claude Code's can be a little higher because some of its internal
requests are not written to the transcript.

The **Usage** tab shows totals and tables by goal, task, agent, session and model. Agent and task
cards show their tokens and estimated cost; a goal also shows its team's (the lead, its workers and
their reviewers). `GET /api/usage` returns the same data.

## Prices

Costs are **estimates** from the API price list, not a bill: a Claude subscription is not charged per
token. The built-in table (`internal/usage/prices.yaml`) comes from
<https://platform.claude.com/docs/en/about-claude/pricing> and shows its date in the UI. It includes
Haiku 5.5's higher rates for prompts over 100 000 tokens, fast mode (2× on Opus 5.5, 5 and 4.8) and
US-only inference (1.1× on 4.6 and later). Server-tool fees such as web search are not priced.

To correct or add prices, create `~/.config/agent-office/prices.yaml` (or point
`AGENT_OFFICE_PRICES_FILE` / `--prices` at another file). Its models replace or add to the built-in
ones; everything else stays. Prices are USD per million tokens with at most three decimals; all five
rates are required:

```yaml
as_of: "2026-12-01"          # optional, shown in the UI
source: my negotiated rates  # optional
models:
  claude-haiku-5-5:
    input: 0.10
    output: 0.50
    cache_read: 0.01
    cache_write_5m: 0.125
    cache_write_1h: 0.20
    geo_us: 1.1                       # optional multiplier for inference_geo "us"
    long_context:                     # optional: other rates above a prompt length
      {above: 100000, input: 0.50, output: 2.50, cache_read: 0.05, cache_write_5m: 0.625, cache_write_1h: 1}
  my-model:                           # fast: optional multiplier for fast mode
    {input: 1, output: 2, cache_read: 0.1, cache_write_5m: 1.25, cache_write_1h: 2, fast: 2}
```

The file must belong to you and not be writable by others (like the token files). Every problem is
reported at once at startup. Costs are computed when shown, so a corrected price re-prices the whole
history. A model without a price shows its tokens and "no price", never $0.

Money is integer throughout: prices are held as nano-USD per token, costs as nano-USD and every API
field as micro-USD (`cost_micro_usd`).

## Budgets

Off by default. With them on, the API socket of a **sandboxed** agent refuses further requests once
the limit is reached:

| Flag | Environment | Limits |
|---|---|---|
| `--budget-session-usd 2` | `AGENT_OFFICE_BUDGET_SESSION_USD` | one agent session (a relaunched worker gets a new one) |
| `--budget-goal-usd 10` | `AGENT_OFFICE_BUDGET_GOAL_USD` | everything under one goal: its lead, the workers of its sub-tasks, their reviewers |

A refused request gets an API error that Claude Code shows in the agent's terminal ("agent-office:
this session's budget of $2.00 is spent …") and does not retry; the UI lists it as a "Budget spent"
event. The agent stays at its prompt, so you can save its work or stop it. Requests already running
when the limit is reached still finish, so the spend can end slightly above the limit.

With a budget set, a request from a session that used a model without a price is refused, since its
spend cannot be known; `--model` with a full model id that has no price stops the daemon at startup.
Host-mode agents talk to Anthropic directly and are not limited; the UI says so.

## Following one session in the logs

Every log line about an agent's run carries `agent_id`, `session_id` and (when it has one) `task_id`:
its start and exit, hook events, team tool calls (tool, outcome, duration), API requests (status,
duration and Anthropic's `request-id`, which the transcript records as `requestId`), egress
decisions, message delivery and imports. The session id is also the transcript's file name and is
shown in the Usage tab.

```sh
agent-office serve --log-format json --log-level debug ... 2>&1 \
  | jq 'select(.session_id == "62c42117-6ea2-4779-bd69-bfc1f00c699d")'
```

Hook events and API requests are logged at debug level. UI requests are logged at debug level with a
`request_id`, also returned as the `X-Request-Id` header.

## Retention

Agent activity events (the reduced hook events behind the activity history) older than 30 days are
deleted at startup and once a day (`--event-retention-days`, 0 keeps them). Token usage and per-session
counters are kept. Of the database backups taken before migrations (`agent-office.db.pre-v<N>`), the
newest 3 are kept.
