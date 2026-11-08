# agent-office: roadmap

Each phase has a goal, a scope and a Definition of Done (DoD). Every phase starts with a short
approved plan and ends with an updated `PROGRESS.md` and a plain-language summary. Later phases are
intentionally coarser; their DoD is refined at the start of the phase.

**Order** (decided 2026-11-08): phases run 0–8, then 12, 9, 10, 11, 13. Numbers are kept, so
references in ADRs and plans stay valid.

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

**Scope**
- Project memory (decisions, conventions) available to agents.
- Resume agents and tasks after a restart; session history.

**Definition of Done**
- [ ] A restarted daemon restores agents, tasks and history.
- [ ] Agents can read relevant project memory at task start.
- [ ] Memory is inspectable and editable by the user.

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- Make the "Resume this session" line meaningful: resume a stopped agent's session from
  agent-office (same agent, workspace and sandbox), then drop the Phase 8 note that explains it
  under exited agents' terminals.

**Backlog** (from the Phase 8 review, 2026-11-08)
- After a daemon restart, a sandboxed agent's card switches from "user settings off" to "user
  settings on". The setting does not apply to sandboxed agents (ADR 0003), so the label is wrong,
  not the behaviour: restore the value the agent ran with (or hide the label for sandboxed agents)
  when agents are loaded from the database.

---

## Phase 10: Remote access

**Goal:** Use agent-office from a phone or another machine over Tailscale.

**Scope**
- Installable PWA; responsive UI.
- Access over Tailscale only; authentication for the UI/API.

**Definition of Done**
- [ ] The PWA installs on a phone and shows the board, terminals and approvals.
- [ ] The daemon is not reachable outside localhost/Tailscale; unauthenticated requests are rejected.

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- Copy and paste in the browser terminal: Ctrl+C must keep interrupting the agent, Ctrl+Shift+C
  opens Chrome's devtools, and a mouse selection is not copied. Support Ctrl+Shift+C / Ctrl+Shift+V
  and copy on selection, like a normal terminal (xterm.js key handler and selection API; the
  clipboard API needs a secure context, which loopback is). Needed on a phone too.

---

## Phase 11: Portfolio polish

**Goal:** Make the project easy to understand, run and evaluate.

**Scope**
- README with screenshots/GIF, architecture overview, quick start.
- Release builds, demo script, write-up of key decisions.
- Pin Debian package versions in the worker image (reproducible builds); Phase 3 pins only the
  base image digest and the Go download.
- Closing keywords in agents' commit messages (`fixes #N`): Phase 5 neutralizes them in PR titles,
  bodies and reviews, but pushes commits as they are, so merging such a PR closes the issue. Flag
  them at push time (like flagged commits) or have the daemon rewrite the PR's merge message
  (ADR 0020, residual).

**Definition of Done**
- [ ] A fresh Linux machine can install and run it by following the README.
- [ ] Tagged release with binaries.
- [ ] A short demo video/GIF and an architecture write-up linking the ADRs.

**Backlog** (from Ozan's end-to-end test, 2026-11-08)
- The worker image has no `python3`; agents reach for it to edit files and fail. Either add
  `python3` to the base image (pinned with the other Debian packages above) or tell agents in the
  packs' skills that it is not available and which tools are.

---

## Phase 12: Team workflow and roles

Added after Ozan's end-to-end test (2026-11-08) and expanded by him before planning (2026-11-08)
into "Team workflow and roles". Appended rather than inserted so that the phase numbers ADRs refer
to stay valid.

**Goal:** A team run that holds up on real work: roles are data, an Architect agrees the plan with
the user, a Tech Lead executes it in dependency order and stays for follow-ups, QA integrates and
tests the result, an Assistant keeps the user briefed, parallel work does not silently collide,
and every agent reaches the user or the agent above it.

**Scope** (see `docs/plans/phase-12.md`, sub-phases 12.1–12.7)
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
- [ ] Roles are YAML (built-in and user-owned); a new worker-behaviour role (e.g. `docs`) runs
      without code changes, with its own prompt, model, tools and workspace mode.
- [ ] A lead that reported its goal stays idle until the goal is marked done or stopped, and
      takes follow-up work typed into its terminal.
- [ ] An Architect's plan is shown, edited and approved in the UI; approval commits
      `docs/specs/<goal>.md` and creates the sub-tasks; the Tech Lead starts on it.
- [ ] A lead can make a sub-task depend on others; it starts only after they are done, from
      their work; overlapping files without a dependency are refused.
- [ ] Conflicts with the default branch and other open agent branches are detected on
      `report_done` and shown in the UI (tested with two branches editing the same lines).
- [ ] After a merge, a worker whose branch conflicts can be relaunched to resolve it.
- [ ] QA merges the reported branches, reports pass or fail with bugs as sub-tasks, and gates
      the goal report.
- [ ] Any agent without someone above it can message the user; the message appears on its task
      card and in the inbox, and the user's reply reaches the agent.
- [ ] The Assistant gives a briefing from daemon data and proposes memory that reaches other
      agents only once the user accepts it.

**Backlog** (from the Phase 8 review, 2026-11-08)
- Investigate the intermittent `TestTeamAuthorization` failure (`internal/engine`): it failed once
  in a full `make check` under load during Phase 8, then passed 15 of 15 on its own and in every
  later full run. Reproduce under load (e.g. `go test -race -count=50 -run TestTeamAuthorization`
  next to other packages, or `-cpu 1,2,8`), capture the failure message and fix the race or the
  test's timing assumption. Placed here because Phase 12 reworks the team code it tests.

---

## Phase 13: Office v2

Design backlog from Ozan (2026-11-08); a late phase, or part of Phase 11.

**Goal:** The office tells more of the story: characters move between places that mean
something, in an office with rooms.

**Scope**
- Characters walk instead of only sitting at desks: to the lead's whiteboard to report
  (`report_done`), to a coffee machine when idle, to the review room when reviewing.
- Separate rooms: a lead's room, a review/meeting room and a workroom; desks still stay assigned
  (ADR 0024).
- Keep the Phase 7 performance budget: walking means continuous animation, so render on demand
  stays the rule (frames only while someone moves), with a measured budget for a busy office;
  `prefers-reduced-motion` moves characters without walking.

**Definition of Done**
- [ ] Characters walk between their desk and the places their events point to, and back.
- [ ] The office has at least three rooms with a clear purpose.
- [ ] An office where nothing moves still renders nothing; with 10 agents walking, a drawn frame
      stays within the Phase 7 budget (p95 under 8 ms in the UI tests).
- [ ] All new art is original or properly licensed.
