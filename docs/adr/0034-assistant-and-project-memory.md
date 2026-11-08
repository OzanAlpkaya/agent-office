# 0034. The assistant: briefings from daemon facts, project memory the user accepts

- **Status:** Accepted; what reaches a prompt and who may list memory amended by ADR 0037
- **Date:** 2026-11-08

## Context

Ozan (2026-11-08): an Assistant, long-running across goals, gives briefings (done, waiting on
me, blocked, cost), reminds of pending decisions, suggests what to do next, and curates project
memory (decisions, conventions, lessons) that other agents start with. No code, no assignments.
Its memory work connects to Phase 9 ("agents can read relevant project memory at task start;
memory is inspectable and editable by the user").

Forces:

- Numbers in a briefing must be right; a model summarizing raw tables gets counts and money wrong.
- Memory that every agent starts with is a **persistent prompt-injection channel**: one poisoned
  entry would steer every later session. The assistant reads agent-written text (questions in the
  inbox, task titles) and the repository.
- The assistant must not become a second lead.

## Options considered

**Where the facts come from**

1. **The assistant reads the board through general tools** (list tasks, list PRs, usage). Cons:
   many calls; arithmetic by the model.
2. **One `get_briefing` tool that returns the facts computed by the daemon**, which the model
   words and prioritizes.

**Who decides what becomes memory**

1. **The assistant writes memory directly.** Cons: the injection channel above, with no human in
   the loop.
2. **The assistant proposes; the user accepts, edits or deletes**; only accepted entries reach
   prompts. The user can also add entries directly.

**Where memory lives**

1. **A file in the repository** (`docs/memory.md`). Cons: agents can change it through merged
   PRs; the same reason packs and roles are not read from the repository (ADR 0023, 0029).
2. **The database** (`memory` table), shown and edited in the UI. Phase 9 can add export or
   per-task relevance without changing who decides.

## Decision

Option 2 in each group.

- **The assistant** is the `assistant` behaviour (ADR 0029): no task, read-only workspace on HEAD,
  one at a time, team tools `get_briefing`, `message_user`, `list_memory`, `propose_memory` and
  nothing else (no `send_message`, no tasks). **Briefing** in the UI (`POST
  /api/assistant/briefing`) starts it, whose first prompt asks for a briefing, or sends a running
  one "Please give me a new briefing." as the user's message (ADR 0030). It stays until stopped;
  the user asks it questions in its terminal or through the inbox.
- **`get_briefing`** returns, from the database: goals with sub-task counts by status, plan state
  and last QA result and the lead's state; what waits on the user (plans to approve, reported goals,
  unanswered questions and blocked notices, quoted and attributed, pending access requests, flagged
  commits, open PRs to merge, memory proposals); what is blocked (blocked tasks, sub-tasks waiting
  for dependencies, conflicts); running agents; estimated cost in total and for sessions started
  today (micro-USD, ADR 0026) and the goal budget.
- **Memory** (`memory` table): `decision | convention | lesson`, title (≤ 200), body (≤ 2 000),
  `proposed | accepted`, source (the proposing agent, or empty for the user). `propose_memory`
  (assistant only, at most 50 open proposals); `GET|POST /api/memory`,
  `PATCH|DELETE /api/memory/{id}`, `POST /api/memory/{id}/accept`; `memory` and `memory_deleted`
  updates; in the `/api/events` snapshot.
- **Every agent's first prompt** (and every relaunch's) ends with the accepted entries, newest
  first, within 6 000 characters, under "Project memory (notes the user approved for every agent;
  follow them unless your task says otherwise)". Proposals never appear in a prompt.

## Consequences

- A briefing's counts and costs are the daemon's; the model can still word them badly, never
  miscount them.
- A prompt-injected assistant can at most put proposals in front of the user and messages in the
  inbox. Planting an instruction in the team needs the user's click, and the UI shows who proposed
  each entry.
- Memory is per project and global to its agents; relevance per task, export to the repository and
  memory from other agents' sessions are Phase 9's.
- An assistant left running holds an agent slot (ADR 0029); it costs nothing while idle.
