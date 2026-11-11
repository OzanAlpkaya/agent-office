# 0017. Team roles: a lead per goal, workers per sub-task, authority from the socket

- **Status:** Accepted; what marking a goal done ends amended by ADR 0054 (Phase 17): every agent of the goal
  and its sub-tasks, and its sub-tasks become done
- **Date:** 2026-11-07

## Context

Phase 4 adds coordination: a lead agent breaks a goal into tasks and delegates them, workers do
the tasks and report back. Agents call the daemon through the team MCP server (ADR 0006). Agents
are untrusted (threat model): they read attacker-controllable content and may be prompt-injected.
Every capability the team tools hand out is therefore a capability a hostile agent could use, and
some of them spend money (starting a worker starts a Claude Code session).

Forces:

- The model is good at planning and wording; it must not decide who may do what (CLAUDE.md: money
  and permissions stay in deterministic code).
- The engine already has a safe way to start an agent on a task (Start: slots, worktree or
  sandboxed clone, task to `in_progress`); assignment should reuse it, not add a second path.
- Phase 5 turns finished work into pull requests per branch, so one branch per task is the unit to
  keep.

## Options considered

**What `assign_task` means**

1. **Start a new worker on the sub-task.** Pros: reuses Start; one agent, one task, one branch;
   a fresh context per task. Cons: a session start per task (seconds, some tokens).
2. **Hand the task to a running idle worker** (a pool). Pros: no start cost. Cons: one branch would
   carry several tasks, or the worker would have to switch branches (which the task prompt forbids);
   earlier context leaks into the next task; harder to attribute work and cost.

**How much the lead may do without a human**

1. **Within deterministic limits:** agent slots (`--max-agents`, the lead's own slot included) and a
   sub-task cap per goal. Pros: the lead can actually lead; spend is bounded by counts. Cons: a
   misbehaving lead can use up its limits.
2. **Every assignment approved by a human.** Pros: maximal control. Cons: the lead becomes a
   suggestion engine; the human approves the same plan twice (once when starting the goal).

**Where identity and authority come from**

1. **The socket a call arrives on** (per-agent MCP socket, ADR 0006), resolved to the agent's
   recorded role and task. Pros: an agent cannot claim to be another; no tokens.
2. **Tool arguments** (`from`, `as_role`). Rejected: trivially forged.

## Decision

Option 1 in each group.

- **Roles:** `agents.role` is `lead` or `worker` (migration 0004; existing agents are workers).
  The user starts a lead on a **top-level** task, the **goal**. At most one lead runs at a time,
  so "the lead" is unambiguous. Workers are started by the user (as before) or by `assign_task`.
- **Tasks:** `tasks.parent_task_id` links a sub-task to its goal; `tasks.created_by` is empty for
  the user or the creating agent's id. A goal's sub-tree is one level deep: sub-tasks cannot have
  sub-tasks, and a sub-task cannot be a goal.
- **Authority of a lead** (the running lead whose task is goal G):
  - `create_task`: creates a sub-task of G in `todo` (at most 20 per goal).
  - `assign_task`: starts a worker on a sub-task of G that is in `todo` or `blocked` and has no
    running agent; refused when all slots are busy.
  - `send_message`: to running workers whose task is a sub-task of G.
  - `list_tasks`: G and its sub-tasks, with their agents.
  - `report_done`: moves G to `review` once no sub-task is `in_progress`; the lead then exits.
- **Authority of a worker:**
  - `send_message`: only to the running lead of its task's goal (`to: "lead"`).
  - `list_tasks`: its own task and its lead.
  - `report_done`: only for its own task, only with committed work (sandbox: the branch is
    imported first and must have commits; host: the worktree must be clean and the branch ahead of
    its base). The task moves to `review`, the lead gets the summary and branch as a message, and
    the worker exits once its turn ends, freeing its slot.
  - `create_task` and `assign_task` are refused.
- **Every agent** may `request_access` (ADR 0019).
- `review → done` stays a human decision; nothing in Phase 4 moves a task to `done`.
- All checks live in the engine, run on every call, and return typed errors (`ErrForbidden`,
  `ErrInvalid`, `ErrTooManyAgents`), which the MCP layer turns into tool errors with a short
  reason. Inputs are validated twice: by the SDK against the tool's JSON schema, then by the engine
  (lengths, control characters, ids belonging to the project).

## Consequences

- A prompt-injected lead can at most create 20 sub-tasks and keep `--max-agents` workers busy on
  them; a prompt-injected worker can at most message its lead (rate-limited, ADR 0018) and ask for
  access. Neither can start agents outside the goal, touch other goals, or grant itself anything.
- Starting a goal is the human's approval of the whole plan's budget, bounded by counts. Token
  budgets (Phase 8) can tighten this without changing the model.
- Workers that reported are gone, so follow-up work is a new sub-task, which matches one branch
  per task for Phase 5.
- **Identity by socket holds in sandbox mode only.** A host-mode agent runs as the user's uid, so it
  could connect to another agent's socket (mode 0600, same user) or call the loopback API, as any
  local process of the user can (threat model). Host mode is the explicit, unsandboxed option.
- If an agent is interrupted (Esc) during its `report_done` turn, no Stop arrives (ADR 0008); the
  interrupt ends it instead, so it never keeps its slot.
- One lead at a time is a simplification; several goals in parallel need lead-scoped addressing,
  which the schema already supports (authority is derived from the goal, not from a global flag).

## Amendment (2026-11-11, Phase 14): per project

"At most one lead runs at a time" holds per project (ADR 0046): each project's engine enforces it,
together with one architect, QA and assistant, for its own goals; two projects can each have a
Tech Lead at once. Authority still comes from the socket: an agent's sockets belong to its
project's engine, so every rule above applies within the agent's project, and ids of another
project are not found (`TestAgentsCannotReachAnotherProject`).
