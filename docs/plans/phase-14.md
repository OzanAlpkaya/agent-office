# Phase 14 plan: Multi-project office

- **Status:** Done on `phase-14/multi-project`; approved by Ozan and merged into `main` (2026-11-11). Written 2026-11-11 with the recommended option
  for every open question and executed straight away, as Ozan asked ("run it like the earlier
  phases: plan with the recommended options, no check-ins, one summary at the end"). Every decision
  is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 14. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites:** the `go` and `node` pack worker images (`make worker-image PACK=go`,
  `PACK=node`) for the sandboxed two-project e2e run, which spends a few cents of haiku tokens.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

One daemon runs several repositories at the same time. A project is a repository plus its own
settings (stack pack, default models, GitHub, budgets, an optional agent cap). Each project has its
own board, team, inbox, memory, usage and office floor; the user switches between them in the UI,
and everything that waits on the user is counted across all of them. One `--max-agents` cap holds
for the whole daemon, so spend and host load stay bounded by one number.

## What exists and what it means

- **The data model is already per project.** `projects` exists since migration 0001 and every
  table that matters (`tasks`, `agents`, `worktrees`, `messages` via agents, `pull_requests`,
  `inbox`, `memory`, `specs` and `qa_runs` via goals) carries a `project_id` or hangs off a row that
  does. Worktrees and sandbox workspaces are already under `<root>/<project slug>/<agent id>`.
- **The engine is one project.** `engine.New` registers `--repo` and keeps one `project`; every
  lookup by id that comes from the API or an MCP tool either goes through the engine's own agent map
  (which only holds its project's agents) or checks `ProjectID` (tasks, inbox, memory, pull
  requests, specs). Running several engines side by side therefore isolates projects by
  construction, as long as nothing daemon-wide is done by one of them.
- **Three things are daemon-wide today and would break a second engine:**
  `MarkUnfinishedAgentsExited` (all projects' agents), the orphan container cleanup in
  `reconcileSandbox` (every container of this daemon instance, so opening a second project would
  kill the first one's workers) and event retention (`DeleteEventsBefore`, harmless but repeated).
- **The cap is counted per engine** (`runningLocked()+starting` against `MaxAgents`, in `Start` and
  in relaunches/resumes).
- **Flags configure the single project:** `--pack`, `--secrets-dir`, `--egress-allow`, `--model`,
  `--role-model`, `--github*`, `--reviewer`, `--review-rounds`, `--budget-*`. `main` resolves them
  into `SandboxOptions` (image, allowlist, pack, secrets dir) and `GitHubOptions` (client, remote,
  base, reviewer) before the engine starts.
- **The API is unprefixed** (`/api/tasks`, `/api/events`, ...) and the UI keeps one world.
- **The control socket** (ADR 0039) already lets the CLI talk to the running daemon (`pair`).

## Out of scope

- Moving a project between data dirs, or several daemons sharing one data dir.
- Deleting a project's history, branches and worktrees on removal (removal hides it and keeps its
  data; "purge" is backlog).
- Cross-project agents or goals (an agent always belongs to exactly one project), shared memory
  between projects.
- Changing a project's settings while its agents run (the change is refused until they stop).
- Per-project sandbox mode: `--sandbox podman|host` stays daemon-wide, because host mode changes the
  daemon's security posture as a whole (ADR 0007).
- A daemon-wide budget in dollars across projects (per-project session and goal budgets only).

## Decisions (recommended options)

1. **One engine per project under a hub.** A new `engine.Hub` owns the store, the daemon-wide agent
   slots, the project registry and one `*engine.Engine` per open project. The engine keeps its code
   and becomes "the project engine"; daemon-wide work moves to the hub: marking the previous run's
   agents exited is per project, the orphan container cleanup runs once in the hub before any
   project opens, event retention runs once in the hub. Alternative: one engine with per-project
   maps everywhere; rejected as a rewrite of every lock and lookup with the isolation guarantees
   re-earned line by line. (ADR 0046)
2. **Project settings are overrides of the daemon's flags.** A project stores only what it sets
   (JSON in a new `projects.settings` column, strictly decoded and validated); everything else
   comes from the `serve` flags and their environment variables. So `serve --repo X --pack go`
   behaves exactly as before, and a second project can say `pack: node`. The UI shows effective
   values with where they come from. Fields: `pack` (`none` for no pack), `secrets_dir`,
   `egress_allow` (added to the pack's and the daemon's), `model`, `role_models` (merged over
   `--role-model`), `max_agents`, `github` (enabled, remote, reviewer, review rounds), budgets per
   session and per goal (micro-USD). Alternative: copy the flags into the project when it is
   created; rejected because a later flag change would silently not apply. (ADR 0046)
3. **Opening a project can fail on its own.** At start the hub opens every registered project; a
   project whose repository is gone, whose pack image is missing or whose GitHub check fails shows
   as `error` with the message and the fix, and the daemon keeps running the others. The `--repo`
   project is the exception and fails `serve` loudly, as today. A settings change reopens the
   project and is refused while any of its agents runs; a project in `error` can be retried.
4. **Removal hides, never deletes.** `project remove` is refused while the project's agents run; it
   sets `removed_at`, closes the engine and keeps tasks, history, branches, worktrees and usage.
   Adding the same repository again brings the project back with its history.
5. **`--max-agents` is daemon-wide; `max_agents` per project is optional and at most the daemon's.**
   A small slot pool in the hub counts each project's running and starting agents; the engine
   reserves under its own lock and the pool's (lock order engine → pool, never the reverse), so two
   projects cannot both take the last slot. The refusal names the daemon-wide cap and how many
   agents other projects run. The Tech Lead's prompt ("at most N agents") uses the project's
   effective cap. (ADR 0047)
6. **"One at a time" is per project.** One Tech Lead, architect, QA and assistant per project (ADR
   0017 amended). Agent names are unique per project (ADR 0043 amended): two offices may both have
   an Ada.
7. **API: project routes under `/api/projects/{project}/...`.** Every route that reads or changes
   project data moves under the prefix (agents, terminals, tasks, access requests, pull requests,
   inbox, specs, QA runs, memory, assistant, usage, status, events). Daemon-wide routes stay:
   health, auth, devices, roles, `/api/status` (sandbox mode, cap, warnings), `/api/usage` (totals
   per project and overall), `/api/projects` (list, add), `/api/projects/{project}` (get, change
   settings, remove, retry) and `/api/events`, which now streams the project list (`projects`
   snapshot, `project` and `project_removed` updates with each project's state and running count).
   Every project update carries its `project_id`. The old unprefixed project routes are removed:
   the UI is embedded in the same binary, and keeping aliases to a "default project" would keep the
   single-project assumption alive in scripts. The `{project}` value is the project id.
8. **CLI over the control socket.** `agent-office project list | add <repo> [flags] | set <project>
   [flags] | remove <project>` talks to the running daemon like `pair` (ADR 0039), so there is one
   writer of the database. `<project>` is the id or the name. `serve` without `--repo` starts with
   the registered projects (possibly none: the UI then asks for the first one).
9. **UI: one current project, the others watched lightly.** A project switcher in the header
   (name, running agents, how many agents wait on the user) chooses the current project; the
   choice is kept per browser, and `--repo`'s project is the default. The current project's world
   comes from its own events WebSocket exactly as today. Every other project gets a light
   subscription that keeps only what decides "waiting on the user" (agents, access requests, inbox,
   plans), so the Approvals badge and drawer count every project with the one `waitingOnUser`
   (ADR 0044); the drawer lists other projects' waits under their name with a switch button. A
   Projects panel adds, edits and removes projects. Usage shows the current project and an "All
   projects" table. (ADR 0048)
10. **The office has a floor per project.** An elevator panel lists the floors (one per project,
    with its waiting count); choosing a floor switches the current project and the office redraws
    that project's rooms and team. (ADR 0048)
11. **Isolation is tested at every boundary an agent has.** Engine tests start agents in two
    projects on one store and call every team MCP tool, over the real socket, as an agent of A with
    B's task ids, agent ids and names, memory ids and goal ids, and check that nothing of B is
    listed, changed or reached; a hook payload with B's session on A's socket is refused; API
    calls with one project's ids under the other's prefix are 404. `make sandbox-test` checks that
    a worker of A sees neither B's workspace nor B's main repository in its container. Host-mode
    agents run as the user and can read any file the user can, so for them isolation is the
    API/MCP level only; this is stated in the UI's host-mode warning and the threat model.

## Sub-phases

The phase is large, so it is split into sub-phases on the one branch, each ending with
`make check` green and its own commits.

### 14.1 Store and hub

- Migration 0013: `projects.settings` (JSON text, default `{}`), `projects.removed_at`; queries to
  list, re-add, change settings and remove projects; `MarkUnfinishedAgentsExited` scoped to a
  project.
- `engine.Hub`: `NewHub`, `Open`/`Add`/`Remove`/`Update`/`Retry`, `Projects()`, `Project(id)`,
  `Subscribe()` for project list changes, `Shutdown()`; the slot pool; orphan container cleanup and
  event retention moved here. `engine.Options` gets the hub's pool and the project row instead of
  `Repo`; the engine logger carries `project`.
- `serve` builds a hub, opens every registered project, then registers and selects `--repo` if
  given; `--repo` is optional.
- Tests: the cap across two projects (start and resume), a per-project cap, one goal per project,
  add/remove/re-add, a failing project next to a working one, restart of two projects.

### 14.2 Project settings

- `engine.ProjectSettings` (strict JSON, validated: pack names, egress names, models, budgets,
  rounds, reviewer, remote, cap) and their resolution over the daemon defaults into the engine's
  options: sandbox options per project (pack image, allowlist, secrets dir), GitHub options per
  project, models, roles with models, budgets, cap. The checks `main` does today (image exists,
  required secrets, GitHub repository readable) run per project when it opens.
- Tests: inheritance and overrides, validation messages, resolution with a pack, a missing image
  as a per-project error.

### 14.3 API, events, control socket, CLI

- Project-scoped routes and WebSocket, daemon routes, `project_id` on every update.
- Control socket project routes; `agent-office project list|add|set|remove`.
- Tests: every route still needs a device; a project route with another project's ids is 404; the
  events stream of one project carries none of another's; CLI parsing and output.

### 14.4 Isolation

- Engine tests over the real MCP and hook sockets for every team tool (see decision 11).
- `make sandbox-test`: a worker of A cannot see B's workspace or repository.

### 14.5 UI

- Per-project API client, the world reducer extracted and shared by the full and the light
  subscriptions, the project switcher, the Projects panel (add, settings, remove), Approvals across
  projects, Usage per project and overall, the office's floors.
- Vitest for the reducer, the cross-project waiting list and the settings form; Playwright against
  a mocked daemon with two projects: switching, the badge counting both, the drawer's switch
  button, adding and removing a project, the office's floors, usage totals.

### 14.6 End to end and docs

- `TestMultiProjectEndToEnd` (sandboxed, haiku): a throwaway clone of `AO_E2E_REPO` with the `go`
  pack and a generated small Node repository with the `node` pack; a Tech Lead and a worker in each
  at the same time, with `--max-agents 4` so a fifth start in either project is refused; both
  goals reported; then SIGKILL, restart, and both projects' interrupted agents are restored.
- The existing e2e runs move to the project routes and keep passing.
- ADRs 0046 (projects and the hub), 0047 (the daemon-wide cap), 0048 (the UI across projects);
  ADRs 0017 and 0043 amended; `docs/architecture.md` (components, domain model, threat model),
  README, `PROGRESS.md`.

## Risks

- **Lock order between engines and the pool.** Only engine → pool is allowed; the pool never calls
  into an engine. `go test -race` and a test that starts in two projects at once from many
  goroutines.
- **Isolation regressions** from a by-id query without a project check. Mitigated by the tool-by-
  tool tests and by keeping lookups inside the project's engine.
- **The UI refactor** touches every API call. Mitigated by the per-project client being the only
  way to call project routes (no unprefixed helpers left) and by the Playwright suite.
- **Opening old projects after the upgrade.** Every project the data dir ever saw (`serve --repo`
  registered each one) opens at start. One whose repository is gone shows as `error` with a Remove
  button instead of stopping the daemon.
- **e2e cost and time:** two sandboxed teams at once; haiku keeps it to a few cents.

## Answers to open questions

All taken with the recommended option, as asked; listed again in the end-of-phase summary.

| Question | Answer |
|---|---|
| One engine with per-project maps, or one engine per project? | One engine per project under a hub (decision 1). |
| Do flags still mean anything with several projects? | They are the daemon defaults; projects override (decision 2). |
| Where do project settings live? | A JSON column in `projects`, strictly decoded (decision 2). |
| What does remove do with the data? | Keeps it; re-adding the repository brings it back (decision 4). |
| Settings change while agents run? | Refused until they stop (decision 3). |
| Per-project secrets? | `secrets_dir` per project, default the daemon's `--secrets-dir`. |
| Per-project sandbox mode? | No; daemon-wide (out of scope). |
| Keep the unprefixed API? | No (decision 7). |
| Names unique across projects? | Per project (decision 6). |
| CLI with the daemon stopped? | No; it talks to the running daemon (decision 8). |

## Changes during the phase

- **Task lookup before the cap.** `Start` now looks up the task before reserving a slot, so a task
  id of another project is "not found" whatever the cap (found by `TestProjectRoutesAreIsolated`).
- **Retention per project.** Event retention became a per-project query run by each engine
  instead of a hub loop: simpler, and a removed project's data stays as removal promises.
- **No shared world reducer.** The full world (`useWorld`) kept its own state; the light watchers
  have a small pure reducer of their own (`reduceLight`) over the same parsed messages, and both
  feed the one `waitingOnUser`. Extracting the full reducer would have touched every update path of
  the main view for no behaviour change.
- **The title** keeps "(n) agent-office" with one project and adds the project's name with several,
  so single-project use looks exactly as before.
- **e2e:** the single-project runs reach their project through the daemon's default project
  (`projectPath`); the first full run's `TestEndToEnd` failed its cleanup check because the
  playground repository held five stale worktree entries from an earlier session (2026-11-07),
  which the daemon's start-up `git worktree prune` (unchanged behaviour) removed; the rerun passed.
- **Independent code review** (5 fixes): an engine's shutdown now ends its event subscriptions (the
  UI froze on a project's old engine after a settings change); new settings are checked before the
  project stops and a failed reopen restores the old ones; a failed removal no longer leaves a
  closed engine registered; open agent tabs are kept per project; the UI opens the daemon's default
  project first. Plus: `project set --inherit` takes the API names and refuses a flag for the same
  setting; a light watcher stops on sign-out.
- **Independent security review** (no cross-project reach found for sandboxed agents; 4 fixes):
  adding a repository and changing pack, `secrets_dir`, `egress_allow` or GitHub settings need this
  machine (remote devices can still rename, change models, caps and budgets, retry and remove);
  `secrets_dir` must be the daemon's secrets dir or inside it; agents' workspaces and linked
  worktrees cannot be registered; settings are logged on every add and change; host mode warns that
  isolation between projects holds for sandboxed agents only. Backlog: purge, project checks in the
  queries themselves, a GitHub token per project, host paths in errors to remote devices.
- **Ozan's review (2026-11-11)**: all 11 decisions approved; tested by hand in a real browser on the
  real daemon (the playground with the go pack and a Node repository with the node pack, a Tech
  Lead team in each at once, approvals across projects, a settings change refused while agents
  ran, SIGKILL and restore; the phone was not tested). Fixed before the merge, each with a
  Playwright test: choosing another floor jumped from the Office back to the Board (the tab is now
  kept across every project switch); the Projects panel showed a refused Save in the sidebar (now
  next to the control that asked); its "agents run across all projects" count came from the status
  fetched at page load (now from the live worlds of every project). The pre-seeded sandbox config
  was re-checked against Claude Code 2.1.322 with Spike F's interactive harness: no dialog with the
  seed, and each key left out brings back exactly its dialog, as with 2.1.319; the tested version
  is now 2.1.322.
