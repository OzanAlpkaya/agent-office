# 0006. Team MCP server: stdio bridge to the daemon over the per-agent socket

- **Status:** Proposed (go-sdk transport over a socket to be confirmed at the start of Phase 4)
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

Option 2, provided the go-sdk can serve a session over an arbitrary `net.Conn`/`io.ReadWriteCloser`
(to be verified in Phase 4; otherwise the bridge speaks streamable HTTP over the unix socket, which
the SDK's handler supports as a plain `http.Handler`). Tool handlers receive the agent identity
from the connection, never from tool arguments. Role checks (e.g. only the lead may `assign_task`)
and input validation happen in deterministic Go code.

## Consequences

- One code path for hooks and MCP: `agent-office` must exist inside worker images (a static binary
  mounted read-only).
- `request_access` never grants anything by itself; it creates an AccessRequest that a human
  approves.
- Pin the go-sdk version (v1.8.0 at the time of writing); its API is still evolving.
