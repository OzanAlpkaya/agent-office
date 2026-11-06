# 0001. Go for the daemon

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

agent-office runs a local daemon that supervises a team of AI coding agents. At any time it may
hold many long-lived, concurrent streams: one PTY per agent, one or more WebSocket connections per
terminal, hook events from every agent, an MCP server, GitHub API calls and SQLite writes. It is a
single-user tool installed on a developer's Linux machine, so installation must be trivial.

It is also a portfolio project. The author's existing work is mostly TypeScript/Node and Python.

## Options considered

1. **Go**
   - Pros: goroutines and channels fit many concurrent PTY/WebSocket streams with low overhead;
     compiles to a single static binary with the web UI embedded via `go:embed`; mature libraries
     for every building block (`creack/pty`, `coder/websocket`, `modernc.org/sqlite` without cgo,
     `google/go-github`); an official MCP SDK (`modelcontextprotocol/go-sdk`); strong standard
     library (`net/http`, `log/slog`).
   - Cons: more verbose than TypeScript; the frontend stays in TypeScript, so there are two
     toolchains; less familiar to the author than Node.
2. **TypeScript / Node**
   - Pros: one language across daemon and UI; the author's strongest stack; official MCP SDK;
     `node-pty` exists.
   - Cons: `node-pty` is a native addon (build issues); no single static binary without bundlers
     like `pkg`/SEA, which have caveats; one event loop makes CPU-heavy work and isolation between
     streams harder; adds little portfolio diversity.
3. **Python**
   - Pros: fast to prototype; official MCP SDK; good for LLM tooling.
   - Cons: packaging and distribution are painful for a daemon; asyncio PTY handling is clumsy;
     higher memory per process; the slowest runtime of the options.
4. **Rust**
   - Pros: best performance and memory safety; single static binary; good async ecosystem
     (tokio, portable-pty).
   - Cons: slowest iteration speed and steepest learning curve for a long solo project; the MCP SDK
     ecosystem is less mature than Go's official SDK; borrow checker friction for an
     orchestration-heavy, I/O-bound service where Go is good enough.

## Decision

Use **Go** for the daemon. The workload is I/O-bound concurrency over many long-lived streams,
which is Go's sweet spot. A single static binary with the UI embedded makes local installation
trivial. The official MCP Go SDK covers the team MCP server. Go also adds a systems-oriented
language to a portfolio that is otherwise TypeScript/Python.

The web UI stays React + Vite + TypeScript.

## Consequences

- Two toolchains (Go + Node) for development; the build has to produce the UI before `go build`
  embeds it (details in a later ADR).
- `sqlc` adds a code-generation step for database queries.
- No shared types between backend and frontend by default; API types must be kept in sync (e.g.
  via a schema or generated TypeScript types; decided later).
- Pure-Go SQLite (`modernc.org/sqlite`) keeps the build cgo-free and the binary static.
