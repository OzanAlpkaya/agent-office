# Progress

**Current phase:** Phase 1 (Single agent), not started; Phase 0 complete, awaiting review

See [docs/plan.md](docs/plan.md) for goals and Definitions of Done.

## Phases

- [x] Phase 0: Research & design
- [ ] Phase 1: Single agent (PTY, live terminal, Claude Code hooks)
- [ ] Phase 2: Multi-agent + task board (SQLite, worktrees)
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
