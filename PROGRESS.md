# Progress

**Current phase:** Phase 6 (Stack packs), not started; Phase 5 merged

See [docs/plan.md](docs/plan.md) for goals and Definitions of Done.

## Phases

- [x] Phase 0: Research & design
- [x] Phase 1: Single agent (PTY, live terminal, Claude Code hooks)
- [x] Phase 2: Multi-agent + task board (SQLite, worktrees)
- [x] Phase 3: Isolation & security (rootless Podman, egress allowlist, secrets)
- [x] Phase 4: Team MCP server + lead agent
- [x] Phase 5: GitHub loop + pluggable code review
- [ ] Phase 6: Stack packs
- [ ] Phase 7: Pixel office UI
- [ ] Phase 8: Observability & cost
- [ ] Phase 9: Memory & persistence
- [ ] Phase 10: Remote access (PWA over Tailscale)
- [ ] Phase 11: Portfolio polish

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
