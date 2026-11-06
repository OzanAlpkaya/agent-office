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
- [ ] Spikes run and their findings are written up (no raw captured data committed).
- [ ] `docs/architecture.md` exists with a component diagram, data flow and domain model.
- [ ] ADRs for: terminal transport, hook ingestion, persistence, UI embedding, MCP transport,
      sandbox approach.
- [ ] Phase 1 DoD refined based on the spike results.

---

## Phase 1: Single agent

**Goal:** Run one Claude Code agent in a PTY and watch it live in the browser, with its state
derived from Claude Code hooks.

**Scope**
- Daemon with `net/http`; agent spawned in a PTY (`creack/pty`).
- Terminal streamed to the browser over WebSocket (`coder/websocket`), rendered with xterm.js;
  input and resize work both ways.
- Hook ingestion endpoint; hook events map to agent state.
- React + Vite UI embedded via `go:embed`; dev workflow with Vite's dev server.

**Definition of Done**
- [ ] `agent-office` starts, serves the UI, and can start one Claude Code session from the UI.
- [ ] The terminal streams live; typing and resizing work; reconnecting a browser tab works.
- [ ] Hook events update the agent state (idle / working / waiting for input / done) in the UI.
- [ ] Unit tests for hook event parsing and the state machine; `go test ./...` passes.
- [ ] A single binary contains the built UI.

---

## Phase 2: Multi-agent + task board

**Goal:** Run several agents at once, each in its own git worktree, managed from a task board.

**Scope**
- SQLite persistence (`modernc.org/sqlite` + `sqlc`) with migrations: agents, tasks, sessions.
- One git worktree + branch per worker agent; lifecycle (create, clean up).
- Task board UI (columns by status); multiple terminals.

**Definition of Done**
- [ ] At least 3 agents run concurrently on one repo, each in its own worktree.
- [ ] Tasks can be created, assigned and moved through statuses; state survives a daemon restart.
- [ ] Worktrees are cleaned up when an agent is removed.
- [ ] Integration tests for the store and the worktree manager.

---

## Phase 3: Isolation & security

**Goal:** Worker agents run with full permissions only inside a sandbox.

**Prerequisite:** Podman installed on the host (installed manually by the owner).

**Scope**
- Rootless Podman container per worker, with only the worktree mounted.
- Egress allowlist (deny by default, allow listed domains).
- Secrets passed as environment variables at launch; never written to disk or logs.
- Agents run with permission prompts disabled only inside the sandbox.

**Definition of Done**
- [ ] A worker cannot read files outside its worktree or reach non-allowlisted domains (tested).
- [ ] Secrets are present in the agent's env and absent from logs, DB and repo (tested).
- [ ] Threat model updated; ADR on Podman vs Claude Code sandbox accepted.

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
- YAML per tool/stack: allowed domains, skills, MCP servers, env var names (names only, no values).
- Schema validation with clear errors; packs applied when launching an agent/sandbox.

**Definition of Done**
- [ ] At least two example packs (e.g. Go, Node/TypeScript).
- [ ] Invalid packs are rejected with actionable error messages (tested).
- [ ] A pack's domains feed the egress allowlist and its env var names feed the secret injection.

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

**Definition of Done**
- [ ] A fresh Linux machine can install and run it by following the README.
- [ ] Tagged release with binaries.
- [ ] A short demo video/GIF and an architecture write-up linking the ADRs.
