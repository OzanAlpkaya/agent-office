# Phase 12 plan: Team workflow and roles

- **Status:** Done on `phase-12/team-workflow`, waiting for Ozan's review (not merged). Written 2026-11-08 with the recommended
  option for every open question and executed straight away, as Ozan asked ("Write the Phase 12
  plan to docs/plans/phase-12.md using your recommended options, then execute it immediately
  without asking me anything"). Every decision is listed in the end-of-phase summary; nothing is
  merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 12 (expanded below into "Team workflow and
  roles"). Progress: [PROGRESS.md](../../PROGRESS.md). Phase order: 8, 12, 9, 10, 11, 13.
- **Prerequisites:** none. `make e2e` at the end of the phase spends a few cents (haiku).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A team run that holds up on real work and looks like a real team: an **Architect** agrees the
plan with the user before work starts, a **Tech Lead** executes it in the order its dependencies
require, **workers** build the sub-tasks, **QA** integrates and tests the result, and an
**Assistant** keeps the user briefed across goals. Roles are data, like stack packs, so new
roles (security, docs, devops) need no code. Parallel work does not silently collide, and every
agent either reaches the user or reaches the agent above it.

## Ozan's requirements (2026-11-08) and the original Phase 12 scope

1. Roles become data: prompt, allowed tools, model, sandbox permissions (read-only vs. commit),
   who the role reports to. New roles as role packs without code changes.
2. Architect: talks with the user first, asks clarifying questions, reads the repository
   read-only, produces a plan (design, sub-tasks, dependencies, files per sub-task, acceptance
   criteria) saved as `docs/specs/<goal>.md`, editable and approvable in the UI. Optional for
   small goals.
3. Tech Lead (the current lead, renamed): executes the approved plan in order, coordinates
   workers, escalates to the Architect when the plan breaks, stays alive until the goal is done.
4. QA: after workers report, merges their branches into an integration branch inside the
   sandbox, runs the full test suite, adds tests for the acceptance criteria, runs and exercises
   the app, files bugs as sub-tasks for the Tech Lead. Distinct from the Reviewer.
5. Assistant: long-running across goals; briefings (done, waiting on the user, blocked, cost),
   reminders of pending decisions, suggestions, curated project memory other agents start with.
   No code, no assignments. Connects to Phase 9.
6. Escalation chain worker → Tech Lead → Architect → user. Only the Architect and the Assistant
   talk to the user; agents without a lead can still leave messages on the task card.
7. Model per role.
8. Conflicts: plan dependencies and file ownership, QA's integration branch, `git merge-tree`
   checks in the main repository on `report_done`, relaunching a worker to resolve conflicts
   after another PR merges.
9. From the roadmap: the lead stays until the goal is done; dependent sub-tasks start only after
   their dependencies; messages to the user; the intermittent `TestTeamAuthorization` failure.

## What exists and what it means

- **Roles are code today.** `agents.role` is `lead | worker | reviewer` (CHECK constraint); the
  MCP server lists tools by those three names; prompts are Go string builders; every agent gets
  the daemon's one `--model`. The engine's authority rules (ADR 0017) are keyed on the role.
- **Behaviour cannot be data.** What a lead may do (create and assign sub-tasks), what a report
  means (import, review, PR) and who may message whom are deterministic rules that must stay in
  code (CLAUDE.md: permissions stay in deterministic code). So a role pack can choose *among*
  behaviours the engine implements and configure them, not invent new authority.
- **Claude Code deny rules hold under `--dangerously-skip-permissions`.** Checked today with
  2.1.319 and haiku: a `permissions.deny` of `Write` removed the tool from the session entirely.
  Bash can still write files, so read-only needs a read-only mount as well (sandbox) or the
  permission prompt (host mode, where Bash prompts anyway).
- **The lead exits after reporting** (`stopAfterTurn` in `reportGoal`), so follow-ups need a new
  lead. Messages to an agent that reported are refused.
- **Workers branch from the main repository's HEAD.** Two workers on one goal can edit the same
  lines; nobody notices until the second PR conflicts on GitHub.
- **Agents can only message agents.** A worker without a lead has no one to tell.
- **`git merge-tree --write-tree`** (git 2.38+; the host has 2.56) computes a merge without a
  working tree, so conflict checks can run in the main repository without touching anyone's
  checkout (ADR 0014 forbids running git inside agent repositories on the host).

## Out of scope

- Several goals in parallel (one Tech Lead and one Architect at a time stays; ADR 0017).
- Resuming a Claude Code session with its context (`--resume`, Phase 9); relaunches start new
  sessions with the context in their prompt, as ADR 0022 does.
- A pull request for QA's integration branch (worker PRs stay the unit of review); the
  integration branch is shown and kept locally. Backlog.
- New behaviours beyond the six below; a role registry or sharing roles between machines.
- Codex adapters for the new roles.

## Sub-phases

The phase is large, so it is split into sub-phases on the one phase branch, each ending with
`make check` green and its own commits. Each later sub-phase builds on the earlier ones.

### 12.1 Roles as data, model per role, Tech Lead lifetime

- **Package `internal/role`** (pack-loader style: strict YAML, every problem reported at once
  with its fix, user-owned files only). A role is `<name>/role.yaml`:

  ```yaml
  schema: 1
  name: security
  description: Checks a sub-task for security problems before it is reported.
  behaviour: worker        # architect | lead | worker | qa | reviewer | assistant
  model: sonnet            # optional; empty = --model
  reports_to: lead         # must be allowed for the behaviour (below)
  workspace: read-write    # read-only | read-write
  tools:
    team: [list_tasks, send_message, report_done, request_access]  # subset of the behaviour's
    deny: [WebFetch]       # Claude Code permission rules, denied in the agent's settings
  prompt: |
    You are the security engineer of a small team ...
  ```

  - **Behaviours** are implemented by the engine and fix the authority (ADR 0017): which team
    tools exist for the role, what a report does, whether the role has a branch that is
    imported. A role chooses one behaviour, may narrow its team tools, denies Claude Code tools,
    picks its model and workspace mode, and brings its own prompt. Behaviour defaults:

    | Behaviour | Team tools | Workspace | Reports to | Lifetime |
    |---|---|---|---|---|
    | architect | list_tasks, send_message, message_user, submit_plan | read-only | user | until the plan is approved; relaunched for escalations |
    | lead | create_task, assign_task, list_tasks, send_message, message_user, start_qa, report_done, request_access | read-only | architect or user | until the goal is done or stopped |
    | worker | list_tasks, send_message, message_user, report_done, report_no_change, request_access | read-write | lead | until it reports (lead-managed) |
    | qa | list_tasks, send_message, report_qa, request_access | read-write | lead | until it reports |
    | reviewer | list_tasks, submit_review | read-write (runs tests, never commits) | none | until it submits |
    | assistant | get_briefing, message_user, list_memory, propose_memory | read-only | user | until stopped |

  - **Built-in roles** (embedded): `architect`, `tech-lead`, `worker`, `qa`, `reviewer`,
    `assistant`, plus examples `docs` (worker) and `security` (worker, read-only checks are not
    enough for a fix, so read-write). **User roles** in `$XDG_CONFIG_HOME/agent-office/roles`
    (`AGENT_OFFICE_ROLES_DIR`, `--roles-dir`); a user role with a built-in's name replaces it
    (shown with its source), so customizing the Tech Lead's prompt is copying one file.
    `agent-office role list|check`.
  - **Model per role:** `model:` in the role, overridden per run by `--role-model
    role=model[,role=model]` (`AGENT_OFFICE_ROLE_MODELS`); empty falls back to `--model`. The
    budget's "full model id without a price" startup check covers role models too.
  - **Read-only workspace:** sandbox: the workspace is mounted `:ro`; both modes: `Edit`,
    `Write`, `NotebookEdit` denied in the settings file. **Deny rules** go into
    `permissions.deny` of the generated settings (host and sandbox).
- **Schema (migration 0008):** `agents.kind` (the behaviour, CHECK-constrained) and `agents.role`
  (the role name, free text, validated by the engine); existing rows: `kind` = old role, `role`
  `tech-lead` for leads. Role names in the API; `"lead"` stays accepted as an alias of
  `tech-lead`.
- **Engine:** authority checks switch from role names to `kind`; `Start` resolves the role
  definition (prompt, model, tools, deny rules, workspace); the MCP server lists the role's
  tools. Prompts move from Go into the role files; the engine still appends the parts that are
  protocol or safety (the task, how to report, message framing), so a role pack cannot break
  them.
- **Tech Lead lifetime:** `report_done` of the goal no longer ends the lead; it stays idle,
  takes follow-up work typed into its terminal (or messages), and may create and assign more
  sub-tasks (the goal moves back to `in_progress`). Moving the goal to `done` (a human
  decision), or stopping it, ends the goal's lead, architect and QA (exit reason `goal_done`).
  **An idle lead keeps its slot**: `--max-agents` keeps counting every running agent, so spend
  and host resources stay bounded by one number; an idle Claude Code session costs nothing.
- **Flake:** reproduce `TestTeamAuthorization` under load first and fix it.

### 12.2 Messages to the user and the escalation chain

- **`message_user(kind, body)`** (`kind`: `question | info | blocked | briefing`): stored in a
  new `inbox` table (agent, task, goal, kind, body, created, read, answered), shown in an
  **Inbox** in the UI header and on the task card, rendered as text, with ADR 0018's limits.
- **Who may talk to the user** (deterministic, from kind, task and the role's `reports_to`):
  architect, assistant, a lead with no architect on its goal (or `reports_to: user`), and any
  worker without a lead (a top-level task). Everyone else is refused with the agent to ask
  instead. Reviewers and QA never message the user (they report).
- **Replies:** the user answers an inbox item in the UI; the reply goes to the agent through
  ADR 0018 delivery as a message **from the user** (`messages.from_user`), framed as the user's
  words rather than as information from other agents. The agent's terminal stays the direct
  channel too.
- **`send_message` addressing:** worker and QA → `lead`; lead → its goal's workers and QA, and
  `architect`; architect → `lead`. An escalation to an architect that has exited relaunches it
  (12.3).

### 12.3 Architect and specs

- **Flow:** the user creates a goal and clicks **Start architect** (or **Start lead** to skip
  it for a small goal). The architect reads the repository (read-only), asks questions with
  `message_user` or in its terminal, then calls **`submit_plan`** with a structured plan:
  `summary` and `design` (Markdown text), `sub_tasks` (`key`, `title`, `description`, `role`,
  `depends_on` keys, `files` it touches, `acceptance` criteria), `risks`. The daemon validates it
  (at most 20 sub-tasks, unique keys, dependencies exist and are acyclic, clean relative paths,
  worker-behaviour roles) and stores it as a **spec version** (`specs`: goal, version, status
  `proposed | approved | superseded`, author, the plan as JSON, the rendered Markdown).
- **UI:** the goal card shows the plan; **Edit** (summary, design and every sub-task field)
  saves a new version by the user; **Approve** is the human gate.
- **On approval** (deterministic): the Markdown is committed as `docs/specs/<goal-slug>.md` on
  the goal's **spec branch** `agent-office/spec-<slug>-<goal id>` in the main repository, built
  with plumbing on top of the default branch (no working tree, no checkout, the user's
  identity, an `agent-office` trailer); the sub-tasks are created from the plan (dependencies
  and files resolved to task ids); the architect is ended (`plan_approved`); the Tech Lead
  starts on the goal with the plan in its prompt. Sub-task workers branch from the spec commit,
  so the spec is in every branch and reaches the default branch with the first merged PR.
- **Escalation:** the Tech Lead sends `send_message(to="architect")` when the plan breaks; an
  exited architect is relaunched (ADR 0022 path, now for architects too) with the approved
  spec, the escalation and the instruction to revise with `submit_plan` (which needs the user's
  approval again) or answer the lead. Approving a revision adds new sub-tasks, updates sub-tasks
  that are still untouched `todo`, deletes untouched `todo` sub-tasks the revision dropped, and
  leaves everything else as it is (listed in the result).

### 12.4 Dependencies, file ownership and conflicts

- **Sub-task fields** (migration 0008): `depends_on` (task ids), `files` (paths, directories
  ending in `/`, or globs), `acceptance` (criteria), `role` (which worker role builds it),
  `spec_key`. `create_task` takes them too, so a lead without an architect can plan dependencies.
- **Dependencies are enforced by `assign_task`:** a sub-task starts only when every dependency
  is `review` or `done` (reported, committed). Its worker **branches from its dependencies'
  work**: one dependency's imported head, or a merge of several made with `merge-tree` +
  `commit-tree` in the main repository (refused with the conflicting files if they do not merge
  cleanly). Dependent PRs are stacked on their dependencies' commits.
- **File ownership is enforced by `assign_task`:** a sub-task whose declared files overlap a
  running sub-task of the goal, with no dependency between them, is refused ("make one depend
  on the other, or wait"). Sub-tasks without declared files are not checked; the prompts ask
  for files.
- **Conflict checks on `report_done`:** in the main repository, `git merge-tree --write-tree
  --name-only` of the reported head against the default branch and against the other open
  sub-task branches of the goal (reported or running with an imported head). Results are stored
  on the task (`tasks.conflicts`), shown on the task card and its PR, and listed in the report
  message to the Tech Lead. Not a refusal: the work is committed; the lead decides.
- **After a PR merges** (GitHub poll): the daemon fetches the base branch into the main
  repository, re-checks every other open PR, records the conflicts and **relaunches the worker
  of each conflicting PR** to merge the new base and resolve them (at most 2 automatic conflict
  rounds per PR, `pull_requests.conflict_rounds`), within slots; its `report_done` needs a new
  commit. Sandboxed workers get the new base as a ref set in their repository by a helper
  container before the relaunch (the host still never runs git there). **Resolve conflict** on
  the task card does the same by hand, also without GitHub (against the main repository's
  HEAD).

### 12.5 QA and the integration branch

- **`start_qa`** (Tech Lead): refused while a sub-task is in progress or none is reported.
  Starts a QA agent (behaviour `qa`) on the goal, in its own workspace on
  `agent-office/qa-<slug>-<id>` at the goal's base (the spec commit or HEAD), with every
  reported sub-task branch present as a branch in its repository (refs created when the clone is
  set up, before the agent has access). The prompt lists the branches and each sub-task's
  acceptance criteria and says: merge them into your branch, run the full test suite, add tests
  for the acceptance criteria, run the app and exercise it, commit, report.
- **`report_qa(verdict, summary, bugs)`** (`pass | fail`; at most 10 bugs, each a title,
  description and the sub-task it belongs to): the branch is imported like a report, the run is
  stored (`qa_runs`: goal, agent, status, branch, the heads it merged, verdict, summary, bugs),
  each bug becomes a `todo` sub-task created by QA (within the 20 per goal), the Tech Lead gets
  the result, and QA ends after its turn.
- **Gate:** the Tech Lead's `report_done` for the goal needs a passing QA run on the current
  heads of the reported sub-tasks (recomputed, so new work needs a new run). `--qa=false`
  (`AGENT_OFFICE_QA`) turns QA off for small projects.
- The integration branch is imported into the main repository and shown on the goal card with
  the verdict; it is never pushed by the daemon.

### 12.6 Assistant and project memory

- **Assistant** (one at a time, no task, read-only workspace on HEAD): started from the header.
  `get_briefing` returns a deterministic summary from the database: goals and their sub-task
  counts by status, what waits on the user (plans to approve, unanswered questions, access
  requests, flagged commits, open PRs to merge), blocked tasks, running agents, cost totals and
  budgets. It answers with `message_user(kind="briefing")` and stays idle for questions in its
  terminal or the inbox. **Briefing** in the header asks a running assistant for a new one.
- **Project memory** (the start of Phase 9): `memory` table (kind `decision | convention |
  lesson`, title, body, status `proposed | accepted`, source). The Assistant proposes entries
  with `propose_memory` and reads them with `list_memory`; the user accepts, edits or deletes
  them in a **Memory** panel, and can add their own. Only **accepted** entries are added to
  every agent's first prompt (newest first, at most 6 000 characters), framed as notes approved
  by the user. Proposals never reach other agents until a human accepts them, so a
  prompt-injected assistant cannot plant instructions in the team.

### 12.7 End-to-end, docs

- `make e2e`: the existing team run (lead stays idle after reporting; stopped by moving the goal
  to done) and a new **team workflow** run in sandbox mode: architect plan submitted, approved
  through the API, Tech Lead assigns two dependent sub-tasks, the second starts only after the
  first and branches from it, QA merges and passes, the goal is reported.
- ADRs, architecture, threat model, `.env.example`, `docs/roles.md`, `docs/plan.md`,
  `PROGRESS.md`, this file.

## Changes

- `internal/role/` (new): loader, built-in roles (`builtin/<name>/role.yaml`), tests.
- `internal/store`: migration `0008_team_workflow.sql`, `queries.sql`, `make generate`.
- `internal/engine`: roles in `Start`, kinds, prompts, lead lifetime, inbox, escalation, specs,
  dependencies, conflicts, QA, assistant, memory; tests.
- `internal/mcp`: tool lists per role; new tools `message_user`, `submit_plan`, `start_qa`,
  `report_qa`, `get_briefing`, `list_memory`, `propose_memory`; new `create_task` and
  `assign_task` fields.
- `internal/agent`: deny rules in the settings; `internal/sandbox`: read-only workspace mount;
  `internal/git`: `MergeTree`, plumbing commit, `Fetch`, extra refs in shared clones.
- `internal/api`: roles, inbox, specs, memory, QA runs, resolve conflict; agent start by role.
- `cmd/agent-office`: `--roles-dir`, `--role-model`, `--qa`; `role list|check`.
- `web/src`: role picker, Inbox, spec view and editor, dependencies and conflicts on task cards,
  QA panel, Assistant and Memory; Vitest and Playwright.
- Docs as listed in 12.7.

## Data / API changes

- Migration 0008 (forward-only): `agents.kind`, `agents.role` without CHECK; `tasks.depends_on`,
  `files`, `acceptance`, `role`, `spec_key`, `conflicts`, `conflicts_checked_at`;
  `messages.from_user`; `pull_requests.conflict_rounds`; new tables `inbox`, `specs`,
  `qa_runs`, `memory`.
- `AgentInfo.kind`; `Task` gains the new fields; updates `inbox`, `spec`, `qa_run`, `memory`.
- New endpoints: `GET /api/roles`; `GET /api/inbox`, `POST /api/inbox/{id}/reply|read`;
  `GET /api/specs`, `PUT /api/specs/{id}`, `POST /api/specs/{id}/approve`;
  `POST /api/tasks/{id}/resolve-conflict`; `GET /api/qa-runs`; `GET|POST /api/memory`,
  `PATCH|DELETE /api/memory/{id}`, `POST /api/memory/{id}/accept`; `POST /api/assistant/briefing`.
- New flags/env: `--roles-dir` / `AGENT_OFFICE_ROLES_DIR`, `--role-model` /
  `AGENT_OFFICE_ROLE_MODELS`, `--qa` / `AGENT_OFFICE_QA`.

## Risks & edge cases

- **Scope.** Six behaviours, a spec editor and QA in one phase. Mitigation: sub-phases with
  green checks each; the e2e at the end covers the whole chain once.
- **Roles as data widen what agents can do.** A role cannot add authority: behaviours and their
  maximum tool sets are code; a role can only narrow tools, deny more Claude Code tools and
  choose read-only. Roles come from the binary or a user-owned directory, never the repository.
  `workspace: read-write` for an architect or assistant is allowed (the user's choice) and shown.
- **The model ignores the plan.** Dependencies and file overlap are enforced by `assign_task`,
  not by the prompt; the QA gate is enforced by `report_done`.
- **Stacked branches age.** A dependent sub-task branches from its dependency's head at
  assignment; later follow-up commits on the dependency are not in it. Conflict checks show the
  result; documented.
- **merge-tree is an approximation of GitHub's merge** (same algorithm, but GitHub merges into
  the then-current base). Checks run again after every merge.
- **Agents talking to the user is a new injection surface.** Inbox items are agent-written text,
  rendered as text and labelled with the agent; replies are the user's own words; the caps of ADR
  0018 apply.
- **Project memory is a persistent injection channel.** Only user-accepted entries reach other
  agents; size-capped; shown with their source.
- **Idle coordinators hold slots.** With `--max-agents 4`, a Tech Lead and an Assistant leave two
  worker slots. Documented; the user raises `--max-agents`.

## Test plan

- Unit: role loader (valid built-ins, every invalid field with its message, user overrides,
  ownership checks), behaviour tool sets, plan validation (cycles, unknown keys, paths), file
  overlap, merge-tree output parsing, briefing aggregation, memory prompt cap.
- Engine (fake Claude Code): role resolution (model, deny rules, read-only, tools), lead stays
  after its report and ends when the goal is done, message_user rules for every kind, replies
  delivered as the user's words, submit_plan → approve → spec commit on the spec branch, tasks
  created with dependencies, Tech Lead started; escalation relaunches the architect; assign
  refused for unmet dependencies and overlapping files; dependent base is the dependency's head
  (and a merge of two); conflicts detected on report_done with two branches editing the same
  lines (DoD); resolve-conflict relaunch with a new base ref; QA start rules, extra branches,
  report_qa bugs → sub-tasks, goal gate; assistant briefing; memory in prompts only once accepted.
- Store: migration 0008 on a database with rows from every earlier migration (kinds, roles).
- API and UI: endpoint shapes and validation; Vitest for the pure helpers; Playwright for the
  inbox, plan approval and the task card's dependencies and conflicts.
- `make check` on every commit; `make ui-test`, `make sandbox-test` and `make e2e` at the end.

## Steps

1. Branch, this plan.
2. 12.1 roles (flake first), with ADR 0029.
3. 12.2 inbox and chain, ADR 0030.
4. 12.3 architect and specs, ADR 0031.
5. 12.4 dependencies and conflicts, ADR 0032.
6. 12.5 QA, ADR 0033.
7. 12.6 assistant and memory, ADR 0034.
8. UI for each sub-phase as it lands; 12.7 e2e, docs, `PROGRESS.md`.

## Open questions (answered with the recommended option)

1. **Can a role pack define new behaviour?** No: a role picks one of six behaviours the engine
   implements and configures it (prompt, model, narrower tools, deny rules, workspace, reports
   to). Authority stays in code (CLAUDE.md). Security, docs and devops roles are worker or
   reviewer behaviours with their own prompt and tools, so they need no code.
2. **May a user role replace a built-in?** Yes, by using its name; the UI and `role list` show
   the source. Unlike stack packs, a role cannot widen the sandbox (no hosts, secrets or image),
   so shadowing has no security cost and makes customizing a built-in one file.
3. **Does an idle Tech Lead keep its slot?** Yes. `--max-agents` stays one bound on everything
   running; idle sessions cost no tokens; exiting and resuming waits for Phase 9.
4. **Where does the spec live?** In the database as versions (the editable source), and on
   approval as `docs/specs/<slug>.md` committed on a spec branch in the main repository that
   every sub-task branch starts from. The daemon never commits to the user's branches or
   working tree.
5. **When is a dependency satisfied?** When it is `review` or `done` (reported and committed),
   and the dependent branches from its work; waiting for a human merge would stall every team
   run.
6. **Does a conflict refuse `report_done`?** No: the work is committed and worth keeping; the
   conflict is shown and sent to the lead, and resolved by a relaunch after a merge or by hand.
7. **Who starts QA?** The Tech Lead (`start_qa`), gated: the goal cannot be reported without a
   passing run on the current heads. The daemon enforces the order; the lead chooses the moment.
8. **How does the user talk to the Architect?** In its terminal (as today) and through the
   Inbox: questions from `message_user` appear there and replies are delivered to the agent.
9. **Is the Assistant a model or a report?** Both: the facts (`get_briefing`) are computed by
   the daemon, so counts and costs are exact; the model words the briefing, suggests next steps
   and proposes memory.
10. **Built-in models per role?** Built-in roles inherit `--model`; `--role-model` sets them per
    run (recommended: `architect=opus,tech-lead=sonnet,worker=sonnet,qa=sonnet,assistant=haiku`).
    A built-in default model would silently change the cost of every existing setup.

## Changes during the phase

- **ADRs as planned:** 0029 (roles as data, model per role, the Tech Lead stays), 0030 (escalation
  chain and messages to the user), 0031 (architect plans and specs), 0032 (dependencies, file
  ownership, conflicts), 0033 (QA), 0034 (assistant and project memory). `docs/roles.md` is the
  role file reference.
- **One migration for the phase** (0008), as planned: `agents.kind` + `agents.role`, plan fields
  and conflicts on tasks, `messages.from_user`, `pull_requests.conflict_rounds`, `inbox`, `specs`,
  `qa_runs`, `memory`. Leads become `kind=lead, role=tech-lead`
  (`TestMigration0008KeepsPhase8Rows`).
- **Deny rules checked before relying on them:** with Claude Code 2.1.319 and haiku, a
  `permissions.deny: ["Write"]` in `--settings` removes the tool even under
  `--dangerously-skip-permissions`. Read-only roles therefore get deny rules plus, sandboxed, a
  `:ro` workspace mount.
- **Prompts split between the role and the engine:** a role's prompt comes first; the goal, task,
  plan, reporting protocol, network rule, framing and accepted memory are appended by the engine,
  so a role file cannot change how work is reported. The reviewer's instructions moved into its
  role file; the engine adds the pull request's data.
- **A user role may replace a built-in** by name (it must keep the behaviour), unlike stack packs:
  a role cannot widen the sandbox, and it makes customizing the Tech Lead one file.
- **Escalation to an exited architect relaunches it** (ADR 0022's relaunch now allows architects);
  the lead's message is queued after the relaunch, so it arrives at the architect's first Stop.
- **Dependencies are met at `review`** (reported), and a dependent sub-task's branch starts from
  their work: one head, or a `merge-tree` + `commit-tree` merge of several made in the main
  repository. `assign_task` also refuses a sub-task whose declared files overlap a running one
  without a dependency between them. These rules live in `deps.go` and landed with 12.3, since the
  approved plan's sub-tasks needed them.
- **QA gate also requires QA's bug sub-tasks to be closed** (reported, or marked done by the user),
  not only a passing run on the current heads; otherwise a second passing run could report a goal
  whose bugs were never fixed.
- **Assistant briefing button:** `POST /api/assistant/briefing` starts the assistant or sends a
  running one "Please give me a new briefing." as the user's message.
- **Snapshot:** `/api/events` now carries the inbox, specs, QA runs and memory; the UI accepts an
  older daemon's snapshot without them.
- **UI:** header Inbox (unread count), Memory (proposals) and Briefing; goal cards show the current
  plan (edit as a new version, approve), the last QA run and their inbox items; task cards show
  role, dependencies (Start agent disabled while they are unreported), files, acceptance criteria
  and conflicts with **Resolve conflict**; Start agent offers worker roles; goals get Start
  architect and Start Tech Lead. Helpers in `web/src/workflow.ts` (first named `team.ts`, renamed
  because `Team.tsx` differs only in case).
- **Found and fixed, pre-existing:**
  - `TestTeamAuthorization` (the Phase 8 backlog item): a real data race. `Start` returned the
    agent's view after releasing `Engine.mu` while its SessionStart hook could already change its
    state. Reproduced in 6 of 120 runs under load (`-race -count=40 -cpu 1,2,8` next to a full test
    run), 0 races after the fix. Under the same load 2 of 120 runs still timed out waiting for an
    agent to start: the fake CLI's 2 s hook timeout dropped a SessionStart; it is now 8 s (Claude
    Code's is 10 s) and the team helpers wait up to 15 s.
  - `TestGitHubLoop` failed once with "rounds 0": `decideFollowup` claimed a follow-up and counted
    its round in two statements; now one transaction.
  - `TestRestartReoffersFollowup` waited for the follow-up claim, which happens before the
    relaunch runs, and could shut the daemon down too early; it now waits for the relaunched
    worker.
- **Process slip, corrected before review:** staging hunks with zero-context patches put one line
  of the race fix into the wrong function in three intermediate commits (the final tree was
  right). Those commits were rebuilt from `6936bea` so each commit builds and vets; the branch has
  not been pushed.
- **With `--qa=false`** the Tech Lead's prompt says QA is off (found while writing the e2e).
- **Independent review** (a separate reviewer agent on the branch diff; no blockers, ten
  should-fix, nine taken):
  - typed (idle) delivery collapsed line breaks and with them the `> ` quoting, so an agent's
    message could contain a readable "From the user:" header: typed bodies are now JSON strings;
  - the file-overlap check and the "being assigned" mark were separate steps (two assignments at
    once could both pass), and a plan approval could rewrite a sub-task whose worker was starting:
    in-flight assignments now count as running and as touched;
  - an unlocked read of an agent's info in `message_user`;
  - `report_done` now refuses kinds other than worker and lead (defence in depth);
  - QA's pass must contain every branch it was given (checked with `merge-base --is-ancestor`);
  - a goal can only be reported when every sub-task is reported or closed by the user;
  - an escalation relaunches the architect at most 3 times per goal; plan versions are limited to
    5 per goal per 10 minutes and 30 in all;
  - nits taken: the spec branch moves after the approval is recorded; the lead cannot message
    reviewers; a conflict relaunch keeps the worker's role; deleted dependencies do not block;
    `**` globs overlap by literal prefix; briefings are the assistant's only; `CommitFile` needs an
    absolute temporary directory; the assistant's prompt marks agent text in briefings as
    information;
  - not taken: a worker whose lead was stopped may still message the user, because ADR 0030 treats
    a stopped lead as no lead (otherwise that worker can reach nobody, the original problem).
    Noted, not changed: `report_done` runs its `merge-tree` checks synchronously (fast for the
    handful of branches of a goal); the sibling conflict entries are display-only and can lose an
    update when two reports finish at the same moment; `FetchBranch` force-updates
    `refs/remotes/<remote>/<base>` in the main repository (the remote-tracking ref, as `git fetch`
    does; ADR 0032).
- **Found by the real e2e runs:** the first full `make e2e` passed (host, sandbox, team, workflow;
  GitHub not configured). After the review fixes the workflow test stalled once: haiku as Tech
  Lead read alpha's report, listed the tasks and ended its turn without assigning beta. Reports to
  the lead now name the sub-tasks that became ready, or say that everything is reported and to
  call `start_qa`; a passing QA result says to report the goal. Then 3 of 3 workflow runs passed
  (83–98 s each).
- **Tests run at the end:** `make check` (Go with `-race`, 151 Vitest tests, typecheck, build);
  `make ui-test` (21 Playwright tests, 4 new); `make sandbox-test` against rootless Podman (one
  skip that needs internet, as before); `make e2e`: `TestEndToEnd`, `TestSandboxEndToEnd`,
  `TestTeamEndToEnd`, `TestTeamWorkflowEndToEnd` passed on the branch before the review fixes;
  after them all four passed again (`TestTeamWorkflowEndToEnd` 3 of 3). `TestGitHubEndToEnd` was skipped (`AO_E2E_GITHUB_REPO`
  unset); the after-merge relaunch is covered against the GitHub fake.
