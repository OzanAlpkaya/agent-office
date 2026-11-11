# agent-office: roadmap

Each phase has a goal, a scope and a Definition of Done (DoD). Every phase starts with a short
approved plan and ends with an updated `PROGRESS.md` and a plain-language summary. Later phases are
intentionally coarser; their DoD is refined at the start of the phase.

**Order** (decided 2026-11-08): phases run 0–8, then 12, 9, 10, 11, 13, then 14 and 15
(added 2026-11-11), then 16. Numbers are kept, so references in ADRs and plans stay valid.

---

## Phase 0: Research & design

**Goal:** Reduce risk before writing production code: confirm how the key building blocks behave
and write down the architecture.

**Scope**
- Throwaway spikes under `spikes/`: PTY ↔ WebSocket ↔ xterm.js round-trip; real Claude Code hook
  payloads; Claude Code session file format (token usage); Codex CLI equivalents.
- Verify current APIs of the chosen Go libraries.
- `docs/architecture.md`: components, data flow, domain model, first threat model.
- First sandbox evaluation: rootless Podman vs Claude Code's built-in sandbox.

**Definition of Done**
- [x] Spikes run and their findings are written up (no raw captured data committed).
- [x] `docs/architecture.md` exists with a component diagram, data flow and domain model.
- [x] ADRs for: terminal transport, hook ingestion, persistence, UI embedding, MCP transport,
      sandbox approach.
- [x] Phase 1 DoD refined based on the spike results.

---

## Phase 1: Single agent

**Goal:** Run one Claude Code agent in a PTY and watch it live in the browser, with its state
derived from Claude Code hooks.

**Scope** (see ADRs 0002, 0003, 0005)
- Daemon on loopback with `net/http`; one Claude Code agent spawned in a PTY (`creack/pty`) with
  `--session-id`, a generated `--settings` file and a scrubbed child env.
- Terminal sessions that outlive WebSocket connections: scrollback ring, fan-out, redraw on attach.
- `agent-office hook` forwarder + per-agent unix socket; adapter that maps Claude Code hook
  payloads to common events; agent state machine.
- React + Vite + TS UI with xterm.js, embedded via `go:embed`; Vite dev server proxy in `--dev`.
- Agents run on the host with normal permission prompts (no bypass mode before Phase 3).

**Definition of Done**
- [x] `agent-office serve` listens on loopback only, serves the embedded UI, and starts one
      Claude Code session from the UI.
- [x] The terminal streams live; typing and resizing work; closing and reopening the tab shows the
      session again (scrollback + redraw) without restarting the agent.
- [x] Cross-origin WebSocket handshakes are rejected (test).
- [x] State in the UI follows the hooks: starting → idle → working → waiting_permission → working →
      idle → exited, using `PermissionRequest` for waiting and ignoring events with `agent_id`.
- [x] State cannot stay stuck: process exit always ends in `exited`, even without a SessionEnd hook.
- [x] Hook events reach the daemon for every event type, including SessionStart, through the
      forwarder; a payload whose `session_id` does not match its socket's agent is rejected (test).
- [x] If the daemon is down, the forwarder exits 0 within its timeout (test).
- [x] The child env contains no `CLAUDECODE`/`CLAUDE_CODE_*` variables from the daemon (test).
- [x] Raw hook payloads are not logged or persisted; only reduced fields (test with a fixture
      containing a prompt).
- [x] Unit tests for payload parsing (fixtures with redacted shapes from Spike B) and the state
      machine; `go test -race ./...`, `go vet`, `gofmt` pass; the web typecheck and build pass.
- [x] A single `make build` produces one binary containing the built UI.
- [x] Open from Phase 0, answered or recorded: Stop on interrupt (Esc), the permission-denied path,
      the trust dialog for new paths.

---

## Phase 2: Multi-agent + task board

**Goal:** Run several agents at once, each in its own git worktree, managed from a task board.

**Scope**
- SQLite persistence (`modernc.org/sqlite` + `sqlc`) with migrations: agents, tasks, sessions.
- One git worktree + branch per worker agent; lifecycle (create, clean up).
- Worktree location: Claude Code's workspace trust covers subfolders and **all worktrees of a
  trusted repo**, wherever they live (`docs/research/claude-code-trust.md`). Decide the location
  in the Phase 2 plan; still detect agents stuck in `starting`.
- Task board UI (columns by status); multiple terminals.

**Definition of Done**
- [x] At least 3 agents run concurrently on one repo, each in its own worktree.
- [x] Tasks can be created, assigned and moved through statuses; state survives a daemon restart.
- [x] Worktrees are cleaned up when an agent is removed.
- [x] Integration tests for the store and the worktree manager.

---

## Phase 3: Isolation & security

**Goal:** Worker agents run with full permissions only inside a sandbox.

**Prerequisite:** Podman installed on the host (installed manually by the owner).

**Scope**
- Rootless Podman container per worker, with only the worktree mounted.
- Egress allowlist (deny by default, allow listed domains).
- Secrets stay outside the worker: the daemon reads the OAuth token from a user-only file and
  its API proxy injects it (ADR 0015, ADR 0016); never written to disk or logs by agent-office.
- Agents run with permission prompts disabled only inside the sandbox.
- One worker image (`internal/sandbox/Containerfile`): Debian slim pinned by digest,
  ca-certificates, git (no SSH client), curl, ripgrep, and the toolchain the playground needs:
  Go 1.27.3, no cgo (so no `go test -race` inside workers). The versioned `claude` binary and the
  static `agent-office` binary are mounted read-only, not baked in. **Per-project toolchains are
  decided by the stack packs in Phase 6**; until then every worker gets this image.

**Definition of Done**
- [x] A worker cannot read files outside its worktree or reach non-allowlisted domains (tested).
- [x] The OAuth token is absent from the agent's env, logs, DB, repo and `podman inspect` (tested).
- [x] Threat model updated; ADR on Podman vs Claude Code sandbox accepted.

---

## Phase 4: Team MCP server + lead agent

**Goal:** Agents coordinate through a team MCP server; a lead agent plans and delegates.

**Scope** (see ADRs 0006, 0017–0019 and `docs/plans/phase-4.md`)
- MCP server (`modelcontextprotocol/go-sdk`) with tools: `create_task`, `assign_task`,
  `list_tasks`, `send_message`, `report_done`, `request_access`, served per agent on its own socket
  through the `agent-office mcp` stdio bridge.
- Lead agent role that breaks a goal into tasks and assigns them; each assignment starts a worker.
- Message delivery on the recipient's Stop hook while it works, typed into its terminal when it is
  idle; claimed once in SQLite.
- `request_access` (network domains) goes to a human approval queue in the UI; an approval grants
  the host to that agent only, until it exits.

**Definition of Done**
- [x] Given a goal, the lead creates and assigns tasks that workers complete end-to-end
      (`TestTeamEndToEnd`, real Claude Code in sandbox mode).
- [x] Messages reach the recipient after its next Stop event, exactly once; an idle recipient gets
      them typed into its terminal (claimed once in SQLite; "exactly once" ends at the hook
      forwarder, ADR 0018).
- [x] Access requests need human approval in the UI; nothing is granted automatically.
- [x] Tests for every MCP tool (input validation, authorization by agent role).

---

## Phase 5: GitHub loop + pluggable code review

**Goal:** Finished work becomes a pull request and gets reviewed automatically.

**Scope** (see ADRs 0020–0022 and `docs/plans/phase-5.md`)
- go-github client with a token read from a user-only file whose path comes from the environment
  (`AGENT_OFFICE_GITHUB_TOKEN_FILE`, ADR 0020); the daemon pushes over SSH from the main repository.
- Open PRs from worker branches; read review comments and checks (polled; no webhooks).
- Pluggable reviewer interface: CodeRabbit or an internal reviewer agent (role `reviewer`).
- Review feedback goes back to the original worker, relaunched in its workspace on its branch.

**Definition of Done**
- [x] A completed task results in a PR on a test repository (`TestGitHubEndToEnd`: real Claude Code,
      sandboxed, PR on `OzanAlpkaya/agent-office-playground`).
- [x] Both reviewer implementations work behind one interface (`TestReviewerContract` runs one
      contract against both; the internal reviewer also ran live in the e2e; CodeRabbit is tested
      against the GitHub fake only, since its app is not installed on the playground).
- [x] Review comments are turned into follow-up work for the original worker (`TestGitHubLoop`,
      `TestSandboxedReviewLoop`; live in the e2e through "Send to worker", because haiku approved the
      small change both times).
- [x] Merging stays a human action (no merge operation in the client, COMMENT reviews only, the
      fake fails any other request; a human merge only moves the task to done).

---

## Phase 6: Stack packs

**Goal:** Configure agents per tech stack with declarative YAML packs.

**Scope** (see ADR 0023, `docs/packs.md` and `docs/plans/phase-6.md`)
- YAML per tool/stack: allowed domains, skills, MCP servers, env var names (names only, no values),
  and the **toolchain**, i.e. which worker image a project's agents get. This replaces Phase 3's single
  image, which carries only the playground's Go toolchain (e.g. a Go pack with a C toolchain for
  `-race`, a Node/TypeScript pack with Node and npm).
- Schema validation with clear errors; packs applied when launching an agent/sandbox.
- Packs come from the binary or a user-owned directory, never from the repository; one per daemon
  run (`--pack`). The Phase 3 image becomes a base image without a toolchain (the default); pack
  images build `FROM` it. Secret values are user-only files, served once to the container's init.

**Definition of Done**
- [x] At least two example packs (e.g. Go, Node/TypeScript): built-in `go` and `node`, each with a
      skill (`TestBuiltinsAreValid`; `TestSandboxedPackToolchains` runs `go test -race` and
      `node`/`npm` in their images).
- [x] Invalid packs are rejected with actionable error messages (tested): `TestLoadInvalid` (45
      cases, each with its message), `TestLoadReportsEveryProblem`, `TestLoadDirChecksTheTree`,
      `TestFindAndList`, `TestPackCommand`; `agent-office pack check` prints them.
- [x] A pack's domains feed the egress allowlist and its env var names feed the secret injection:
      `TestPackOptions`, `TestSandboxedPackLaunch` (a secret reaches the worker's agent CLI once and
      nothing else; leads and reviewers get none).
- [x] A pack selects the worker image (toolchain); projects without a pack get the default image:
      `TestPackOptions`, `TestWorkerImageAndInit` (base image, no toolchain), `TestSandboxEndToEnd`
      (real Claude Code with `--pack go` runs `go test -race` in the worker).

---

## Phase 7: Pixel office UI

**Goal:** A pixel-art office view where agents are characters whose activity reflects their state.

**Scope** (see ADRs 0024, 0025 and `docs/plans/phase-7.md`)
- PixiJS scene in an Office tab next to the task board, lazy-loaded; original art drawn in code.
- Agent state (working, waiting, idle, reviewing, starting, exited) shown through poses and
  animations; the lead's whiteboard shows its sub-tasks; messages fly between desks; click to open
  a terminal. No daemon changes.

**Definition of Done**
- [x] The office shows every running agent with live state: every agent the daemon knows gets a
      desk and a pose that follows its state and role (`pose.test.ts`; Playwright "shows every
      agent at a desk with its pose", "follows live state changes, new and removed agents"); live
      check against a real daemon with a haiku lead and two workers in sandbox mode.
- [x] Clicking an agent opens its terminal/task: a click on the character in the canvas, or its
      nameplate (also by keyboard), opens the agent's terminal (Playwright "clicking a character
      opens its terminal", "nameplates open agents from the keyboard"; live check).
- [x] Runs smoothly with 10 agents; all assets are original or properly licensed: in headless
      Chromium (WebGL) a drawn frame costs the scene 0.7 ms at p95 and at most 3.4 ms with 10 and
      20 agents, the page stays at 60 fps, the scene draws only frames that changed (at most
      30 fps; none when nothing moves), and 20 tab round trips leak no WebGL context (Playwright
      "stays smooth with 10/20 agents", "an office where nothing moves does not redraw",
      "switching tabs does not leak…"); all art drawn in code (`web/src/office/ART.md`), checked
      by `art.test.ts`.

---

## Phase 8: Observability & cost

**Goal:** See what agents are doing and what they cost.

**Scope** (see ADRs 0026–0028, `docs/usage.md` and `docs/plans/phase-8.md`)
- Token usage per API message: metered on a sandboxed agent's API socket, read from a host-mode
  agent's session files (transcript and subagent files); priced when read from a configurable
  price table (integer money).
- Per agent / task / goal / session / model usage, turns and tool calls; optional budgets per
  session and per goal, refused on the API socket; structured logs with the session id as the
  correlation id; event retention.
- Usage tab in the UI; usage on agent and task cards.

**Definition of Done**
- [x] Token usage per agent and task is shown and matches the session files (tested with fixtures):
      the same messages as a redacted transcript and as API responses give identical usage, and the
      price table reproduces the transcript's own cost (`TestTranscriptMatchesAPIResponses`); the
      engine turns a host transcript into the same totals (`TestHostUsageFromTranscript`,
      `TestTranscriptWrittenAfterStop`); the UI shows them per agent, task, goal, session and model
      (Playwright `usage.spec.ts`). Live: in `make e2e`, three real host sessions equalled their
      transcripts, and every message of a sandboxed session's transcript was metered on its API
      socket with the same tokens.
- [x] Cost estimates use a configurable price table: built in from the pricing page (2026-11-08),
      overridden per model by a user-owned `prices.yaml` (`TestPricesUserFile`, `TestPricesInvalid`),
      re-priced on read (`TestUsageRepricedOnRead`).
- [x] Logs can be followed across daemon, agent and hook events via a correlation id: every record
      naming an agent carries its `session_id` and `task_id`, from start and hooks through team
      tool calls and API requests (with Anthropic's request id) to exit (`TestLogsFollowOneSession`);
      `--log-format json`.

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- When an agent stops, Claude Code prints "Resume this session with: claude --resume …", which
  cannot be used with agent-office yet (the session belongs to the agent's sandbox and config).
  Hide that line or explain it in the terminal view until Phase 9 makes resuming work. Placed here
  because it is the next phase; the real fix is in Phase 9.

---

## Phase 9: Memory & persistence

**Goal:** Agents and the team keep useful context across sessions.

**Scope** (see ADRs 0035–0037 and `docs/plans/phase-9.md`)
- Resume an exited agent's conversation (`claude --resume … --fork-session`) in its workspace and
  sandbox; restore the agents a daemon stop interrupted (`--restore-agents`, default on).
- Session history: every session's terminal output saved in the data dir and shown in the UI.
- Relevant project memory: entries scoped to roles and files, `list_memory` for the team, Markdown
  export.

**Definition of Done**
- [x] A restarted daemon restores agents, tasks and history: interrupted agents are resumed with
      their conversations (`TestRestoreAfterShutdown`, `TestRestoreAfterCrash`,
      `TestRestoreStopsAtShutdown`; live in `make e2e`: a host agent and a sandboxed worker recalled
      their earlier conversation after a SIGKILL and restart), and every session's terminal output
      and counters stay readable (`TestSessionHistory`, `TestResumeAndSessionsAPI`; Playwright "an
      agent from an earlier daemon run shows its saved sessions").
- [x] Agents can read relevant project memory at task start: entries scoped to the agent's role or
      its task's files come first, others are left out, and `list_memory` shows all accepted
      entries to every behaviour but reviewers (`TestMemoryRelevance`, `TestBehaviourTools`).
- [x] Memory is inspectable and editable by the user: scopes shown and edited in the memory panel,
      exported as Markdown (`TestTeamWorkflowAPI`; Playwright "project memory proposals wait for
      the user").

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- [x] Make the "Resume this session" line meaningful: Resume continues a stopped agent's session
      from agent-office (same agent, workspace and sandbox); the Phase 8 note is replaced
      (`TestResumeKeepsAgentAndWorkspace`, Playwright "an exited agent's terminal explains the
      resume line and resumes it").

**Backlog** (from the Phase 8 review, 2026-11-08)
- [x] A sandboxed agent's card no longer switches to "user settings on" after a restart: the
      effective value is stored, migration 0009 fixes old rows, and sandboxed cards say "sandbox
      config" (`TestMigration0009FixesSandboxedSettings`).

**Backlog** (from Phase 9)
- Relaunches for review follow-ups, conflict rounds and architect escalation still start fresh
  sessions (ADR 0035); switching them to resume is a small change if fresh sessions turn out to
  lose too much.
- Follow-up and conflict rounds do not survive a restart (ADR 0022); persisting the round would
  let those workers be restored too.

---

## Phase 10: Remote access

**Goal:** Use agent-office from a phone or another machine over Tailscale.

**Scope** (see ADRs 0038–0040, `docs/remote.md` and `docs/plans/phase-10.md`)
- Installable PWA (manifest, icons drawn in code, an offline-page service worker); a phone layout
  with a terminal key bar.
- Access over Tailscale only: the loopback daemon behind `tailscale serve`, `--remote-host`
  (`*.ts.net`), optional `--remote-users`; Funnel refused.
- Authentication for every request: devices paired with one-time codes (`agent-office pair`, the
  Devices panel), tokens in HttpOnly cookies, revocation that closes WebSockets; a strict CSP.

**Definition of Done**
- [x] The PWA installs on a phone and shows the board, terminals and approvals: Chromium reports no
      installability errors, on the mocked UI and on the real daemon (Playwright "Chromium finds no
      installability errors…", live check); phone viewports (Pixel 7, iPhone 14) show the board,
      approvals with 44 px buttons, the Agents tab and a terminal with its key bar, without the page
      scrolling (Playwright "on a phone …"); live: a phone-profile Chromium paired over HTTPS
      through a proxy set up like `tailscale serve` and used a real Claude Code terminal. **Open:**
      installing on Ozan's own phone over his tailnet (needs Tailscale on this machine, sudo).
- [x] The daemon is not reachable outside localhost/Tailscale; unauthenticated requests are
      rejected: loopback-only listener; remote Hosts only for `--remote-host` names with a tailnet
      user's identity and no Funnel marker (`TestRemoteHostRules`, `TestRemoteUsers`,
      `TestBehindTailscaleServe`); every route but three public ones and both WebSockets answer 401
      without a device (`TestEveryRouteNeedsADevice` walks the route table); a revoked device's
      WebSockets close with 4401 (`TestRevokeClosesWebSockets`).

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- [x] Copy and paste in the browser terminal: selection, Ctrl+Shift+C/V and a right-click menu with
  Copy and Paste, working without the clipboard-write permission (fixed after Ozan's manual test);
  F12 reaches the browser (Playwright "copied text lands in the clipboard without the
  clipboard-write permission", "paste from the menu and Ctrl+Shift+V…", "a copy that fails says
  why"). Original item:
  Copy and paste in the browser terminal: Ctrl+C must keep interrupting the agent, Ctrl+Shift+C
  opens Chrome's devtools, and a mouse selection is not copied. Support Ctrl+Shift+C / Ctrl+Shift+V
  and copy on selection, like a normal terminal (xterm.js key handler and selection API; the
  clipboard API needs a secure context, which loopback is). Needed on a phone too.

**Backlog** (from Phase 10)
- Push notifications for approvals and questions waiting on the user (Web Push: a push service and
  VAPID keys; ADR 0040).
- A QR code for pairing a phone (needs a QR encoder; the 8-character code is typed today).
- A worker started without a task that never got a prompt has no conversation, so restoring or
  resuming it fails ("resume failed"); refuse it up front with a clear reason instead (found while
  fixing the restore-twice bug).

---

## Phase 11: Portfolio polish

**Goal:** Make the project easy to understand, run and evaluate.

**Scope** (see ADRs 0041, 0042, `docs/demo.md` and `docs/plans/phase-11.md`)
- README rewrite: pitch, a GIF of a real team run, screenshots, a Mermaid architecture diagram, the
  key design decisions with their ADRs, a quick start verified from a fresh clone, "How this was
  built"; an ADR index by topic.
- `make demo` records a real sandboxed team run in the Office tab (Playwright) and makes the GIF.
- `make secret-scan` (gitleaks, pinned) over the whole history; a personal-data scan; findings
  reported, history not rewritten.
- `make release`: a deterministic linux/amd64 tarball from a clean, exactly tagged tree; v0.1.0.
- Worker images from a pinned Debian snapshot with exact versions; `python3` in the base image.
- Closing keywords in agents' commit messages flagged at push time.

**Definition of Done**
- [x] A fresh Linux machine can install and run it by following the README: a clean Debian
      container with only the documented build prerequisites builds it from a fresh clone; on this
      machine a fresh clone followed the quick start (worker image, serve with a scratch data dir,
      pair, a sandboxed worker on a task, its branch in the repository). Two README corrections came
      out of it (a user-started worker stays after reporting; `libatomic1` for Node's tarball on a
      minimal Debian).
- [x] Tagged release with binaries: `v0.1.0`, built by `make release` (static linux/amd64, a
      deterministic tarball: two builds of the tag gave the same checksum) with release notes in
      `docs/releases/v0.1.0.md`; uploading it on GitHub is Ozan's step.
- [x] A short demo video/GIF and an architecture write-up linking the ADRs: `docs/screenshots/team-run.gif`
      (a real haiku team, 55 seconds of real time at about 2×, 6 MB), the README's design-decisions
      table and Mermaid diagram (rendered with mermaid 11 to check it), `docs/adr/README.md`.

**Backlog** (planned for this phase)
- [x] Pin Debian package versions in the worker image: apt sources pinned to the base image's own
      `snapshot.debian.org` timestamp, exact versions in the base and pack images; a `--no-cache`
      rebuild installs the same 128 package versions (ADR 0041, `TestWorkerImageAndInit`).
- [x] Closing keywords in agents' commit messages: flagged at push time, accepted through the
      existing flagged-commit flow; the task prompt asks agents not to write them (ADR 0020 amended,
      `TestClosingKeywordsBlockPush`, `TestClosingReferences`, `TestCommitMessages`).
- [x] `python3` in the base image (ADR 0041). Original item: the worker image has no `python3`;
      agents reach for it to edit files and fail.

**Backlog** (from Phase 11)
- CI (GitHub Actions) running `make check` on pushes and pull requests; a change under `.github/`.
- Signed release artifacts (for example Sigstore), and linux/arm64 builds once they can be tested.
- Personal data in the public history: every commit's author and committer is a personal Gmail
  address (reported to Ozan; not rewritten). Future commits could use GitHub's noreply address.

---

## Phase 12: Team workflow and roles

Added after Ozan's end-to-end test (2026-11-08) and expanded by him before planning (2026-11-08)
into "Team workflow and roles". Appended rather than inserted so that the phase numbers ADRs refer
to stay valid.

**Goal:** A team run that holds up on real work: roles are data, an Architect agrees the plan with
the user, a Tech Lead executes it in dependency order and stays for follow-ups, QA integrates and
tests the result, an Assistant keeps the user briefed, parallel work does not silently collide,
and every agent reaches the user or the agent above it.

**Scope** (see ADRs 0029–0034, `docs/roles.md` and `docs/plans/phase-12.md`, sub-phases 12.1–12.7)
- Roles as data (role packs: prompt, team tools, denied tools, model, read-only or read-write
  workspace, reports-to) over six engine behaviours; model per role.
- Architect (read-only) produces a plan (design, sub-tasks, dependencies, files, acceptance
  criteria), editable and approvable in the UI and committed as `docs/specs/<goal>.md`; optional.
- Tech Lead (the lead, renamed) executes the approved plan and stays until the goal is done.
- QA merges reported branches into an integration branch in the sandbox, tests, files bugs as
  sub-tasks; the goal report needs a passing run.
- Assistant: briefings, pending decisions, suggestions, project memory (accepted by the user).
- Escalation chain worker → Tech Lead → Architect → user; messages to the user on the task card.
- Conflicts: dependencies and file ownership enforced on assignment, `git merge-tree` checks on
  `report_done`, relaunch to resolve after a PR merges.

**Definition of Done**
- [x] Roles are YAML (built-in and user-owned); a new worker-behaviour role (e.g. `docs`) runs
      without code changes, with its own prompt, model, tools and workspace mode
      (`TestRolesAreData`: a user's `devops` role with its model, prompt, deny rules and two team
      tools; `TestLoadInvalid`, `TestUserRoles`, `TestBehaviourTools`; built-in `docs` and
      `security` roles).
- [x] A lead that reported its goal stays idle until the goal is marked done or stopped, and
      takes follow-up work typed into its terminal (`TestReportDone`: still running after its
      report, a new sub-task moves the goal back to in progress, marking the goal done ends it with
      `goal_done`; live in `TestTeamEndToEnd` and `TestTeamWorkflowEndToEnd`).
- [x] An Architect's plan is shown, edited and approved in the UI; approval commits
      `docs/specs/<goal>.md` and creates the sub-tasks; the Tech Lead starts on it
      (`TestArchitectPlanToTechLead`, `TestEscalationRevisesThePlan`; Playwright "a proposed plan is
      shown on the goal and approved by the user"; live in `TestTeamWorkflowEndToEnd`).
- [x] A lead can make a sub-task depend on others; it starts only after they are done, from
      their work; overlapping files without a dependency are refused
      (`TestDependenciesAndFileOwnership`, including a merge of two dependencies;
      `TestOverlapCountsAssignmentsInFlight`; live: the dependent branch had its dependency's file).
- [x] Conflicts with the default branch and other open agent branches are detected on
      `report_done` and shown in the UI (tested with two branches editing the same lines):
      `TestConflictsOnReportAndResolve`; Playwright "task cards show dependencies, conflicts, QA and
      their roles".
- [x] After a merge, a worker whose branch conflicts can be relaunched to resolve it
      (`TestMergeRelaunchesConflictingWorker`: automatic after a GitHub merge;
      `TestConflictsOnReportAndResolve`: Resolve conflict by hand, report refused until the merge is
      committed).
- [x] QA merges the reported branches, reports pass or fail with bugs as sub-tasks, and gates
      the goal report (`TestQAGatesTheGoal`, `TestQAPassNeedsTheBranches`, `TestQAExitWithoutReport`,
      `TestGoalNeedsEverySubTask`; live in `TestTeamWorkflowEndToEnd`).
- [x] Any agent without someone above it can message the user; the message appears on its task
      card and in the inbox, and the user's reply reaches the agent (`TestMessageUserAndChain`,
      `TestArchitectChain`, `TestOneLineDeliveryCannotForgeTheUser`; Playwright "the inbox shows the
      team's questions and sends the answer").
- [x] The Assistant gives a briefing from daemon data and proposes memory that reaches other
      agents only once the user accepts it (`TestAssistantBriefingAndMemory`,
      `TestMemorySectionIsCapped`; Playwright "project memory proposals wait for the user").

**Backlog** (from the Phase 8 review, 2026-11-08)
- [x] Investigate the intermittent `TestTeamAuthorization` failure: a real data race (`Start` read
      the agent's view after releasing the engine lock while its first hook changed it), fixed;
      plus a test timing assumption (a 2 s hook timeout in the fake CLI under load).

**Backlog** (from Phase 12)
- QA's integration branch carries the tests QA added, but only worker branches become pull
  requests: offer the integration branch as a pull request (or a sub-task that moves QA's tests
  onto the default branch).
- Several goals in parallel (one Tech Lead, architect and QA at a time today, ADR 0017).
- [x] Per-task relevance and export of project memory (done in Phase 9, ADR 0037).

---

## Phase 13: Office v2

Design backlog from Ozan (2026-11-08).

**Goal:** The office tells more of the story: characters move between places that mean
something, in an office with rooms.

**Scope** (see ADR 0045 and `docs/plans/phase-13.md`)
- A floor plan with the Tech Lead's room (whiteboard), a planning room (the architect's drafting
  table), a review room (table, wall screen), a QA lab (test bench), a kitchen (coffee machine), a
  lobby (front door, the assistant at reception), the user's office and the workroom; two bands of
  rooms on a desktop, three stacked on a phone. Roles from role packs get a shirt of their own.
  Desks stay assigned (ADR 0024).
- Characters walk routed paths (walkability grid, BFS, string pulling) on errands derived from what
  the UI already sees: a report goes to the whiteboard, a reviewer reviews in the review room, an
  idle agent takes one coffee, new agents walk in through the front door, leavers walk out.
- Render on demand stays; `prefers-reduced-motion` moves characters without walking.
- Showcase refreshed: a new README GIF of a real team run and current screenshots.

**Definition of Done**
- [x] Characters walk between their desk and the places their events point to, and back: Vitest
      `errands.test.ts` (every rule, priorities, spots, report then exit) and `floor.test.ts`
      (a clear route from every desk to every spot and the door, in every plan); Playwright
      "a report sends the worker to the whiteboard, then back to its desk", "a worker that reports
      and exits reports first, then leaves by the front door", "an idle agent takes one coffee and
      comes back", "the user comes first…", "a new agent walks in through the front door…",
      "a character away from its desk opens its terminal when clicked"; live in the `make demo`
      run (workers walking in, reporting at the whiteboard, leaving).
- [x] The office has at least three rooms with a clear purpose: eight (Planning, Tech Lead, Your
      office, QA lab, Review room, Kitchen, Lobby, Workroom), each with an HTML label and a tooltip
      saying what it is for; the architect, QA and the assistant work at stations of their own, and
      agents waiting on the user queue at its door, as many as the Approvals count (Playwright "agents
      waiting on you queue at your door…", "the architect, QA and the assistant work at their own
      stations", "roles from role packs sit in the workroom in a shirt of their own", "a question and
      a plan waiting for you are in Approvals…"; Playwright
      "the office has rooms with a purpose"; on a phone the stacked plan, "the office stacks its
      rooms and stays readable").
- [x] An office where nothing moves still renders nothing; with 10 agents walking, a drawn frame
      stays within the Phase 7 budget: ten characters walking out at once drew 64 frames at p95
      0.4 to 0.6 ms (max about 6 ms) in headless Chromium with WebGL, and 2.3 to 3.1 ms (max 15 ms)
      while an Android emulator kept nine cores busy; the emptied office then
      drew no frames (Playwright "a busy office…", "an office where nothing moves does not
      redraw"); reduced motion changes places without a single walking frame.
- [x] All new art is original or properly licensed: pixel maps drawn in code
      (`web/src/office/ART.md`), checked by `art.test.ts`.

---

## Phase 14: Multi-project office

Added by Ozan after Phase 13 was merged (2026-11-11).

**Goal:** One daemon runs several repositories at the same time.

**Scope** (see `docs/plans/phase-14.md`)
- Projects are first-class: add, list and remove them from the CLI and the UI. Each project has its
  own repository, stack pack (image, egress, secrets, skills), default models, GitHub settings and
  budgets.
- `serve` works without `--repo`; `--repo` keeps working and registers and selects that project.
- The engine holds per-project state; "one goal at a time" becomes per project. `--max-agents` is a
  daemon-wide cap across projects, with an optional per-project cap.
- API and WebSocket events are project-scoped. The UI gets a project switcher; board, usage, inbox
  and memory are per project; the Approvals badge counts across all projects.
- The office shows one floor per project and can switch between them.
- Restart and restore work per project. Usage shows totals per project and overall.

**Definition of Done**
- [x] Two projects with different packs (go and node) run sandboxed teams at the same time, end to
      end: `TestMultiProjectEndToEnd` (a daemon without `--repo`, both projects added with
      `agent-office project add`; a Tech Lead and a worker in each at once; the go worker wrote
      `go version go1.27.3`, the node worker `v20.19.2`; no branch crosses repositories; 35 s,
      about $0.013 with haiku).
- [x] Isolation tests: an agent in project A cannot reach project B's tasks, messages, worktrees or
      memory through any socket or MCP tool: `TestAgentsCannotReachAnotherProject` (every team
      tool over the real MCP socket with B's ids and names; B's session on A's hook socket),
      `TestProjectRoutesAreIsolated`, `TestProjectEventsCarryOnlyTheirProject`,
      `TestSandboxedProjectsAreIsolated` (mounts and visible paths in a real Podman worker).
      Host-mode agents run as the user; for them isolation is the API and MCP level (ADR 0046).
- [x] The daemon-wide agent cap holds across projects: `TestHubCapAcrossProjects`,
      `TestHubCapHoldsUnderConcurrentStarts`, `TestHubCapCountsResumes`, `TestHubProjectCap`; live
      in `TestMultiProjectEndToEnd` (a fifth start refused in both projects, a slot freed in one
      used by the other).
- [x] After a daemon SIGKILL, both projects come back with their agents restored:
      `TestMultiProjectEndToEnd` (both Tech Leads resumed their conversations, each project's
      inbox names only its own), `TestHubRestoresEveryProject`.
- [x] `make check`, `make ui-test` (74), `make sandbox-test` and `make e2e` (host, sandbox, team,
      workflow, multi-project) pass; the GitHub e2e was not run.

**Backlog** (from Phase 14)
- Purge a removed project's history, branches and worktrees (removal keeps them today).
- Enforce the project in the by-id queries themselves (`AND project_id = ?`), as defence in depth
  behind the engine's checks.
- A GitHub token file per project (one fine-grained token covers every project's repository today).
- Error messages to remote devices name host paths (the repository, git's stderr).

---

## Phase 15: Codex adapter

Added by Ozan after Phase 14 was merged (2026-11-11).

**Goal:** Codex agents work next to Claude Code agents: one team can mix both, and every agent
stays sandboxed with the credential outside its container.

**Scope** (see `docs/plans/phase-15.md`)
- Start with a spike against the installed Codex CLI: hook payloads and states, usage reporting,
  resume, and whether a Codex agent runs in the network-less sandbox with the credential injected
  by the daemon. If the real credential would have to be inside the container, stop and ask Ozan.
- A Codex adapter behind the existing agent interface.
- Roles and projects choose the agent CLI and model (for example a Claude Tech Lead with Codex
  workers, or a Codex reviewer).
- The team MCP tools work for Codex agents.
- Usage per vendor.

**Definition of Done**
- [x] In a sandboxed team with a Claude lead, a Codex worker implements a sub-task end to end:
      `TestCodexTeamEndToEnd` (a haiku Tech Lead assigns, a Codex worker commits and reports through
      the team MCP tools, its branch is imported, its usage is metered as OpenAI's);
      `TestSandboxedCodexWorker` with a fake Codex in a real Podman worker.
- [x] A Codex reviewer reviews a Claude worker's branch: the GitHub e2e with
      `AO_E2E_REVIEWER_CLI=codex` (the review on the playground's pull request came from a Codex
      reviewer); `TestSandboxedCodexReviewer` with the GitHub fake.
- [x] Restart behaviour is tested: `TestCodexTeamEndToEnd` SIGKILLs the daemon with an idle Codex
      worker, which comes back on its own Codex session and recalls a codeword from before;
      `TestSandboxedCodexRestore`. Limits are in ADR 0049 (Codex starts a session only with its
      first prompt; prompt-less agents are marked idle once their TUI is up).
- [x] `make check`, `make ui-test` (79), `make sandbox-test` and `make e2e` (host, sandbox, team,
      workflow, multi-project, Codex team, and the GitHub run with a Codex reviewer) pass.

**Backlog** (from Phase 15)
- Codex agents in host mode (refused today: the credential could not be kept from a host process).
- Prices for OpenAI models (Codex usage is unpriced unless `prices.yaml` has the model), and a
  conservative count for Responses streams broken off before their final event.
- A repository's own `.codex/config.toml` is loaded for the trusted workspace; consider mounting an
  empty one over it.
- Several goals at once in one project, when real use shows the need. Today a project runs one
  goal at a time (one Tech Lead, architect and QA, ADR 0017); the Board says so on the other goals'
  cards. Two goals can already run in parallel as two projects (Phase 14).

---

## Phase 16: Autonomous team

Added by Ozan after Phase 15 was merged (2026-11-11), from running a real sprint (PulseGo Sprint 3)
where he had to step in about ten times; each time the Tech Lead could have decided itself if it had
seen what he saw and had the tools to act.

**Goal:** A goal runs from start to "ready for my review" without the user relaying anything.

**Scope** (see `docs/plans/phase-16.md`)
- Exit reasons: why an agent exited (out of memory, signal, exit code) in plain words, for the lead
  and the UI.
- Self-recovery: an agent that dies unexpectedly is resumed once automatically; a second failure
  goes to the lead with the reason.
- Lead powers: resume or stop its own workers; take back an in_progress sub-task from an exited
  worker and assign it again.
- No lost messages: a message to an agent that is not running is queued and delivered when it
  resumes.
- A stall watchdog (daemon code): a goal whose agents are all idle, with nothing waiting on the user,
  wakes its lead with a status summary.
- An exiting worker's uncommitted work is saved automatically.
- Access decisions by the lead, per project (`user`, `lead`, `lead within a host policy`); approvals
  scoped to the agent or the project, surviving a resume.
- The lead decides everything reversible itself and asks the user only for merges and pushes,
  secrets, pack changes and irreversible steps (its role prompt).
- A worker memory limit per pack or project, with a clear message when a worker hits it.
- Inbox: grouped by goal and agent, answered items collapsed, briefings apart.
- Smaller findings: `storage.googleapis.com` and `make` in the go pack; memory proposals must not
  describe effects they do not have.

**Definition of Done**
- [x] An OOM-killed sandboxed worker shows "out of memory" (with its limit) in the UI and in a message to
      its lead, from a real kernel OOM kill: `TestSandboxedOOMKillIsExplained` (and the running watch,
      `TestSandboxedChildOOMIsReported`), live in `TestAutonomousTeamEndToEnd`; host agents show the
      signal or exit code (`TestUnexpectedExitResumedOnce`). Podman's `State.OOMKilled` stays false here
      (measured), so sandbox-init's exit report is the source (ADR 0051).
- [x] An unexpected exit is resumed once with a message saying what happened; a second one reaches the
      lead with the reason and is not resumed: `TestUnexpectedExitResumedOnce`, live with two OOM kills.
- [x] The lead resumes and stops its workers and takes back a sub-task from an exited worker; the next
      worker continues from the previous branch (and keeps its approvals): `TestLeadResumesAndStopsItsWorkers`,
      `TestReclaimTaskContinuesFromTheBranch`; live, the lead took both paths in different runs.
- [x] A message to a stopped agent is delivered when it resumes: `TestMessageToStoppedWorkerWaits`,
      `TestRelaunchGetsMessagesQueuedWhileEnding`, live (into the resumed session).
- [x] The stall watchdog wakes the lead with who is idle, the open tasks and the undelivered messages,
      and stays quiet while something waits on the user: `TestStallWatchdogWakesTheLead`,
      `TestStallWatchdogWaitsForTheUser`, `TestStallWatchdogIgnoresReportedGoals`, live after 35 s.
- [x] A worker's uncommitted work is committed and imported when it exits, sandboxed and on the host:
      `TestSandboxedRemoveKeepsDirtyAndSavesWork`, `TestExitSavesUncommittedWork` (and not when the
      repository's git config would run a command, `TestHostAutosaveRefusesAgentGitConfig`).
- [x] A project can let its lead decide access requests, optionally within a host policy, with a recorded
      reason; approvals for the agent or the project survive a resume; request #4 fixed with a test that
      fails without the fix: `TestLeadDecidesAccess`, `TestLeadProjectScopeNeedsThePolicy`,
      `TestUserDecidesByDefault`, `TestSandboxedGrantSurvivesResume`; live, `example.com` reachable from the
      resumed session.
- [x] The Tech Lead's prompt has the rule on what it decides and what it asks; the assistant's memory
      proposals cannot claim effects: `TestBuiltinPromptRules`.
- [x] `resources.memory` and `worker_memory` set the worker limit; a worker that hits it gets a clear
      message: `TestLoadValid`/`TestLoadInvalid`, `TestProjectSettingsParseAndValidate`,
      `TestSandboxedChildOOMIsReported`.
- [x] The inbox groups by goal and agent, collapses answered items and keeps briefings apart: Vitest
      (`inbox groups`) and Playwright (`autonomy.spec.ts`).
- [x] The go pack allows `storage.googleapis.com` and has `make` (4.4.1-2, pinned); image rebuilt.
- [x] `TestAutonomousTeamEndToEnd` reproduces the incidents with real haiku agents (two real OOM kills,
      a lead-decided access request, a stopped worker with a waiting message, an interrupted worker while
      the lead waits) and the goal reaches review with no question to the user (about 150 s per run).
- [x] `make check` (288 Vitest tests), `make ui-test` (83), `make sandbox-test` (plus `TestWorkerEgress`
      with `AO_SANDBOX_NET=1`) and `make e2e` (host, sandbox, team, workflow, multi-project, Codex team,
      autonomy) pass; the GitHub e2e was not run.

**Backlog** (from Phase 16)
- A resume continues the latest session that finished a turn (ADR 0035): a session killed mid-turn after
  it got a prompt is skipped, so its partial conversation is not continued (its git work is kept).
- Revoking project-wide access grants from the UI.
- Per-sub-task memory limits for one heavy job.
