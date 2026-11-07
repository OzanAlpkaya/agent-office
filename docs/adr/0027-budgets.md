# 0027. Budgets: estimated-cost limits per session and per goal, refused on the API socket

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0016 (the API socket can refuse a request when a budget is spent)

## Context

The threat model left "an agent spends inference without limit" and "agents keep each other busy"
open until Phase 8: counts bound them (sessions, sub-tasks, messages, review rounds; ADR 0017,
0018, 0022) but not money. CLAUDE.md asks that money stays in deterministic code. Sandboxed agents
reach Anthropic only through their API socket (ADR 0016), and ADR 0026 knows each session's
estimated cost as it accrues.

## Options considered

**Unit**

1. **Tokens.** Pros: no price table needed. Cons: a token of Opus and a token of Haiku differ
   fortyfold; users think in money.
2. **Estimated USD** from ADR 0026's price table. Cons: only as good as the table.

**Scope**

1. **Per session** only. Cons: a lead can start many workers, each within its limit.
2. **Per goal** only. Cons: one runaway worker can spend the whole goal.
3. **Both**, each optional.

**Enforcement**

1. **Tell the model** (prompt, or Anthropic's task budgets). Cons: advisory; a prompt-injected
   agent ignores it.
2. **Stop the agent** when over. Cons: kills work in the middle of a tool call; no effect until the
   next reading.
3. **Refuse the next API request** on the socket. Pros: deterministic and immediate; the session
   stays up, so its work can be saved.

**Default**

1. **A limit by default.** Cons: a subscription user's "cost" is notional; a surprise refusal in the
   middle of a task is worse than no limit for most runs.
2. **Off by default**, one flag to turn on.

## Decision

Option 2, option 3, option 3 and option 2.

- `--budget-session-usd` (`AGENT_OFFICE_BUDGET_SESSION_USD`) limits one agent session;
  `--budget-goal-usd` (`AGENT_OFFICE_BUDGET_GOAL_USD`) limits everything recorded under one goal:
  the goal's own agents (its lead), the workers of its sub-tasks and the reviewers of their pull
  requests. 0 (default) is off. Decimal dollars on the command line, negative values refused at
  startup; inside the daemon they are micro-USD integers, compared with the nano-USD spend.
- Before forwarding a request, the API proxy asks the engine. A spent budget gets
  `403 {"type":"error","error":{"type":"permission_error","message":"agent-office: ... budget of
  $X is spent ..."}}`: Claude Code shows the message and does not retry a 403. The refusal is a
  sandbox event in the UI (`api_budget`, "Budget spent"), logged with the session id.
- **Fail closed:** with any budget set, a request from a session whose usage includes a model
  without a price is refused (the spend cannot be known), and the daemon refuses to start when
  `--model` is a full model id (`claude-…`) without a price. Aliases such as `haiku` resolve inside
  Claude Code, so they are checked per request instead.
- A relaunched worker (ADR 0022) starts a new session with a fresh session budget; the goal budget
  still counts everything before.
- Host-mode agents are not limited (they have no API socket); the UI and `GET /api/status` say so.

## Consequences

- A prompt-injected or looping team stops spending at a limit the user chose, without relying on
  the model.
- Requests already in flight finish, so a budget can be exceeded by the cost of the requests running
  when it is reached (one per agent in practice, plus subagents). Documented in the UI.
- A spent budget leaves the agent idle at its prompt; the user can save its work, stop it or
  restart the daemon with a higher limit. Raising a limit at run time is a later addition.
- The budget is only as accurate as the price table; it is an estimate, not a billing guarantee.
