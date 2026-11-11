# Phase 17 plan: Daily use

- **Status:** Done; reviewed and approved by Ozan, merged into `main` (2026-11-12). Written 2026-11-11 with the recommended option for
  every choice, as Ozan asked ("run it like the earlier phases ... with the recommended options, no
  check-ins, one summary at the end").
- **Roadmap:** [docs/plan.md](../plan.md), Phase 17. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Source:** Ozan's use of agent-office on PulseGo Sprints 3–6.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

The things a user does every day once a goal reaches review take one click: finding the goal that
is running, closing a finished one, merging its tested work. Finished goals stay readable in an
archive. What the UI shows as a warning is something to act on, not Claude Code's own housekeeping.

## What exists and what it means

- **The Board** shows every task of the project in five columns: after a few sprints, most cards are
  done goals and their sub-tasks. Goals are top-level tasks; sub-tasks have `parent_task_id`.
- **Marking a goal Done** (`UpdateTask`) ends its Tech Lead, architect and QA (`ExitGoalDone`); its
  sub-tasks keep their status (mostly `review`) and its workers are left alone. Removing exited
  agents is one click per agent; removal hides the agent (`removed_at`) and keeps its branch and, when
  dirty, its workspace.
- **Usage** covers every session of the project, removed agents included, keyed by task and goal.
- **The Tech Lead's report** (`report_done` with a summary) moves the goal to review; the summary is
  only logged (its length), not stored. There is no commit message.
- **QA** (ADR 0033) merges the sub-task branches into its own integration branch, which is imported
  into the main repository on `report_qa`; `qa_runs.head` is the commit that passed.
- **Resume** (ADR 0035) continues the latest session, but skips a resumed session in which no turn
  finished (`sessions.turns = 0`) for the session it resumed. That rule exists because Claude Code
  writes a fork's transcript only once the fork gets a message, so a fork that was only shown has
  none. A fork that got a prompt and was killed before its turn finished is skipped as well, which is
  the backlog item.
- **Access grants** (ADR 0053) are rows in `access_grants`, loaded into every launch and added to
  running sessions on approval; nothing removes them.
- **Sandbox events**: the API socket forwards only `POST /v1/messages` (everything else is an
  `api_rejected` event and a warning in the UI); the egress proxy records every refused CONNECT as
  `egress_denied`. Workers run with `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` and
  `DISABLE_AUTOUPDATER=1`, yet Ozan still sees such events from Claude Code itself.
- **Pids**: `--sandbox-pids` (default 256) for every worker; memory already has `resources.memory`
  (pack) and `worker_memory` (project).

## Decisions (recommended options)

| # | Question | Decision |
|---|---|---|
| 1 | What is an active goal? | A top-level task that is not `done`. The Board shows active goals and their sub-tasks (any status); a goal picker above the columns narrows it to one goal ("All active goals" by default, remembered per project in the browser). Plain top-level tasks count as goals. |
| 2 | Where is the archive? | A third mode of the Board, "Archive (n)", next to the picker: done goals, newest first, with their date, sub-task count, cost and last QA verdict. A goal opens into a detail view with its description, the Tech Lead's report (summary and commit message), its sub-tasks with status and branch, every agent that worked on it (removed ones too: name, role, CLI, model, how it ended), its cost (the existing usage, by goal), its QA runs, and its merge (commit, target, pushed or not). A done goal can be reopened by changing its status, as today. Nothing is deleted. |
| 3 | Where does the archive's data come from? | One API call per opened goal, `GET /tasks/{id}/record`, built from the store (agents including removed ones, QA runs, the goal's report and merge). The list itself uses data the UI already has (tasks, usage, QA runs). |
| 4 | What does marking a goal Done do? | In one transaction, every sub-task that is not done becomes done; the goal's running agents end, workers included (their uncommitted work is saved on exit, ADR 0051). The UI asks first when agents still run ("2 agents still running will be stopped; their work is saved"). |
| 5 | How are exited agents removed in one action? | "Remove n exited agents" on a done goal's card and in the archive: `POST /tasks/{id}/remove-agents` removes every exited agent of the goal and its sub-tasks with the normal, work-preserving removal (dirty workspaces kept, branches with commits kept), one by one, and returns what was removed and what was kept and why. Running agents are not touched. |
| 6 | Mid-turn resume | Measure Claude Code 2.1.322 first (a fork given a prompt, killed mid-turn, then `--resume` of the fork). If its transcript holds the prompt and partial turn and resumes, sessions record whether they got a prompt (`sessions.prompted`, set on `UserPromptSubmit` and by a launch prompt) and the skip applies only to forks that neither got a prompt nor finished a turn. If not, ADR 0035 says precisely why. ADR 0035 is amended either way. |
| 7 | Revoking grants | The access panel lists the project's grants (host, scope: project or the agent's name, who approved it, when) with a Revoke button: `DELETE /access-grants/{id}`. Revoking removes the row and recomputes the grants of every running session it covered from the remaining rows, so a host still covered by another grant stays allowed. Agent-scoped grants can be revoked the same way. Revoking widens nothing, so remote devices may do it. The agent is not told. |
| 8 | Expected background traffic | Measure what a sandboxed Claude Code 2.1.322 sends besides `POST /v1/messages`. Requests it makes on its own (count_tokens, update and settings checks and the like) are classified as expected: a known list in the sandbox package, by API path and by egress host. They are still refused as before (nothing new is forwarded or tunnelled) but logged at debug level, counted per agent and shown as a quiet "n background requests refused (expected)" line instead of warnings. Anything not on the list stays a warning as today. |
| 9 | Pids limit per pack and project | `resources.pids` in pack.yaml and `worker_pids` in project settings (64 to 65536); a project's value wins over its pack's, which wins over `--sandbox-pids`, like memory (ADR 0051). The status and the settings form show it. A worker that hits it (fork failures) is told the limit and where to raise it when the daemon can tell (`pids.events` `max` of the worker's cgroup, polled like `memory.events`). |
| 10 | Which branch is merged, and when | The head of the goal's latest passed QA run (`qa_runs.head`), only while the goal is in review and QA's gate holds (QA passed on the current work, no open bug sub-tasks), the same check as the Tech Lead's report. |
| 11 | Commit message | `report_done` gains `commit_message` (Tech Lead only, 1-4000 characters): the squash commit's message, stored with the summary in a new `goal_reports` table. Without one, the goal's title and the summary are used. The confirmation shows the message in an editable field, prefilled; what the user confirms is what is committed. |
| 12 | Target branch and remote | Project setting `merge_target` (default `main`); the Push button uses the project's `github_remote` (default `origin`). Both are shown in the confirmation. |
| 13 | How the squash merge is made | Without touching any working tree first: `git merge-tree --write-tree` of the target head and the QA head (a conflict refuses, naming the files), then `commit-tree` with the target as the only parent, the user's git identity, the confirmed message and an `Agent-Office-Goal: <id>` trailer. If the target is checked out in the main repository, the working tree must have no tracked changes (untracked files are fine; git refuses itself if one would be overwritten) and the branch moves with `git merge --ff-only`, which updates the files; otherwise with a compare-and-swap `update-ref`. A target checked out in another worktree is refused. |
| 14 | "A target that moved" | The confirmation carries the target head and the QA head it showed; the merge is refused when either changed since ("main moved since the confirmation: review it again"). If the target moved since QA tested (QA's base is not the target head), the confirmation says so ("main has 3 commits QA did not test with") and the merge is still allowed when it is clean: a conflict is refused, as asked. |
| 15 | Push | `git push <remote> refs/heads/<target>:refs/heads/<target>`: never forced, no tags, no terminal (ADR 0020). A rejected push (the remote moved) is shown with git's reason. Shown after a merge of that goal until it is pushed; the confirmation names the remote, its URL and the commit. |
| 16 | Who may merge and push | Only the user, from the UI or API, after the confirmation; no team tool exists for it and the Tech Lead's prompt keeps "ask the user for merges and pushes". Any paired device may (it is the user acting); every merge and push is logged and recorded in `goal_merges`. |

## Sub-phases

1. **Schema** (migration 0016): `goal_reports`, `goal_merges`, `sessions.prompted`.
2. **Closing a goal**: sub-tasks done, every agent ended, "remove exited agents".
3. **Goal view and archive**: the picker, the archive list, `GET /tasks/{id}/record` and its view.
4. **Mid-turn resume**: the spike, then the fix or the documented reason; ADR 0035 amended.
5. **Revoking grants**: API, recomputed grants, UI.
6. **Expected background traffic**: measurement, the known list, quiet display.
7. **Pids per pack and project.**
8. **Merge and push**: `report_done`'s commit message, git functions, engine, API, UI, and an end-to-end
   test on a throwaway repository with a local bare remote.
9. ADRs, docs, PROGRESS, the full gates.

## Risks

- **Merging writes the user's main branch** and pushing publishes it; both stay user-only, behind a
  confirmation, refused unless the state is exactly what was shown.
- **Updating a checked-out branch** must also update its files; `git merge --ff-only` does both, and
  refuses rather than overwriting.
- **The mid-turn resume depends on undocumented Claude Code behaviour**; pinned by a spike, the fake
  CLI and the e2e.
- **Treating requests as expected could hide real problems**; the list is narrow (exact paths and
  hosts seen from Claude Code), the requests are still refused, and counted.

## Answers to open questions

Every choice took the recommended option, as Ozan asked; they are the decisions above.

## Changes during the phase

- **Mid-turn resume is fixed, not documented away.** Measured first (Claude Code 2.1.322, haiku, print
  mode, a scratch repository): a fork killed during a tool call, and one killed right after its prompt
  was recorded and before any answer, both have a transcript and `--resume` continues them (they knew
  the codewords given in the fork and what they were doing). So decision 6 took its first branch:
  `sessions.prompted` (set on the main agent's first `UserPromptSubmit`); ADR 0035 amended.
  `TestResumeContinuesInterruptedSession` fails without the change.
- **Background traffic is prevented, not only classified.** Measured with a scratch daemon and the
  egress proxy logging refused CONNECTs' headers: a sandboxed 2.1.322 worker sent
  `downloads.claude.ai` and a `git` clone of its plugin marketplace from `github.com` at every start,
  despite `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`. The binary has
  `CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL`; with it, two runs (one with a subagent)
  refused nothing, and a control run without it brought both back. Workers now get it. No
  `count_tokens` call was seen; it stays on the known list from Ozan's report. The known list is exact
  (`downloads.claude.ai:443`, `POST /v1/messages/count_tokens`); `github.com` is not on it, since an
  agent's own `git` or `curl` must stay a warning (they send the same `User-Agent`). ADR 0016 amended.
- **Pids:** the watch reads the worker cgroup's `pids.events` (`max`), which works here because
  systemd delegates the pids controller to the user (`cgroup.subtree_control` lists it);
  `TestSandboxedPidsLimitIsReported` with real Podman.
- **Closing a goal asks only about busy agents.** Decision 4 asked "when agents still run"; the
  Playwright test showed that a reported goal's Tech Lead is always running (idle), so every Done would
  have asked. The confirmation counts agents that are working, starting or waiting on a permission.
- **Remove exited agents, Merge and Push are in the archive's record too.** Done goals leave the board
  (decision 1), so the button decision 5 put "on a done goal's card" could never show; it lives in the
  record view, which also offers Merge and Push (a goal marked done before it was merged or pushed).
- **The grants list** sits in the sandbox panel (the "pack" badge), next to the network allowlist.
- **Found by the Playwright tests:** the goal card counted only its own task's agents (the board
  passes each card its task's agents), so a goal's workers were missed; fixed before the commit.
- **Independent reviews.** A security review led to 4 changes: `merge_target` is accepted from this
  machine only (it chooses what Merge and Push write, like `github_remote`); the confirmation lists the
  changed paths and flags CI, hook and agent-config files (`.github/`, `.husky/`, `.claude/`,
  `CLAUDE.md`, ...); Push sends exactly the confirmed merge commit (`<sha>:refs/heads/<target>`), not
  later local commits; credentials in an http(s) remote URL are not shown. Recorded, not changed: in
  host mode an agent can write the main repository's git config and hooks, which the user's Merge or
  Push then runs (host agents already run as the user; refusing `core.hooksPath` would block
  repositories that use it); `worker_pids`, like `worker_memory`, may be changed from a remote device
  (a resource limit, not access). A code review led to 5 fixes: `endGoal` read the database under the
  engine lock and skipped an agent silently on an error; a failed merge record could strand a merge
  (retried, logged with the commits); the push held the merge lock over the network; a failed grant
  reload at launch kept the earlier grants (now none); the archive record did not reload after a merge.
  Recorded: a checkout of the target in the instant between the worktree check and the
  compare-and-swap is not detected; a done goal whose closed sub-task's branch moved after QA cannot be
  merged without a new QA run.
- **Found by the gate runs:** the first full `make e2e` failed once in `TestAutonomousTeamEndToEnd` (Phase 16):
  its second induced OOM kill, 2 s after the automatic resume, was recorded as a plain SIGKILL. No warning in
  the log, so sandbox-init's exit report was missing or had no OOM kill; the cause is not known (one
  hypothesis: the kernel killed sandbox-init in the fresh container). It passed alone right after and in the
  second full run; this phase did not touch that path. `TestSandboxedTeamWorker` read the lead's messages
  between the status change and the message once; the test now waits for the message.
