# 0006. Team MCP server: stdio bridge to the daemon over the per-agent socket

- **Status:** Accepted (2026-11-07, Phase 4: go-sdk v1.8.0 serves a session over any reader/writer
  with `mcp.IOTransport`; MCP gets its own per-agent socket, see Decision)
- **Date:** 2026-11-06

## Context

Agents coordinate through a team MCP server (`create_task`, `assign_task`, `send_message`,
`report_done`, `request_access`). The server lives in the daemon, because it changes engine state.
Every call must be attributed to the calling agent and authorized by its role. Workers will run in
containers with no network path to the host (Spike E), but they can reach a mounted unix socket.

## Options considered

1. **Streamable HTTP served by the daemon** (`mcp.NewStreamableHTTPHandler` with a per-request
   server factory, identity from a bearer token). Pros: no extra process; the SDK supports it
   directly. Cons: containers on the internal network cannot reach the host. Exposing it means an
   extra listener or routing through the proxy, plus a token to manage.
2. **Stdio MCP server = `agent-office mcp`**, a thin bridge that pipes stdio to the agent's unix
   socket. The daemon serves MCP on that connection. Pros: the same path and identity model as
   hooks (ADR 0003), works with `--network none`, no tokens (socket = identity). Cons: one bridge
   process per agent session; per the Claude Code docs, stdio MCP servers run outside its built-in
   sandbox, which matters less once the whole agent is in a container.

## Decision

Option 2. Verified at the start of Phase 4 by reading go-sdk v1.8.0: `mcp.IOTransport{Reader,
Writer}` runs newline-delimited JSON-RPC over any `io.ReadCloser`/`io.WriteCloser`, so the daemon
calls `Server.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)` for each accepted
unix-socket connection. Streamable HTTP over the socket was not needed.

**MCP gets its own socket** (`mcp.sock`, mode 0600, next to the agent's hook socket; bind-mounted
into sandboxed workers at `/run/agent-office/mcp.sock`), not the hook socket: a hook is one request
per connection (read to EOF, reply, close), while an MCP session is one long-lived stream. Mixing
both on one socket would need a protocol prefix for no gain. Claude Code starts the bridge from a
daemon-generated `--mcp-config` file (server name `agent-office`, so tools are
`mcp__agent-office__<tool>`); the socket path is passed in the server's `env`. One `mcp.Server` per
agent; at most 4 concurrent connections per agent; frames are limited to 1 MiB. Tool handlers receive the agent identity
from the connection, never from tool arguments. Role checks (e.g. only the lead may `assign_task`)
and input validation happen in deterministic Go code.

## Consequences

- One code path for hooks and MCP: `agent-office` must exist inside worker images (a static binary
  mounted read-only).
- `request_access` never grants anything by itself; it creates an AccessRequest that a human
  approves.
- Pin the go-sdk version (v1.8.0 at the time of writing); its API is still evolving.
