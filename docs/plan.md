# agent-office: roadmap

Each phase has a goal, a scope and a Definition of Done (DoD). Every phase starts with a short
approved plan and ends with an updated `PROGRESS.md` and a plain-language summary. Later phases are
intentionally coarser; their DoD is refined at the start of the phase.

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

**Scope**
- MCP server (`modelcontextprotocol/go-sdk`) with tools: `create_task`, `assign_task`,
  `send_message`, `report_done`, `request_access`.
- Lead agent role that breaks a goal into tasks and assigns them.
- Message delivery to an agent on its Stop hook (when it is idle).
- `request_access` goes to a human approval queue in the UI.

**Definition of Done**
- [ ] Given a goal, the lead creates and assigns tasks that workers complete end-to-end.
- [ ] Messages reach the recipient after its next Stop event, exactly once.
- [ ] Access requests need human approval in the UI; nothing is granted automatically.
- [ ] Tests for every MCP tool (input validation, authorization by agent role).

---

## Phase 5: GitHub loop + pluggable code review

**Goal:** Finished work becomes a pull request and gets reviewed automatically.

**Scope**
- go-github client using a token from env; git push over SSH.
- Open PRs from worker branches; read review comments and checks.
- Pluggable reviewer interface: CodeRabbit or an internal reviewer agent.
- Review feedback goes back to the worker as a task.

**Definition of Done**
- [ ] A completed task results in a PR on a test repository.
- [ ] Both reviewer implementations work behind one interface.
- [ ] Review comments are turned into follow-up work for the original worker.
- [ ] Merging stays a human action.

---

## Phase 6: Stack packs

**Goal:** Configure agents per tech stack with declarative YAML packs.

**Scope**
- YAML per tool/stack: allowed domains, skills, MCP servers, env var names (names only, no values),
  and the **toolchain**, i.e. which worker image a project's agents get. This replaces Phase 3's single
  image, which carries only the playground's Go toolchain (e.g. a Go pack with a C toolchain for
  `-race`, a Node/TypeScript pack with Node and npm).
- Schema validation with clear errors; packs applied when launching an agent/sandbox.

**Definition of Done**
- [ ] At least two example packs (e.g. Go, Node/TypeScript).
- [ ] Invalid packs are rejected with actionable error messages (tested).
- [ ] A pack's domains feed the egress allowlist and its env var names feed the secret injection.
- [ ] A pack selects the worker image (toolchain); projects without a pack get the default image.

---

## Phase 7: Pixel office UI

**Goal:** A pixel-art office view where agents are characters whose activity reflects their state.

**Scope**
- PixiJS scene alongside the task board; original art assets only.
- Agent state (working, waiting, idle, reviewing) shown through animations; click to open a terminal.

**Definition of Done**
- [ ] The office shows every running agent with live state.
- [ ] Clicking an agent opens its terminal/task.
- [ ] Runs smoothly with 10 agents; all assets are original or properly licensed.

---

## Phase 8: Observability & cost

**Goal:** See what agents are doing and what they cost.

**Scope**
- Token usage and cost parsed from Claude Code session files.
- Per agent / task / session metrics; structured logs with correlation ids.
- Dashboard in the UI.

**Definition of Done**
- [ ] Token usage per agent and task is shown and matches the session files (tested with fixtures).
- [ ] Cost estimates use a configurable price table.
- [ ] Logs can be followed across daemon, agent and hook events via a correlation id.

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

---

## Phase 10: Remote access

**Goal:** Use agent-office from a phone or another machine over Tailscale.

**Scope**
- Installable PWA; responsive UI.
- Access over Tailscale only; authentication for the UI/API.

**Definition of Done**
- [ ] The PWA installs on a phone and shows the board, terminals and approvals.
- [ ] The daemon is not reachable outside localhost/Tailscale; unauthenticated requests are rejected.

---

## Phase 11: Portfolio polish

**Goal:** Make the project easy to understand, run and evaluate.

**Scope**
- README with screenshots/GIF, architecture overview, quick start.
- Release builds, demo script, write-up of key decisions.
- Pin Debian package versions in the worker image (reproducible builds); Phase 3 pins only the
  base image digest and the Go download.

**Definition of Done**
- [ ] A fresh Linux machine can install and run it by following the README.
- [ ] Tagged release with binaries.
- [ ] A short demo video/GIF and an architecture write-up linking the ADRs.
