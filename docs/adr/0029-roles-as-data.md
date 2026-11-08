# 0029. Roles as data over engine behaviours; model per role; the Tech Lead stays

- **Status:** Accepted; `list_memory` added to the architect, lead, worker and qa ceilings by ADR 0037
- **Date:** 2026-11-08
- **Amends:** ADR 0017 (roles become `kind` + role name; the lead stays after its report),
  ADR 0021 (the reviewer's prompt comes from its role)

## Context

Through Phase 8 a role was a Go constant (`lead | worker | reviewer`, a CHECK constraint in
SQLite) with its prompt in Go string builders, its MCP tools picked by name and one `--model`
for every agent. Ozan asked (2026-11-08) for roles to become data like stack packs: a prompt,
allowed tools, model, sandbox permissions (read-only or commit) and who the role reports to, so
that security, docs or devops roles need no code. Phase 12 also adds architect, QA and
assistant roles.

Forces:

- What a role may *do* is authority: creating and assigning sub-tasks starts paid sessions,
  a report moves tasks and pushes branches, messages reach other agents and the user.
  CLAUDE.md: permissions stay in deterministic code. A YAML file must not be able to grant them.
- What a role *is told* and *runs with* (prompt, model, which Claude Code tools it has, whether
  its files are writable) is configuration the user should own.
- Roles must not come from the repository (the same argument as ADR 0023: agents influence the
  repository through their merged branches).
- Claude Code deny rules: checked on 2026-11-08 with 2.1.319 and haiku, a
  `permissions.deny: ["Write"]` in `--settings` removes the tool from the session even with
  `--dangerously-skip-permissions`. Bash can still write files.

## Options considered

**What a role file can define**

1. **Everything, including authority** (a list of capabilities such as `can_assign`). Pros: any
   role without code. Cons: a role file becomes a permission system; a mistake or a copied role
   widens what a prompt-injected agent can do; every engine rule needs a capability flag.
2. **A choice of behaviour plus configuration.** The engine implements six behaviours
   (architect, lead, worker, qa, reviewer, assistant) with their authority and lifecycle; a role
   picks one and configures prompt, model, workspace mode, reports-to, a subset of the
   behaviour's team tools and denied Claude Code tools. Pros: authority stays in code, a role can
   only narrow it; security/docs/devops are worker or reviewer behaviours with their own prompt.
   Cons: a genuinely new behaviour (say, a release manager that tags releases) needs code.

**May a user role replace a built-in?**

1. **No** (as for stack packs). Cons: customizing the Tech Lead means a new name and no way to
   make agent-office start it for QA or reviews.
2. **Yes, by name, keeping the behaviour.** A role cannot widen the sandbox (no hosts, secrets
   or image), so shadowing costs nothing in security; the source is shown.

**Model per role**

1. **Built-in defaults** (architect opus, others sonnet). Cons: silently changes the cost of
   every existing setup, and the e2e's `--model haiku`.
2. **Inherit `--model`; set per role in the role file or `--role-model role=model`.**

**Does an idle Tech Lead keep its `--max-agents` slot?** (the Phase 12 roadmap question)

1. **Yes**: one bound on everything running. An idle Claude Code session makes no API calls.
2. **No, exit and resume later**: needs session resume (Phase 9).
3. **Separate limits** for coordinators and workers. Cons: two numbers to tune, and a
   coordinator is still a container with memory.

## Decision

Option 2, option 2, option 2 and option 1.

- **Package `internal/role`.** `<name>/role.yaml`, `schema: 1`, strict decoding, every problem
  reported with its fix (`docs/roles.md`). Fields: `name`, `description`, `behaviour`, `model`,
  `reports_to`, `workspace` (`read-only | read-write`), `tools.team` (a subset of the
  behaviour's; the tools a behaviour needs to hand its result back are required),
  `tools.deny` (Claude Code permission rules), `prompt` (with `{max_sub_tasks}` and
  `{max_workers}` placeholders). Built-ins are embedded; user roles live in
  `$XDG_CONFIG_HOME/agent-office/roles` (`AGENT_OFFICE_ROLES_DIR`, `--roles-dir`), user-owned,
  no group/other write, no symlinks; an invalid user role fails startup. `agent-office role
  list|check`, `GET /api/roles`.
- **Behaviours** and their ceilings:

  | Behaviour | Team tools at most | Default workspace | reports_to |
  |---|---|---|---|
  | architect | list_tasks, send_message, message_user, submit_plan | read-only | user |
  | lead | create_task, assign_task, list_tasks, send_message, message_user, start_qa, report_done, request_access | read-only | architect, user |
  | worker | list_tasks, send_message, message_user, report_done, report_no_change, request_access | read-write | lead |
  | qa | list_tasks, send_message, report_qa, request_access | read-write | lead |
  | reviewer | list_tasks, submit_review | read-write | none |
  | assistant | get_briefing, message_user, list_memory, propose_memory | read-only | user |

- **Schema (migration 0008):** `agents.kind` is the behaviour (CHECK-constrained) and
  `agents.role` the role's name (free text up to 32 characters); existing leads become
  `kind=lead, role=tech-lead`. Every authorization check in the engine uses `kind`. The API
  accepts role names; `"lead"` stays an alias of `tech-lead`.
- **Enforcement of a role's configuration:** the MCP server lists only the role's team tools
  (and the engine still authorizes every call by kind); `tools.deny` and, for read-only roles,
  `Edit`, `Write`, `NotebookEdit` go into `permissions.deny` of the agent's generated settings;
  a read-only sandboxed agent's workspace is mounted `:ro`, so Bash cannot change it either. In
  host mode Bash still prompts (permission prompts are on there).
- **Prompts:** the role's prompt comes first; the engine appends what is protocol or safety
  (the goal or task, how to report, the network rule, message framing), so a role file cannot
  change how work is reported.
- **Models:** a role's `model`, else `--model`; `--role-model architect=opus,worker=sonnet`
  (`AGENT_OFFICE_ROLE_MODELS`) overrides per run. The budget startup check (ADR 0027) covers
  every role model.
- **One at a time:** at most one running lead, architect, QA and assistant (one goal at a time,
  ADR 0017).
- **The Tech Lead stays:** the goal's `report_done` moves it to `review` but no longer ends the
  lead; it stays idle, takes follow-up work (typed, or messages), and may create and assign more
  sub-tasks, which moves the goal back to `in_progress`. Marking the goal `done` (a human
  decision) ends its lead, architect and QA (exit `goal_done`); workers are left to finish.
  The idle lead keeps its slot.

## Consequences

- Security, docs and devops roles are YAML files; the built-in `docs` and `security` roles show
  how. A new behaviour is a code change and an ADR, on purpose.
- A role can make an agent weaker (fewer tools, read-only) but not stronger than its behaviour.
  `workspace: read-write` on an architect or assistant is allowed; it is the user's file.
- With `--max-agents 4`, a Tech Lead and an Assistant leave two slots for workers, QA and
  reviewers; the user raises `--max-agents` for bigger teams.
- The role name is shown in messages (`From agent-1a2b3c (tech-lead, ...)`) and in the UI; the
  kind drives colours and poses.
