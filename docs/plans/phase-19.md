# Phase 19 plan: Lessons from the game factory

- **Status:** In progress. Written 2026-11-12 with the recommended option for every choice, as Ozan asked
  ("run it like the earlier phases ... with the recommended options, no check-ins, one summary at the end").
- **Roadmap:** [docs/plan.md](../plan.md), Phase 19. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Source:** Ozan's runs of Bouncibo Goals 01-02.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

agent-office runs unattended as a systemd user service. Plans are approved with one click and changed with
one sentence. Agent slots and the sub-task limit go to open work only. Follow-up work after a squash merge
starts from the target branch, so it merges without history-only conflicts. The board and the task modal
show what each QA round and each agent run did.

## What exists and what it means

- **serve** runs in a terminal; nothing restarts it when it dies. Its flags all have `AGENT_OFFICE_*`
  environment equivalents. A systemd user service does not inherit the shell's environment or `PATH`.
- **Plans** (ADR 0031): the plan card shows every field of every sub-task in one big form under "Edit". The
  only way to ask for changes is to answer the Architect in the inbox or its terminal.
- **The Architect** is stopped when its plan is approved (`ApproveSpec`, `ExitPlanApproved`). An Architect
  that is started again for a Tech Lead's escalation and only answers the lead stays running and holds a
  slot. So does one restored after a daemon restart.
- **Caps** (ADR 0047): a project's `max_agents` is clamped to `--max-agents` when the project opens. The
  daemon-wide cap is fixed at start. Any project settings change reopens the project's engine, so it is
  refused while an agent runs (`ErrProjectBusy`). The office layout is the only exception.
- **Sub-tasks:** at most `MaxSubTasks = 20` per goal, counted over every sub-task ever created, done ones
  included. Tasks have no "superseded" state: a sub-task the lead replaces stays in progress with no agent.
- **Bases** (ADR 0031, 0032): a sub-task starts from its dependencies' work, else from the approved spec
  commit; a taken-back sub-task starts from its previous worker's branch. QA starts from the spec commit and
  merges every reported sub-task branch. After the user squash-merges the QA branch into `main` (ADR 0055),
  new sub-tasks still start from the spec commit and QA still merges the pre-squash branches. `main` then
  holds the same changes as a squash commit with other history, which caused Goal 02's 35 conflicts.
- **QA** shows only as a chip on the goal card and as a panel in the goal's modal.
- **The task modal** shows the task, its agents (name and state) and the cost.
- **Agent states** come from hooks: starting, idle, working, waiting_permission, exited. A turn that ends
  while a `run_in_background` shell still runs reads as Idle.

## Decisions (recommended options)

| # | Question | Decision |
|---|---|---|
| 1 | Service manager | A **systemd user unit** (`$XDG_CONFIG_HOME/systemd/user/agent-office.service`, default `~/.config/...`); Linux-first, no sudo. |
| 2 | What the unit runs | `service install [serve flags]` parses the flags with serve's own parser (an invalid flag fails before anything is written). `ExecStart` is the absolute, symlink-resolved path of this binary plus `serve` and the flags exactly as given, quoted for systemd. `Environment=PATH=` is the installing shell's `PATH`, so `claude`, `podman` and `git` resolve as they do in the shell. `AGENT_OFFICE_*` variables set in the shell are **not** copied: install names each one it sees and tells the user to pass it as a flag. The unit has `Restart=on-failure`, `RestartSec=5s`, `KillMode=mixed` (SIGTERM to the daemon only, which stops its agents and records them for `--restore-agents`; anything left is killed after `TimeoutStopSec=60s`) and `SyslogIdentifier=agent-office` (stdout and stderr go to the journal). |
| 3 | Install, uninstall, status | `install` writes the unit (replacing an older one), runs `systemctl --user daemon-reload` and `enable --now` (or `restart` if it was running); `--no-start` only writes and enables it. If a daemon already answers on the data dir's control socket outside the unit, install refuses. `uninstall` runs `disable --now`, removes the file and reloads. `status` prints the unit path, its `ExecStart`, `is-enabled` and `is-active`, whether lingering is on (`loginctl show-user`) with the command to turn it on, and `journalctl --user -u agent-office -f` for the logs. systemctl is run through an interface, so tests use a fake. A test runs `systemd-analyze --user verify` on the written unit when that command exists. |
| 4 | Request changes | A **Request changes** box on a proposed or approved plan: one text field of up to 4000 characters, then Send. The request is stored on the plan version (`specs.change_request`, `change_requested_at`) and shown there ("Changes requested, waiting for the Architect"). It reaches the Architect as the user's words. A running Architect gets it as a user message. An exited one is started again on the same agent and workspace, with the plan and the request in its prompt, which agent-office owns (memory: team messages are not instructions). The Architect answers with `submit_plan`; Approve keeps working on the proposed version meanwhile. The plan rate limit applies. User change requests do not use up the escalation relaunch limit. |
| 5 | Lighter editing | The plan shows each sub-task as a row with **Edit** and **Remove**. Edit opens that one sub-task inline: title, description and acceptance criteria, with key, role, dependencies and files under "More". The summary, design and risks have their own Edit. Each save is a new proposed version, as before. The full form goes away. |
| 6 | The Architect exits | (a) On approval, as today. (b) An Architect started again for an escalation or a change request is stopped with `plan_approved` when a turn ends while its goal has an approved plan, no proposed version and no message waiting for it. (c) `--restore-agents` does not restore an Architect whose goal has an approved plan and no proposed version. (d) It is stopped when its goal is done, as today. |
| 7 | Effective cap shown | Project settings → General: "Max agents" shows "Effective: K (the smaller of this project's N and the daemon's M). The Tech Lead, Architect and QA count too." The rail's agent list header shows "running / effective cap". |
| 8 | Daemon-wide cap at runtime | Settings → App gets "Max agents (all projects)", 1-32, saved in a new `daemon_settings` table, so it survives a restart. A saved value wins over `--max-agents`: the daemon logs both at start, and the setting shows the flag's value with "Use --max-agents" to clear it. Lowering it stops nobody; new starts wait until agents exit. API: `GET/PUT /api/daemon`. |
| 9 | Settings while agents run | Settings that only matter when an agent starts are applied to the running engine without a reopen, at once, while agents run: `max_agents`, `max_sub_tasks`, `model`, `role_models`, `codex_model`, budgets, `worker_memory`, `worker_pids`, `access_decider`, `lead_access_hosts`, `merge_target` and `office_layout`. The rest change what the engine is built from (`pack`, `secrets_dir`, `egress_allow`, `github`, `github_remote`, `reviewer`, `review_rounds`, `role_clis`) and still need the agents stopped. The refusal names those fields. The engine keeps the live settings behind an atomic pointer, read at each use. |
| 10 | Which sub-tasks count | **Open** sub-tasks count: to do, in progress and blocked. Reported (review) sub-tasks hold finished work, so they don't count, and neither do done or superseded ones. A fixed ceiling of 200 sub-tasks per goal, counted over all of them, bounds a runaway loop. |
| 11 | Per-project limit | Project setting `max_sub_tasks`, 1-100, default 20 (the daemon default; no flag). Used by `create_task`, QA bug filing, plan validation, plan approval and the lead's prompt (`{max_sub_tasks}`). |
| 12 | Superseding | `create_task` gets `supersedes`: ids of the goal's sub-tasks the new one replaces. Each must be a sub-task of the goal, not done, with no running agent (otherwise: "stop_agent first"). They are closed: status done, `tasks.superseded_by` set to the new task. An approved plan revision that drops a sub-task already worked on closes it the same way when no agent runs on it (`superseded_by` = the spec version); a running one is kept, as today. The card shows "Superseded" with a link to the replacement, and the lead's prompt explains the field. |
| 13 | Follow-up base | Sub-tasks from the approved plan without dependencies keep starting from the spec commit, so they run side by side. A sub-task that is not from the plan (created by the lead or by QA after approval) starts from **the goal's latest finished work**. That work is the latest finished QA run's head (passed or failed), or the spec commit if no QA run finished. Every reported or done, not superseded, sub-task head that is not in it yet is merged onto it without a working tree. A head that conflicts is left out and logged. Dependencies are merged on top, as today. |
| 14 | After a squash merge | A goal has a **merged cut** once a recorded goal merge's commit is in the target branch. The merged heads are the merge's QA head and every sub-task head that QA run merged. Then: (a) new sub-tasks (decision 13) and QA start from the target branch's current head, with the not-yet-merged reported heads ported onto it (below); (b) QA merges only sub-tasks whose head is not in a merged head; (c) the QA gate and Merge treat the merged sub-tasks as covered. |
| 15 | Porting a pre-squash branch | A sub-task head that is not merged and does not contain the latest merge commit (a worker that kept its branch, a follow-up round, a taken-back task) is **ported**. That is a three-way merge onto the target head whose base is the newest common ancestor of the head and the merged heads (`git merge-tree --merge-base`), committed with the target head as the only parent. Only the changes made after the already-merged part move. The ported commit gets a branch `agent-office/port-<task>-<sha>` that QA merges in place of the original. The QA run records both heads: the task's own, for the gate, and the ported one, for the pass check. A port that conflicts is refused with the files, so the Tech Lead assigns a sub-task that resolves it. |
| 16 | QA cards | Each QA run of a goal is a card on the board, under the goal's column logic: In progress while running, Review when passed or failed, Done when the goal is done. A goal picker that shows one goal shows its QA cards too. The card shows "QA round n", a chip (running, passed, failed with n bugs, error), the QA agent and the run's cost. Its modal shows the summary, the merged branches, the bugs linked to their sub-tasks (opening them), the integration branch and the agent. The goal card keeps its QA chip. A run's cost is its QA agent's usage. |
| 17 | Run info | A wider modal (min(1100 px, 96 vw); a full sheet on a phone) with a "Runs" section, one block per agent of the task: role and name, model, start and end time next to its cost, duration as working vs waiting (from the agent's reduced state events: working = time in working; waiting = idle, waiting_permission and waiting_background while it ran), branch and last commit (sha, subject, time), change summary (files changed, lines added and removed against the agent's base), session history (each session's start and end, resumed from, exit reason) and tokens (input, output, cache write, cache read). Served by `GET /api/tasks/{id}/runs`, computed on request. |
| 18 | Verification result | `report_done` gets an optional `verification` (at most 2000 characters): the checks the worker ran and their result. The worker prompt asks for it. Workers' reports (summary and verification) are stored in a new `task_reports` table and shown in the run's block as agent-written text. |
| 19 | Session exit reasons | `sessions.exit_reason`, written when a session ends. Older sessions show none. |
| 20 | Background wait state | A new state **waiting_background**, shown as "Waiting on background task". The Claude Code adapter adds one boolean to the reduced event: a Bash `PreToolUse` with `run_in_background: true` (no command text is kept). When a turn ends after such a start in the session, the engine checks the agent's process tree for descendants of the CLI other than agent-office's own (`mcp`, `hook`). On the host it reads the PTY child's descendants from `/proc`; in a container, the descendants of its init process (from `podman inspect`). If one runs, the state is waiting_background, rechecked every 5 seconds until it ends (then Idle). Any hook moves it on as usual. Codex agents are unchanged. The stall watchdog treats it as busy, not idle. |
| 21 | Migrations | One migration, `0019_game_factory.sql`: `daemon_settings`, `tasks.superseded_by`, `specs.change_request` and `change_requested_at`, `sessions.exit_reason`, `task_reports`, the `qa_runs.merged` JSON gets a `port` field (no schema change). |
| 22 | ADRs | 0060 the service unit; 0061 live caps and settings; 0062 the sub-task limit and superseding; 0063 follow-up bases and porting after a squash merge; 0064 the background wait state. ADR 0031 (plan changes, the Architect's exit), 0033 (QA cards) and 0047 (caps) are amended. |

## Sub-phases

1. Plan and roadmap (this file).
2. Migration 0019, queries, `make generate`.
3. Service command (`cmd/agent-office/service.go`), ADR 0060.
4. Caps: the daemon setting, live project settings, the effective cap in the UI. ADR 0061.
5. Sub-task limit, `max_sub_tasks`, superseding. ADR 0062.
6. Follow-up bases, the merged cut, porting, QA after a merge. ADR 0063.
7. Plans: request changes, lighter editing, the Architect's exit.
8. QA cards on the board.
9. Run info and the verification result.
10. The background wait state. ADR 0064.
11. The end-to-end follow-up after a squash merge, docs, PROGRESS, the full gates.

## Risks

- **Porting is new git logic on the merge path.** It is tested with real repositories: a squash, a
  follow-up on the old branch, a follow-up on the target, a conflicting port and a second merge. Ported
  commits only add branches; nothing the user's branches point to is rewritten.
- **Live settings** touch many reads of `e.opts`. The live part moves behind an atomic pointer, with
  `go test -race` over concurrent starts and changes.
- **Process-tree probing** depends on `/proc` and `podman inspect`. It runs only after a background Bash
  start was seen, and any failure keeps the state Idle, as today.
- **The service runs the real data dir by design.** Tests write units to a temp `XDG_CONFIG_HOME` with a
  fake systemctl; nothing in development installs a real unit.
- **The e2e spends tokens** (haiku): the follow-up after a squash merge is one more round in the workflow
  test.

## Answers to open questions

Every choice took the recommended option, as Ozan asked; they are the decisions above.

## Changes during the phase

(appended during the phase)
