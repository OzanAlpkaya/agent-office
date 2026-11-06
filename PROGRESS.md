# Progress

**Current phase:** Phase 3 (Isolation & security), not started; Phase 2 complete, awaiting review

See [docs/plan.md](docs/plan.md) for goals and Definitions of Done.

## Phases

- [x] Phase 0: Research & design
- [x] Phase 1: Single agent (PTY, live terminal, Claude Code hooks)
- [x] Phase 2: Multi-agent + task board (SQLite, worktrees)
- [ ] Phase 3: Isolation & security (rootless Podman, egress allowlist, secrets)
- [ ] Phase 4: Team MCP server + lead agent
- [ ] Phase 5: GitHub loop + pluggable code review
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
