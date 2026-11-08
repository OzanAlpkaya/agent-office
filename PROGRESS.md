# Progress

**Current phase:** Phase 9 (Memory & persistence) done on `phase-9/memory-persistence`, waiting for review; next: Phase 10

**Phase order from here on** (decided by Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13. Phase numbers stay
as they are, so ADRs and plans that refer to them stay valid.

See [docs/plan.md](docs/plan.md) for goals and Definitions of Done.

## Phases

- [x] Phase 0: Research & design
- [x] Phase 1: Single agent (PTY, live terminal, Claude Code hooks)
- [x] Phase 2: Multi-agent + task board (SQLite, worktrees)
- [x] Phase 3: Isolation & security (rootless Podman, egress allowlist, secrets)
- [x] Phase 4: Team MCP server + lead agent
- [x] Phase 5: GitHub loop + pluggable code review
- [x] Phase 6: Stack packs
- [x] Phase 7: Pixel office UI
- [x] Phase 8: Observability & cost
- [x] Phase 9: Memory & persistence
- [ ] Phase 10: Remote access (PWA over Tailscale)
- [ ] Phase 11: Portfolio polish
- [x] Phase 12: Team workflow and roles
- [ ] Phase 13: Office v2

## Log

- 2026-11-06: Project foundation: repo layout, project rules, roadmap, ADR 0001 (Go for the daemon).
- 2026-11-06: Phase 0 done on `phase-0/research-design`. Spikes: PTY/WebSocket round-trip,
  Claude Code hook capture (print, Stop injection, interactive), session-file structure, Podman vs
  Claude Code sandbox, unix socket into a network-less container. Docs: verified library API notes,
  Codex research, architecture + threat model, ADRs 0002–0007. Phase 1 DoD refined.
- 2026-11-06: Phase 0 merged into `main` (`--no-ff`). Playground repo created at
  `~/projects/agent-office-playground`.
- 2026-11-06: Phase 1 done on `phase-1/single-agent`. Packages `terminal`, `agent` (Claude Code
  adapter), `hooks` (state machine + per-agent socket), `engine`, `api`; `serve`/`hook` commands;
  React UI embedded in the binary; Makefile. End-to-end with haiku in the playground: approve,
  deny, interrupt, re-attach, stop. Findings: no hook on interrupt or dismissed permission
  (fixed via input-aware state); trust dialog for new folders, inherited by worktrees.
- 2026-11-07: Phase 1 merged into `main` (`--no-ff`); ADR 0008 (interrupt inference).
- 2026-11-07: Phase 2 done on `phase-2/multi-agent`. ADRs 0009 (git via CLI), 0010 (worktree
  layout), 0011 (data model and restart). Packages `git` and `store` (SQLite, goose, sqlc); engine
  runs up to `--max-agents` agents in worktrees under the XDG data dir, with tasks, persistence,
  restart reconciliation and work-preserving removal. Task board UI. Opt-in `make e2e` passes
  twice against the playground, including daemon SIGKILL and restart.
- 2026-11-07: Phase 2 review: task prompt now forbids pushing and touching other branches (push and
  PRs are the daemon's job in Phase 5); ADR 0012 (forward-only migrations). Merged into `main`
  (`--no-ff`).
- 2026-11-07: Phase 3 done on `phase-3/isolation`. ADRs 0013 (schema version guard, closes the
  ADR 0012 gap), 0014 (git in the sandbox: shared clones owned by a subordinate uid, bundle
  import, imports never move a branch backwards), 0015 (OAuth token file), 0016 (`--network none`,
  per-agent egress allowlist and credential-injecting API socket); ADR 0007 implemented. Spike F
  (Claude Code in a network-less worker). Workers run in rootless Podman with permission prompts
  off only inside the container; host mode stays as a labelled, prompted option. Import now, save
  work, restart reconciliation, sandbox events in the UI. Migrations 0002 and 0003.
  `make sandbox-test` covers the isolation and token DoD; `make e2e` passes in host and sandbox
  mode, including a daemon SIGKILL. CLAUDE.md: approved phase plans run without per-step check-ins.
- 2026-11-07: Phase 3 review: all decisions approved, including the plan.md scope update. Approved
  phase plans are now saved under `docs/plans/` (`phase-3.md` first). Merged into `main` (`--no-ff`).
- 2026-11-07: Phase 4 done on `phase-4/team-mcp`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-4.md`). ADR 0006
  accepted (go-sdk `IOTransport` over a per-agent `mcp.sock`; `agent-office mcp` stdio bridge);
  ADRs 0017 (roles and authority), 0018 (message delivery), 0019 (access requests). Migration
  0004 (roles, sub-tasks, messages, access requests, `imports` rebuilt for `report_done`). New
  dependency: `modelcontextprotocol/go-sdk` v1.8.0. A lead plans a goal through six team tools;
  workers report with committed, imported work and end; messages go by Stop hook or typed into an
  idle terminal; access requests wait in a UI approval queue and grant per agent. `make check`,
  `make sandbox-test` and `make e2e` (host, sandbox and the new team run) pass. An independent
  review found no authorization hole; its six delivery/report fixes are in.
- 2026-11-07: Phase 4 review: all decisions approved. Merged into `main` (`--no-ff`).
- 2026-11-07: Phase 5 done on `phase-5/github-review`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-5.md`). ADRs 0020 (GitHub
  integration: token file, daemon pushes over SSH, polling), 0021 (pluggable review: `Reviewer`
  interface, internal reviewer agent, CodeRabbit), 0022 (follow-ups relaunch the original worker).
  Migration 0005 (reviewer role, `pull_requests`, `reviews`). New packages `github` (+ `githubtest`
  fake), `review`, `secret`. New dependency: `google/go-github/v93` v93.0.0. `--github` (opt-in,
  sandboxed agents only): a worker's `report_done` pushes its branch and opens or updates its PR;
  the internal reviewer posts a COMMENT review; changes requested relaunch the original worker,
  whose new commit updates the same PR; a human merge moves the task to done. An independent review
  of the diff led to 14 fixes (host mode refused, `.github/` gate, review cap, superseded reviews,
  races). The real e2e run changed the follow-up path from a team message to a relaunch.
- 2026-11-07: Phase 5 review: all decisions approved. Ozan narrowed the GitHub token to Contents
  read-only. Closing keywords in agents' commit messages added to Phase 11 as a later item. Merged
  into `main` (`--no-ff`).
- 2026-11-07: Phase 6 done on `phase-6/stack-packs`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-6.md`). ADR 0023 (stack
  packs). New package `pack`; new dependency `go.yaml.in/yaml/v3` v3.0.5. Packs (strict YAML,
  every problem reported with its fix) come from the binary (`go`, `node`) or a user-owned
  directory, never the repository; `--pack` picks one per run. The worker image is now a base
  image without a toolchain (the default); pack images build `FROM` it (`worker-image --pack`).
  A pack's hosts replace the default allowlist; its secrets are user-only files served once over
  a per-agent socket to `sandbox-init` (workers only); its skills are mounted read-only; its MCP
  servers join the agent's MCP config. `pack list|check` commands and a UI pack panel. An
  independent review led to 9 fixes (secrets for workers only, read-only skills, ENTRYPOINT,
  exec bits, image tag inputs, reserved names). Two pre-existing tests that failed without
  `-race` fixed. `make check`, `make sandbox-test` and `make e2e` pass (the GitHub run on a
  rerun: haiku once reported a follow-up without committing).
- 2026-11-07: Phase 6 review: all decisions approved. Added before the merge, as Ozan asked: in a
  review follow-up round `report_done` needs a new commit, and `report_no_change(reason)` answers
  without one, posting the reason on the PR (ADR 0022 amended, migration 0006); the sandbox e2e
  checks with `/skills` that Claude Code lists the pack's skill. `make check`, `make sandbox-test`
  and the sandbox and GitHub e2e runs pass. Merged into `main` (`--no-ff`).
- 2026-11-08: Phase 7 done on `phase-7/pixel-office`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-7.md`). ADRs 0024
  (office view: lazy-loaded PixiJS scene, art drawn in code, HTML nameplates) and 0025 (UI tests:
  Vitest in `make check`, Playwright against a mocked daemon in the opt-in `make ui-test`). An
  Office tab shows every agent at a desk with a pose for its state and role, the lead's
  whiteboard with its sub-tasks, and messages flying between desks; a click opens the agent's
  terminal. No daemon changes. New dependencies: `pixi.js` 8.22.0; dev: `vitest` 5.1.0,
  `@playwright/test` 1.65.0, `@types/node` 26.6.6. An independent review led to render-on-demand,
  scene-side frame statistics, desks kept across tab switches and stronger tests, which found a
  lost first click (fixed). Live check with a real haiku team in sandbox mode.
- 2026-11-08: Phase 7 review: approved. Screenshots kept in `docs/screenshots/` (two in the README,
  whose status line was still at Phase 2). Ozan tested the system end to end by hand (single agent,
  team run, access request deny/approve, interrupt, Import now, Save work, remove): everything
  worked. His findings are backlog items in `docs/plan.md`: hide or explain Claude Code's resume
  line (Phase 8, real fix in Phase 9), terminal copy/paste (Phase 10), `python3` in the worker
  image (Phase 11), and two new phases appended so existing phase numbers stay valid: Phase 12
  Team workflow (the lead stays until the goal is done, conflict detection and sequential
  sub-tasks, messages to the user) and Phase 13 Office v2 (walking characters, rooms). Merged into
  `main` (`--no-ff`).
- 2026-11-08: Phase order from here on set by Ozan: 8, 12, 9, 10, 11, 13. Phase 8 plan written with
  the recommended options and started on `phase-8/observability-cost` (`docs/plans/phase-8.md`).
- 2026-11-08: Phase 8 done on `phase-8/observability-cost`; plan written with the recommended options
  and executed without a separate approval, as Ozan asked (`docs/plans/phase-8.md`). ADRs 0026
  (usage and cost), 0027 (budgets), 0028 (correlation ids, log format, retention). New package
  `usage`; migration 0007. No new dependencies. Sandboxed agents are metered on their API socket,
  host-mode agents read from their transcripts; one row per API message, priced on read from a
  built-in table (pricing page, 2026-11-08) plus an optional user `prices.yaml`; money is integer
  (nano-USD inside, micro-USD in the API). A Usage tab (by goal, task, agent, session, model) and
  usage on agent and task cards. Optional budgets per session and per goal, refused on the API
  socket. Every log line about an agent carries its session id (`--log-format json`); events older
  than 30 days are pruned; the newest 3 migration backups are kept. Backlog: exited agents'
  terminals explain the "Resume this session" line (Phase 9 makes it work). The real e2e run found
  that Claude Code writes a turn's last transcript line after its Stop hook (fixed with re-reads);
  an independent review led to 5 fixes (budgets fail closed on write errors, no SQLite write on the
  stream path, saturating money math, ...). A pre-existing two-step write in review follow-ups
  (`setFollowup`) is now one transaction. `make check`, `make ui-test`, `make sandbox-test` and
  `make e2e` (host, sandbox with a real budget refusal, team) pass.
- 2026-11-08: Phase 8 review: all decisions approved. The live Usage tab screenshot is in
  `docs/screenshots/usage.png`. Backlog in `docs/plan.md`: the intermittent `TestTeamAuthorization`
  failure under load (Phase 12) and the "user settings" label flipping on sandboxed agents after a
  restart (Phase 9). Merged into `main` (`--no-ff`).
- 2026-11-08: Phase 12 expanded by Ozan into "Team workflow and roles" (roles as data, Architect,
  Tech Lead, QA, Assistant, escalation chain, model per role, conflict handling). Plan written with
  the recommended options and started on `phase-12/team-workflow` (`docs/plans/phase-12.md`).
- 2026-11-08: Phase 12 done on `phase-12/team-workflow`; plan written with the recommended options
  and executed without a separate approval, as Ozan asked (`docs/plans/phase-12.md`). ADRs 0029
  (roles as data over six behaviours, model per role, the Tech Lead stays), 0030 (escalation chain,
  messages to the user), 0031 (architect plans, specs committed on a spec branch), 0032
  (dependencies, file ownership, merge-tree conflict checks, resolve-conflict relaunches), 0033 (QA
  integration runs gate the goal), 0034 (assistant briefings, project memory the user accepts).
  New package `role` and `docs/roles.md`; migration 0008; no new dependencies. Built-in roles:
  architect, tech-lead, worker, qa, reviewer, assistant, plus docs and security. The UI gets an
  inbox, plan view/editor/approval, QA and conflict panels, a role picker and project memory. The
  Phase 8 backlog flake (`TestTeamAuthorization`) was a real data race, fixed; two more pre-existing
  test races fixed. An independent review led to 9 fixes (typed messages quoted as JSON so the
  user's header cannot be forged, in-flight assignments in the overlap check, QA pass must contain
  the branches, every sub-task before the goal, relaunch and plan limits, ...). The real e2e found
  a haiku lead stalling after a report; reports now name the next step. `make check`,
  `make ui-test`, `make sandbox-test` and `make e2e` (host, sandbox, team, workflow 3 of 3) pass.
- 2026-11-08: Phase 12 review: all decisions approved, including the recommended per-role models.
  Ozan tested plan approval with a real architect in the UI end to end. Fixed before the merge: the
  architect no longer claims a plan is approved when "approved" is typed into its terminal (it
  points to the UI's Approve button), and the inbox's plan notice has Review plan and Approve plan
  buttons. Merged into `main` (`--no-ff`).
- 2026-11-08: Phase 9 done on `phase-9/memory-persistence`; plan written with the recommended options
  and executed without a separate approval, as Ozan asked (`docs/plans/phase-9.md`). A spike against
  Claude Code 2.1.319 showed `--resume <old> --fork-session --session-id <new>` continues a
  conversation under the daemon's id (and that a fork's transcript repeats its parent's messages
  with a cumulative cost-state). ADRs 0035 (resume and restore), 0036 (session history), 0037
  (relevant memory); migrations 0009 and 0010; no new dependencies. Exited workers, architects,
  leads and assistants can be resumed with their conversation (Resume button, replaces the Phase 8
  note); a restart restores the agents the stop interrupted (`--restore-agents`, default on), a
  mid-turn one with a short "you were cut off" prompt, and an inbox notice says what came back.
  Every session's terminal output is saved and replayed read-only with a session picker. Memory
  entries can be scoped to roles and files; prompts get the relevant ones first; `list_memory` for
  every behaviour but reviewers; Markdown export. Backlog: the sandboxed settings label after a
  restart is fixed. A pre-existing race in pull request updates (stale row, fresh reviews) surfaced
  as a flaky `TestGitHubLoop` and is fixed. An independent review led to 4 fixes (a failed resume
  poisoned later resumes, a shutdown during a restore cleared states, `prView` narrowed, history vs.
  removal). `make check`, `make ui-test`, `make sandbox-test` and `make e2e` (host, sandbox, team,
  workflow; a host agent and a sandboxed worker recalled their earlier conversation after a SIGKILL
  and restart) pass; the GitHub e2e was not run.
