# Progress

**Current phase:** Phase 18 (UI and office polish) is done on `phase-18/ui-polish`, waiting for Ozan's review.

**Phase order from here on** (decided by Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13, then 14. Phase numbers stay
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
- [x] Phase 10: Remote access (PWA over Tailscale)
- [x] Phase 11: Portfolio polish
- [x] Phase 12: Team workflow and roles
- [x] Phase 13: Office v2
- [x] Phase 14: Multi-project office
- [x] Phase 15: Codex adapter
- [x] Phase 16: Autonomous team
- [x] Phase 17: Daily use
- [x] Phase 18: UI and office polish (waiting for review)

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
- 2026-11-08: Phase 9 merged into `main` (`--no-ff`).
- 2026-11-08: Phase 10 done on `phase-10/remote-access`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-10.md`). ADRs 0038 (the
  loopback daemon behind `tailscale serve`, `--remote-host` `*.ts.net` only, Funnel refused, optional
  `--remote-users`), 0039 (paired devices: one-time codes from `agent-office pair` or the Devices
  panel, SHA-256-stored tokens in HttpOnly cookies, every route needs a device, revocation with
  lineage closes WebSockets), 0040 (PWA with an offline-page service worker, phone layout, terminal
  key bar, the focused terminal sets the size, Ctrl+Shift+C/V and copy on selection, strict CSP).
  New package `auth`, migration 0011, `docs/remote.md`; no new dependencies. Remote access was
  checked live against the real daemon through a proxy that behaves like `tailscale serve`
  (Chromium with a phone profile: pairing, terminals, revocation, installability, no CSP
  violations). An independent security review found no bypass and led to 6 fixes (device lineage
  on revocation, `--remote-users`, strict CSP, a planted `/tmp` runtime dir, no login in logs, the
  pairing code out of the URL). `make check`, `make ui-test` (36), `make sandbox-test` and `make e2e`
  (host, sandbox, team, workflow; the team tests on a rerun after an e2e harness fix) pass; the
  GitHub e2e was not run. Open: installing the app on Ozan's phone over his tailnet.
- 2026-11-08: Phase 10, after Ozan's manual test: terminal copy silently failed in Chrome (the
  async Clipboard API was refused without the clipboard-write permission, and the error was
  dropped). Copy now goes through the browser's copy event first, failures show a notice, a
  right-click menu has Copy and Paste, F12 reaches the browser; the new tests read back the
  clipboard with only clipboard-read granted and fail on the old code. `make check` and
  `make ui-test` (38) pass.
- 2026-11-08: Phase 10, Ozan's second manual test: (1) copy in Claude Code's fullscreen mode
  (mouse tracking; its selection arrives as OSC 52, which the terminal dropped) now works, with
  OSC 52 writes accepted only right after the user's gesture; (2) "resume failed" after a second
  restart: forks resumed without a prompt have no transcript, so resumes now continue the latest
  session with a conversation (ADR 0035 amended). `TestRestoreTwice`, OSC 52 UI tests, host e2e
  step 7d (two restores, codeword recalled); `make check`, `make ui-test` (41) and the host e2e pass.
- 2026-11-08: Phase 10 review: all decisions and fixes approved. Ozan checked by hand that copy works
  in a sandboxed fullscreen session (plain selection, and Shift+drag with Ctrl+Shift+C) and that
  a4fc81 resumes again. Merged into `main` (`--no-ff`). Open: installing the app on his phone over
  his tailnet.
- 2026-11-08: Phase 11 done on `phase-11/portfolio-polish`; plan written with the recommended options
  and executed without a separate approval, as Ozan asked (`docs/plans/phase-11.md`). README
  rewritten around a GIF of a real sandboxed haiku team run (`make demo`: Playwright drives a real
  daemon on a throwaway clone; 55 seconds, about two cents), a Mermaid architecture diagram, the key
  design decisions with their ADRs, a quick start verified from a fresh clone (in a clean Debian
  container and on this machine) and "How this was built"; `docs/adr/README.md` indexes the ADRs;
  `docs/demo.md`. `make secret-scan` (gitleaks v8.30.1 via `go run`): no secrets in any commit; the
  personal-data scan's one real finding is the personal Gmail address on every commit (reported, not
  rewritten). Backlog: worker images from a pinned Debian snapshot with exact versions and `python3`
  (ADR 0041; a `--no-cache` rebuild installs the same 128 versions); closing keywords in agents'
  commit messages flagged at push time (ADR 0020 amended). `make release` (ADR 0042) builds a
  deterministic tarball from a fresh worktree of an exact tag (two builds, same checksum); v0.1.0
  tagged locally, release notes in `docs/releases/v0.1.0.md`. No new dependencies (gitleaks runs
  pinned outside `go.mod`; mermaid was only downloaded to check the diagram renders). `make check`,
  `make ui-test` (41), `make sandbox-test` (with `AO_SANDBOX_NET=1` for the egress test) and
  `make e2e` (host, sandbox, team, workflow) pass; the GitHub e2e was not run.
- 2026-11-09: Phase 11 merged into `main` (`--no-ff`). The v0.1.0 tag was not kept: no tags or releases
  until Ozan says the project is finished (CLAUDE.md); `make release` and ADR 0042 stay for then.
- 2026-11-09: UI/UX polish pass on `ui/ux-polish` (plan `docs/plans/ui-ux-polish.md`), from Ozan's
  own use. Agents get a first name from the daemon (130 names; unique among listed agents; stored,
  migration 0012; ADR 0043); the id stays the key in the API, logs and branches. The tab strip
  holds the agents you opened (at most six, closable, movable, kept per browser) and a switcher
  (`/`) finds any of 20 agents, grouped by goal, exited ones last; a full-screen sheet on a phone
  (ADR 0044). Approvals moved into a drawer behind a header badge, so nothing moves when one
  arrives (tested by position). Exited agents walk out of the Office and free their desk (ADR 0024
  amended). No new dependencies.
- 2026-11-09: UI/UX polish follow-up (Ozan's review). One list of what waits on the user
  (access requests and terminal permission prompts, counted per agent) feeds the badge, drawer,
  title and Office, which had disagreed (ADR 0044 amended). The flaky tabs test read the tabs
  once before the daemon's snapshot arrived (1/30, 10/30 under CPU throttling), fixed with
  retrying assertions; throttling also found the `/` shortcut attached after the first paint.
  Agents see teammates' names in prompts, messages and tool outputs (ADR 0043 extended). The open
  tab keeps a readable task.
- 2026-11-09: Phase 13 done on `phase-13/office-v2`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-13.md`). ADR 0045 (floor
  plan, routes on a walkability grid, errands derived from what the UI sees); no daemon changes,
  no new dependencies. The office has four rooms (Tech Lead with the whiteboard, review room,
  kitchen with the coffee machine and the front door, workroom), side by side on a desktop and
  stacked on a phone. Characters walk routed paths: in through the front door, to the whiteboard
  when their task moves to review or done, to the review table while reviewing, one coffee per
  idle spell, straight back when they need the user, and out when they exit; nameplates stay on
  desks and say where their character is. Render on demand stays: ten characters walking drew at
  p95 0.4 to 0.6 ms (2.3 to 3.1 ms with an Android emulator busy on nine cores), an emptied office
  draws nothing. The UI tests found a click race after a tab remount and two grid-change bugs
  during walks (fixed); an independent review led to 5 fixes (a return to the desk never drawn
  with reduced motion, a stuck hover, a fade-in restarting, resumed leavers, the no-route
  fallback). Showcase refreshed: a new GIF from a real sandboxed haiku team (`make demo`) and current
  screenshots. `make check` and `make ui-test` (61) pass; `make e2e`
  was not run (daemon, hooks, engine and worktrees unchanged).
- 2026-11-09: Phase 13, before the merge (Ozan's review): the demo goal now names each sub-task's
  files (`internal/httpapi` for the health check, `internal/bookmark` for duplicate URLs), so the
  Tech Lead declares disjoint files and both workers run in parallel; the earlier goal had both
  edit the handler, and file ownership (ADR 0032) ran them one after the other. GIF re-recorded
  (goal reported after 50 s instead of 107, under two cents), README, screenshots and the review
  page updated. The README's inspiration line is removed, and the originality rule in `CLAUDE.md` no
  longer names other projects (only: do not copy code, names or assets from other projects).
- 2026-11-09: Phase 13, Ozan's second review: rooms for every role. A planning room (the
  architect's drafting table), a QA lab (test bench), a lobby (front door, the assistant at
  reception) and the user's office, whose door has a queue of the agents waiting on the user, as
  many as the Approvals count, with a sign counting the same list; the architect brings its plan.
  The list behind the badge now includes unanswered questions and plans waiting for approval (ADR
  0044 amended); roles from role packs wear a shirt of their own colour (ADR 0045 amended). Two
  bands of rooms on a desktop, three on a phone. An independent review led to 4 fixes (the office
  redrew on every page render, an idle station agent kept it drawing, the queue order, plans of
  several goals). `make check` (274 Vitest tests) and `make ui-test` (66) pass; office screenshots
  refreshed. `/etc/resolv.conf` on this machine had become empty at 09:14, so the static daemon (Go's
  own resolver) could not resolve `api.anthropic.com` and two recordings timed out; after Ozan
  fixed it (a symlink to systemd-resolved's stub), the GIF and the real-run stills were recorded
  again in the new office (goal reported after 57 s, $0.0237).
- 2026-11-11: Phase 13 recorded as merged (2026-11-09). Phase 14 "Multi-project office" added to
  `docs/plan.md` by Ozan.
- 2026-11-11: Phase 14 done on `phase-14/multi-project`; plan written with the recommended options and
  executed without a separate approval, as Ozan asked (`docs/plans/phase-14.md`). ADRs 0046 (one
  engine per project under a hub; project settings override the daemon's flags; project-scoped API
  under `/api/projects/{project}/`; `agent-office project list|add|set|remove|retry` over the control
  socket), 0047 (a daemon-wide agent cap across projects through a shared slot pool, an optional cap
  per project), 0048 (the UI shows one project and watches the others lightly, so Approvals counts
  every project; a Projects panel; an elevator with a floor per project; All projects usage); ADRs
  0017 and 0043 amended (one lead and unique names per project). Migration 0013 (`projects.settings`,
  `removed_at`); no new dependencies. `serve` works without `--repo`; `--repo` registers and selects
  its project. Restart reconciliation, retention and the orphan container cleanup no longer touch
  other projects. An independent code review led to 5 fixes (stale event streams after a settings
  change, settings checked before a project stops and rolled back if the reopen fails, a failed
  removal, tabs per project, the default project first) and a security review to 4 (repositories and
  sandbox-widening settings from this machine only, `secrets_dir` inside the daemon's, no agent
  workspaces or linked worktrees as projects, a host-mode isolation warning). `make check`,
  `make ui-test` (74), `make sandbox-test` (plus `TestWorkerEgress` with `AO_SANDBOX_NET=1`) and
  `make e2e` (host, sandbox, team, workflow, and the new two-project run: go and node packs at once,
  the cap across projects, both Tech Leads restored after a SIGKILL) pass; the GitHub e2e was not run.
- 2026-11-11: Phase 14 review: all 11 decisions approved. Ozan tested it by hand in a real browser on
  the real daemon (two packs, a team in each at once, approvals across projects, a refused settings
  change, SIGKILL and restore; not the phone). Fixed before the merge, each with a Playwright test:
  switching floors left the Office for the Board, a refused Save showed in the sidebar, and the
  Projects panel's running count was stale. The sandbox seed was re-checked with Claude Code 2.1.322
  (unchanged keys and dialogs); the tested version is now 2.1.322. `make check` and `make ui-test`
  (77) pass. Merged into `main` (`--no-ff`).
- 2026-11-11: Phase 15 "Codex adapter" added to `docs/plan.md` by Ozan, and done on
  `phase-15/codex-adapter`; plan written with the recommended options and executed without a separate
  approval, as Ozan asked (`docs/plans/phase-15.md`). Spike G against Codex CLI 0.167.1 (ChatGPT
  plan login) showed that Codex runs in the network-less worker with a placeholder login while the
  daemon adds the real access token on the API socket, so the gate Ozan set (stop if the real
  credential must be in the container) did not trigger; the container holds the account id and plan
  (identifiers). ADRs 0049 (Codex agents and their credential: placeholder login, TLS on the loopback
  leg from an in-memory CA, a route allowlist, the refresh token never read, sandboxed only) and 0050
  (the agent CLI per role, overridable like models; usage per vendor). Migration 0014 (`agents.cli`,
  `sessions.cli_session_id`); no new dependencies. Roles, `--role-cli`, project `role_clis` and
  `--codex-model` choose CLI and model; the UI shows a codex tag, roles' CLI and usage per vendor.
  The e2e runs found that ChatGPT's backend sends the Responses stream without a Content-Type and that
  Codex sends SessionStart only with the first prompt (both handled). An independent code review led
  to 5 fixes and a security review to 4 (cookies and personal account fields kept from the agent,
  encoded paths and upgrades refused, session learning, packs' env). `make check`, `make ui-test`
  (79), `make sandbox-test` (plus `TestWorkerEgress` with `AO_SANDBOX_NET=1`) and `make e2e` (host,
  sandbox, team, workflow, multi-project, a Claude-led team with a Codex worker including a SIGKILL
  and restore, and the GitHub run with a Codex reviewer) pass.
- 2026-11-11: Phase 15 review: all decisions approved, including the real ChatGPT account id and plan
  type in the placeholder login. Ozan tested it by hand in a real browser on the real daemon (a Claude
  Tech Lead with a Codex worker, codex tag and role picker, the Codex terminal with no login dialog,
  usage per vendor, a SIGKILL restart that recalled PLUM73, only placeholders in the container's
  `auth.json`). Fixed before the merge: a goal's card says why its Tech Lead cannot start while
  another goal's runs (one goal at a time per project) and opens the running one (Vitest and
  Playwright). The Codex terminal's one warning is Codex's notice that `--dangerously-bypass-hook-trust`
  is on, which agent-office passes on purpose; harmless. Backlog: several goals at once in one
  project. `make check` and `make ui-test` (80) pass. Merged into `main` (`--no-ff`).
- 2026-11-11: Phase 16 "Autonomous team" added to `docs/plan.md` by Ozan, from his PulseGo Sprint 3 run, and
  done on `phase-16/autonomous-team`; plan written with the recommended options and executed without a
  separate approval, as he asked (`docs/plans/phase-16.md`). Measured first: rootless Podman 6.1.4 here
  reports `State.OOMKilled=false` for real OOM kills and removes the cgroup within milliseconds, so
  `sandbox-init` writes an exit report from its own cgroup's `memory.events` (Podman's flag is read
  too). ADRs 0051 (exit reasons in plain words, one automatic resume, saved work on exit, the Tech
  Lead's `resume_agent` / `stop_agent` / `reclaim_task`, messages that wait for a resume and go into its
  first prompt), 0052 (a stall watchdog in the daemon wakes the lead of a goal that does not move,
  bounded nudges, then the user), 0053 (access decisions by the Tech Lead per project, optionally within
  a host policy; approvals stored as grants for the agent, its sub-task's later workers, or the project;
  fixes request #4); ADR 0019 amended. Migration 0015; no new dependencies. Worker memory per pack
  (`resources.memory`) and project (`worker_memory`), a running OOM watch, the Tech Lead's rule on what
  it decides and what it asks, the assistant's memory rule, the go pack with `make` and
  `storage.googleapis.com`, a grouped inbox. An independent code review led to 10 fixes and a security
  review to 2. `TestAutonomousTeamEndToEnd` reproduces the incidents with real haiku agents and real
  kernel OOM kills; its runs found 3 more bugs (an OOM death read as a plain kill, a waiting message lost
  when a resumed worker reported in its first turn, an approval not reaching the reassigned worker), and
  the full `make e2e` found a restore regression of the phase (a leftover exit report); all fixed with
  tests.
- 2026-11-11: Phase 16 merged into `main` (`--no-ff`).
- 2026-11-12: Phase 17 "Daily use" added to `docs/plan.md` by Ozan, from PulseGo Sprints 3–6, and done on
  `phase-17/daily-use` with the recommended options (`docs/plans/phase-17.md`). The board shows active goals
  (or one, picked); done goals go to an archive with their record (sub-tasks, agents including removed ones,
  cost, QA, the Tech Lead's report, merges). Marking a goal done closes its sub-tasks and ends its agents;
  its exited agents are removed in one action (ADR 0054). Merge squash-merges a goal's QA-passed head into
  `merge_target` with the Tech Lead's `commit_message` after a confirmation, refused on tracked changes,
  conflicts or moved heads; Push sends that commit, never forced (ADR 0055). A resume continues a session
  that got a prompt even if it was cut off mid-turn (measured with Claude Code 2.1.322; ADR 0035 amended).
  Stored access grants can be revoked (ADR 0053 amended). Claude Code's startup requests are prevented with
  `CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL` (measured) and known background requests are shown
  quietly (ADR 0016 amended). Worker pids per pack (`resources.pids`) and project (`worker_pids`), with a
  `pids.events` watch. Migration 0016; no new dependencies. A security review led to 4 changes and a code
  review to 5 fixes. `make check`, `make ui-test` (89), `make sandbox-test` and `make e2e` (the workflow run
  now merges and pushes a real goal to a local bare remote) pass; one Phase 16 autonomy e2e failure in the
  first full run is recorded, not explained.
- 2026-11-12: Phase 17 review: all decisions approved. Ozan tested it by hand on the real daemon (a goal on
  the playground, Merge and Push from the goal card, closing the goal, the archive view, removing exited
  agents). Merged into `main` (`--no-ff`).
- 2026-11-12: Phase 18 "UI and office polish" added to `docs/plan.md` by Ozan and done on `phase-18/ui-polish`
  with the recommended options (`docs/plans/phase-18.md`). One design system (`theme.css`,
  `docs/design-system.md`); a top bar with Approvals and Inbox, a left rail with the views and compact agent
  rows, a bottom tab bar on a phone; an agent's actions in a header above its terminal; Settings as a page
  with tabs and Inbox as a drawer, so nothing pushes the board down (ADR 0056). Board cards show a title, a
  status chip, the agent on it and the cost, and open a modal with everything else; a successful Merge
  offers to mark the goal done. Office layouts per project in YAML (`office_layout`), parsed and validated by
  `internal/office` with four presets, a live preview in the settings, hot desks for agents beyond the desks,
  and the default office with a warning for a stored layout that no longer validates (ADR 0057). The user's
  character sits at their desk while a device has the UI open and visible (ADR 0058). Branch clean-up of a
  done goal and of a project: merged, squashed or unmerged against the merge target, compare-and-swap
  deletes, unmerged only when named again, every head recorded (migration 0017, ADR 0059). Before and after
  screenshots in `docs/screenshots/phase-18/`. No new dependencies. A security review led to 3 fixes and a
  code review to 3 more. `make check` (328 Vitest tests), `make ui-test` (101), `make sandbox-test` and
  `make e2e` (host, sandbox, team, workflow, multi-project, Codex team, autonomy) pass; the GitHub e2e was not run.
- 2026-11-12: Phase 18 review changes, on `phase-18/ui-polish` before the merge: the shown project's settings
  behind a gear by the picker (General, Office, Sandbox, Memory) and the app's in the rail (Projects,
  Devices, Profile); "+ New project" in the picker; Settings → Profile with a name for the user's nameplate
  and an avatar creator drawn with the office's art, saved daemon-wide (migration 0018, `/api/profile`);
  the modal shows only the actions that fit the task's state; the office fills the pane's width at a
  whole-pixel scale and covers a tall pane with workroom floor; names only on a phone, the task on a tap;
  inherited values in full under each setting; settings tabs that fit a phone. ADR 0056-0058 amended.
  `make check` (407 Vitest tests), `make ui-test` (106), `make sandbox-test` and `make e2e` pass (GitHub e2e
  not run).

