# agent-office

A local tool that runs a team of AI coding agents (Claude Code, Codex, ...) on a git repository.
A lead agent plans the work and assigns tasks; each worker agent gets its own git worktree and
sandbox; a web UI shows a task board, live terminals and, later, a pixel-art office where you can
watch the team at work.

> **Status:** early development. Phase 1 works: one Claude Code agent in a live browser terminal,
> with its state tracked from hooks. Multi-agent support starts in Phase 2.

## Quick start (Linux)

Requires Go 1.27+, Node 26+ and Claude Code on `PATH`.

```sh
make build
./bin/agent-office serve --repo ~/path/to/a/git/repo --model haiku
# open http://127.0.0.1:7680 and click "Start agent"
```

Configuration flags and `AGENT_OFFICE_*` variables are listed in `.env.example` and
`agent-office serve -h`. The daemon only listens on loopback. Agents run on the host with Claude
Code's normal permission prompts. Sandboxed full-permission agents come in Phase 3.

## Planned features

- Live agent terminals in the browser (PTY → WebSocket → xterm.js)
- Agent state from Claude Code hooks (working, waiting for input, done)
- Multi-agent task board with one git worktree per worker
- Sandboxed workers (rootless Podman, egress allowlist, secrets as env vars)
- Team MCP server for coordination and a lead agent that delegates
- GitHub PR loop with pluggable code review
- Stack packs, cost tracking, memory, remote access over Tailscale

See the full roadmap in [docs/plan.md](docs/plan.md) and progress in [PROGRESS.md](PROGRESS.md).

## Stack

- **Daemon:** Go (`net/http`, `coder/websocket`, `creack/pty`, MCP Go SDK, SQLite + sqlc,
  go-github, `log/slog`). Shipped as a single binary.
- **UI:** React, Vite, TypeScript, xterm.js (PixiJS later), embedded into the binary.
- **Platform:** Linux first.

Architecture decisions are recorded in [docs/adr/](docs/adr/).

## Inspiration

Ideas inspired by [jsm-hq](https://github.com/jsmastery-pro/jsm-hq) and
[AgentSpace](https://muratify.com). No code, names or assets are reused.

## License

[MIT](LICENSE)
