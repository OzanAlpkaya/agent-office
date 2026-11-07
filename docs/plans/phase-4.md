# Phase 4 plan: Team MCP server + lead agent

- **Status:** Written 2026-11-07 with the recommended option for every open question and executed
  straight away, as Ozan asked ("write the plan using your recommended options, then execute it
  without waiting for approval"). Every decision is listed in the end-of-phase summary for review;
  nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 4. Progress: [PROGRESS.md](../../PROGRESS.md).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

Agents coordinate through a team MCP server inside the daemon. A **lead** agent gets a goal (a
task), breaks it into sub-tasks and assigns them; each assignment starts a **worker** on that
sub-task. Workers report back with `report_done`; messages between lead and workers are delivered
at the recipient's next Stop hook, or typed into its terminal when it is idle, exactly once.
Agents can ask for more network access with `request_access`, which only ever creates a request
that a human approves or denies in the UI.

The model decides *what* to plan and say. Everything that changes state or spends resources
(who may create or assign tasks, how many, which agent receives a message, what an approval
grants) is decided by deterministic Go code in the engine, from the identity of the socket a call
arrives on, never from tool arguments.

## Out of scope

- Merging workers' branches, pushing and PRs (Phase 5). A finished goal leaves one imported branch
  per sub-task in the main repository.
- Budgets and token accounting (Phase 8). Phase 4 bounds cost with deterministic counts: agent
  slots (`--max-agents`), sub-tasks per goal, messages per agent and minute.
- Access kinds other than network domains (secrets and paths need the Phase 6 stack packs).
- Worker-to-worker messages, several leads at once, user-to-agent chat, resuming sessions
  (Phase 9), Codex.

## Approach

1. **MCP transport (confirms ADR 0006).** go-sdk v1.8.0's `mcp.IOTransport` serves a session over
   any reader/writer, so the daemon serves MCP on a per-agent unix socket (`mcp.sock`, mode 0600,
   next to the hook socket; in sandbox mode bind-mounted like the others). `agent-office mcp` is a
   stdio ↔ socket bridge that Claude Code starts from a daemon-generated `--mcp-config` file. One
   `mcp.Server` per agent, its tool handlers closed over that agent's id. A separate socket from
   hooks, because MCP is a long-lived stream while the hook protocol is one request per
   connection.
2. **Roles (ADR 0017).** `agents.role` is `lead` or `worker`. A lead is started by the user on a
   top-level task (the goal); at most one lead runs at a time. Its authority is the goal's
   sub-tree: it may create sub-tasks of its goal, assign them, message their workers and list
   them. A worker may message its lead, report its own task done and request access.
   `assign_task` starts a new worker for the sub-task (the existing Start path, so sandbox, slots
   and worktrees are unchanged). The lead never gets a process-level ability it did not have.
3. **Tools** (`internal/mcp`, typed inputs validated by the SDK's JSON schema, then again by the
   engine): `create_task`, `assign_task`, `list_tasks` (added: the lead needs to see progress
   without guessing), `send_message`, `report_done`, `request_access`. Tool errors come back as
   MCP tool errors (`isError`) with a short reason, so the model can correct itself.
4. **Messages (ADR 0018).** A `messages` table. Delivery claims a recipient's undelivered messages
   with one `UPDATE … RETURNING`, so a message is handed out at most once, and is retried until it
   is:
   - on the recipient's **Stop** hook (not a subagent's, not `StopFailure`, not when
     `stop_hook_active`): reply `{"decision":"block","reason":…}`; the agent continues and its
     state stays `working`;
   - when the recipient is **idle** (after a finished turn, an interrupt or `idle_prompt`), after a
     short settle delay and only if the user has not typed in the last seconds: typed into its
     terminal through the engine's input path, as one sanitized line (no control characters, so no
     escape sequence or lone Esc / Ctrl-C from another agent ever reaches a PTY), then Enter.
   Messages are framed as coming from another agent: information, not instructions that override
   the recipient's task (prompt injection between agents is in the threat model).
5. **`report_done`.** A worker's report checks deterministically that its work is committed: in
   sandbox mode it imports the branch (`Engine.Import`, trigger `report_done`) and refuses if there
   are no commits or uncommitted changes; in host mode it checks the worktree and the branch. Then
   the task moves to `review`, the lead gets a message with the summary and the branch, and the
   worker exits once its turn ends, freeing its slot. A lead's report (all sub-tasks out of
   `in_progress`) moves the goal to `review` and ends the lead. `done` stays a human decision.
6. **Access requests (ADR 0019).** `request_access(kind="domain", value, reason)` creates a
   pending row and an update for the UI; nothing else. The UI's approval queue approves or denies.
   Approval adds the host to *that running agent's* egress allowlist (a per-agent grant set in its
   egress proxy); it never changes other agents or the defaults, and ends with the agent. The agent
   gets a message with the decision. Requests of exited agents expire; host-mode agents have no
   egress restriction, so their requests are refused.
7. **Prompts.** The lead's first prompt names the goal, the tools, the limits, and says not to edit
   code and to end its turn after assigning (agent-office delivers worker reports as new input).
   The worker prompt gains: commit, then `report_done`; `send_message` to the lead for questions.
   In host mode, the team tools are pre-allowed in the settings file (`mcp__agent-office`), since
   the daemon authorizes every call; all other tools still prompt.
8. **Limits (deterministic):** 20 sub-tasks per goal, 30 messages per agent per 10 minutes, 50
   undelivered messages per recipient, 4 000 characters per message, 2 000 per summary, 5 pending
   access requests per agent, 4 MCP connections per agent.

## Changes

- `internal/store`: migration `0004_team.sql`: `agents.role`, `tasks.parent_task_id`,
  `tasks.created_by`, tables `messages` and `access_requests`, and `imports.trigger` gains
  `report_done` (copy-table rebuild with a row-survival test, ADR 0012). `queries.sql`,
  `make generate`.
- `internal/mcp`: tool definitions over a `Team` interface (no engine import), per-agent socket
  listener, stdio bridge. New dependency: `github.com/modelcontextprotocol/go-sdk` v1.8.0 (pinned,
  ADR 0006), which brings `github.com/google/jsonschema-go`.
- `internal/agent`: MCP config file, `--mcp-config` / `--strict-mcp-config` flags, permission
  allow rule in host settings, lead and worker prompts stay in the engine.
- `internal/sandbox`: the MCP socket in `WorkerSpec`; per-agent egress grants.
- `internal/engine`: roles, team operations with authorization, messages and delivery,
  `report_done`, access requests, MCP listeners in both launch paths, restart semantics.
- `internal/api`: `role` on start, access-request endpoints, per-agent messages, new update types.
- `cmd/agent-office`: the `mcp` subcommand.
- `web/`: "Start lead" on goals, role badges, sub-task links, approval queue, message list.
- `test/e2e`: a team run in sandbox mode with real Claude Code (haiku).
- Docs: ADR 0006 accepted, ADRs 0017–0019, architecture, threat model, `.env.example` if a
  variable is added, `PROGRESS.md`, this file.

## Data / API changes

- Migration 0004 only adds columns with defaults (existing agents are workers, existing tasks have
  no parent and were created by the user), adds tables, and rebuilds `imports` with every row kept.
- `AgentInfo.role`, `Task.parent_task_id`, `Task.created_by`.
- `POST /api/agents` accepts `{"task_id", "role"}`.
- `GET /api/access-requests`, `POST /api/access-requests/{id}/approve`, `…/deny`,
  `GET /api/agents/{id}/messages`. Updates `message` and `access_request`; the snapshot carries
  access requests.

## Risks & edge cases

- **PTY delivery collides with the user typing.** Deferred while the user typed recently; the Stop
  path is preferred. Residual: a message typed while the user is composing joins their draft.
- **"Exactly once" ends at the hook forwarder.** A message is marked delivered when the reply is
  handed to the forwarder; if Claude Code then drops it, it is lost (at most once). Recorded in
  ADR 0018; the message list in the UI shows what was delivered and how.
- **Agents ping-ponging messages** cost tokens without end. Bounded by the message rate limit and
  the sub-task cap; budgets come in Phase 8.
- **Haiku may not follow the lead protocol.** The e2e goal is small and concrete; if the model
  misbehaves the daemon still refuses anything outside the rules, which is what the tests check.
- **Claude Code's MCP behaviour** (`--mcp-config`, tool naming `mcp__<server>__<tool>`, env for
  stdio servers, permission rules for MCP tools) is checked in the e2e run with 2.1.319.
- **A dropped stdio bridge** (daemon restart) leaves the agent without team tools until it is
  restarted; restart already ends every agent (ADR 0011).

## Test plan

- `internal/mcp`: every tool against a fake `Team` through a real MCP client over an in-memory
  transport: schema validation (missing / unknown / wrong-typed fields), error mapping; listener
  identity (calls are attributed to the socket's agent), connection limit; bridge round trip and
  daemon-down behaviour.
- `internal/engine`, with the fake `claude`: roles and authorization for every tool (lead-only
  tools refused for workers, workers limited to their lead, a lead limited to its goal), limits,
  one running lead; message delivery on Stop exactly once (two Stops, concurrent Stop and idle
  delivery), `stop_hook_active` and subagent Stops never deliver, idle PTY delivery is sanitized,
  state stays `working` after a block; `report_done` checks and auto-exit; access requests:
  pending → approve grants the host to that agent only, deny, double decision refused, expiry on
  exit and restart; an MCP client over the agent's real socket.
- `internal/store`: migration 0004 keeps rows (imports rebuild).
- `internal/api`: role on start, access-request endpoints (origin guard applies), messages.
- `internal/sandbox`: grants in the egress proxy; the MCP socket mount.
- `make e2e`: existing host and sandbox runs, plus `TestTeamEndToEnd` (sandbox mode, haiku): a
  goal with two small sub-tasks; the lead creates and assigns them, both workers commit and
  report, both tasks end in `review` with imported commits, the lead receives both reports exactly
  once and reports the goal.

## Steps (each one or more commits with `make check` green)

1. Branch `phase-4/team-mcp`; this plan.
2. ADR 0006 accepted; ADRs 0017 (team model), 0018 (message delivery), 0019 (access requests).
3. Migration 0004, queries, generated code, migration test.
4. `internal/sandbox`: egress grants, MCP socket in the worker spec.
5. `internal/mcp`: tools, listener, bridge, tests; `agent-office mcp`.
6. `internal/agent`: MCP config and flags, host-mode allow rule.
7. Engine: roles, tasks with parents, `create_task` / `assign_task` / `list_tasks`, prompts.
8. Engine: messages and delivery.
9. Engine: `report_done`, access requests, MCP listeners in both launch paths.
10. API and UI.
11. e2e team run; docs (architecture, threat model, plan DoD, `PROGRESS.md`), phase summary.

## Open questions (answered with the recommended option)

1. **What does `assign_task` do?** Start a new worker on the sub-task (recommended), rather than
   hand the task to an already running idle agent. One agent per task keeps branches, imports and
   the UI as they are, and a worker's context is not polluted by an earlier task.
2. **Who may the lead spawn?** Only workers, within `--max-agents` (the lead's own slot included)
   and 20 sub-tasks per goal. No human approval per assignment: the goal itself was started by the
   human, and the counts bound the spend. (Alternative: an approval per assignment, which would
   make the lead mostly a suggestion engine.)
3. **Does `report_done` end the worker?** Yes, once its turn ends: it frees the slot for the next
   assignment, and follow-up work becomes a new sub-task. The terminal stays readable.
4. **What can `request_access` ask for?** Network domains only, granted per running agent. Secrets
   and paths wait for the stack packs (Phase 6); the schema already allows those kinds.
5. **Host-mode leads?** Allowed. Host-mode agents still ask before every tool except the team
   tools, so the human approves each step; the e2e for the team flow runs sandboxed.
6. **Message addressing:** workers can only reach their lead; the lead can reach the workers of
   its goal. No worker-to-worker messages in this phase.

## Changes during the phase

(appended as the phase goes)
