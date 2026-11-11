# 0046. Projects: one engine per project under a hub

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Until Phase 13 a daemon ran one repository: `serve --repo` registered it, and the engine held one
`project`. Phase 14 asks for several repositories at once, each with its own stack pack, models,
GitHub settings and budgets, its own board, inbox, memory and office floor, and an agent of one
project must not reach another's tasks, messages, worktrees or memory.

The data model was already per project: `projects` exists since migration 0001, every table that
matters carries a `project_id` or hangs off a row that does, and worktrees and workspaces live under
`<root>/<project slug>/<agent id>`. Inside the engine, lookups by id from the API or a team tool go
through its own agent map or check the task's, inbox item's, memory entry's or pull request's
project. Three things were daemon-wide and would break a second engine: marking the previous run's
agents exited (every project's), the orphan container cleanup (every container of this daemon
instance, so opening a second project would kill the first one's workers) and event retention.

## Options considered

1. **One engine with per-project maps.** Every map, lock and lookup gains a project key. Pros: one
   object. Cons: a rewrite of the engine's locking and of every lookup, with the isolation that the
   single-project engine gets by construction re-earned line by line.
2. **One engine per project under a hub.** The engine stays the project engine; a hub owns the
   store, the registry, the daemon-wide agent cap (ADR 0047) and the engines. Pros: isolation by
   construction (an agent's sockets belong to its project's engine, which only sees its project);
   the engine's code and tests stay. Cons: daemon-wide work must move to the hub, and a project
   engine must never do it.
3. **One daemon per project.** Pros: total isolation. Cons: no daemon-wide cap, one UI per project,
   several ports and data dirs; the opposite of the goal.

How projects are configured:

1. **Copy the flags into each project when it is created.** What the UI shows is what it uses, but
   a later flag change silently does not apply to existing projects.
2. **Project settings override the daemon's flags.** The flags (and their `AGENT_OFFICE_*`
   variables) are the daemon's defaults; a project stores only what it sets.

## Decision

Option 2 for both.

- `engine.Hub` opens every registered project at start (in parallel; one that cannot open, e.g.
  its repository is gone or its pack image is missing, stays in state `error` with the reason and
  the daemon keeps running the others), adds, renames, reconfigures, retries and removes projects,
  and streams the project list. Project operations are serialized; reads are not.
- The project engine scopes restart reconciliation (`MarkUnfinishedAgentsExited` takes a project)
  and event retention to its project; the orphan container cleanup runs once in the hub before any
  project opens. A standalone `engine.New` (tests, embedders) still registers its repository and
  does the cleanup itself.
- **Settings** (`projects.settings`, migration 0013) are JSON, decoded strictly (an unknown field is
  an error, so a typo never silently inherits) and validated with every problem at once: `pack`
  (`none` for no pack), `secrets_dir`, `egress_allow` (added to the pack's names and
  `--egress-allow`), `model`, `role_models` (merged over `--role-model`), `max_agents` (at most the
  daemon's cap), `github`, `github_remote`, `reviewer`, `review_rounds`, and the session and goal
  budgets in micro-USD. Precedence for models stays as in ADR 0029: a role's model (from the role
  file, `--role-model` or the project's `role_models`) wins over the project's `model`, which wins
  over `--model`. `--sandbox podman|host` stays daemon-wide. Opening a project resolves what needs
  the outside world: its pack's image (which must be built), allowlist and secrets, and its GitHub
  client.
- **Changing settings reopens the project** and is refused while any of its agents runs or starts
  (`closeIdle` checks and closes under the engine lock, so no start slips in between); a rename
  needs no reopen. **Removing** is refused the same way; it sets `removed_at`, closes the engine and
  keeps tasks, history, branches, worktrees and usage. Adding the same repository again brings the
  project back with its history (and with the settings the add gives, if any).
- `serve` works without `--repo`; `--repo` registers its repository if needed, opens it and makes
  it the default the UI opens first, and unlike other projects a failure stops `serve`, as before.
- **API:** every route that reads or changes project data is under `/api/projects/{project}/`,
  resolved to that project's engine before the handler runs (unknown: 404; could not open: 409).
  Every engine update carries its `project_id`. Daemon routes: `/api/status` (sandbox mode, cap,
  defaults), `/api/usage` (totals per project and overall), `/api/projects` and
  `/api/projects/{project}` (list, add, change, retry, remove) and `/api/events`, which now streams
  the project list. The unprefixed project routes are removed rather than aliased to a default
  project: the UI ships in the same binary, and an alias would keep the single-project assumption
  alive in scripts.
- **CLI:** `agent-office project list | add <repo> [flags] | set <project> [flags] | remove
  <project> | retry <project>` talks to the running daemon over its control socket (ADR 0039), so
  the daemon stays the database's one writer. `<project>` is an id or a unique name; `set --inherit
  pack,model` drops overrides.
- A task id of another project is now looked up before the cap check in `Start`, so it is "not
  found" whatever the cap.

## Consequences

- Isolation comes from structure: an agent's hook, MCP, API and secrets sockets are bound to its
  project's engine. It is tested at every boundary: every team tool over a real MCP socket with
  another project's ids (`TestAgentsCannotReachAnotherProject`), another project's session on a
  hook socket, the API with another project's ids under the wrong prefix
  (`TestProjectRoutesAreIsolated`), and a sandboxed worker's mounts and visible paths
  (`TestSandboxedProjectsAreIsolated`).
- Host-mode agents run as the user and can read any file the user can, including another project's
  repository; for them isolation is at the API and MCP level only. The host-mode warning says so.
- After the upgrade every repository the data dir ever saw (each `serve --repo` registered one)
  opens at start; one that is gone shows as `error` with Remove and Retry instead of stopping the
  daemon.
- The memory export's header uses the project's name from when its engine opened; a rename shows
  there after the next reopen.
- Purging a removed project's data is not offered yet (backlog).
