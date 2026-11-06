# Library API notes (Phase 0)

Verified on 2026-11-06 by reading the module source in the Go module cache. Docs and blog posts
were used only as a starting point; several of them were out of date. Pin these versions and check
again when a phase starts using a library.

| Library | Version | Import path |
|---|---|---|
| creack/pty | v1.1.24 (2024-10) | `github.com/creack/pty` |
| coder/websocket | v1.8.15 | `github.com/coder/websocket` |
| MCP Go SDK | v1.8.0 | `github.com/modelcontextprotocol/go-sdk/mcp` |
| modernc sqlite | v1.61.1 | `modernc.org/sqlite` (driver name `"sqlite"`) |
| goose (migrations) | v3.29.0 | `github.com/pressly/goose/v3` |
| go-github | v93.0.0 | `github.com/google/go-github/v93/github` |
| xterm.js | 6.0.0 (+ `@xterm/addon-fit` 0.11.0) | npm `@xterm/xterm` |

## creack/pty

```go
ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80}) // sets Setsid + controlling tty
err = pty.Setsize(ptmx, &pty.Winsize{Rows: r, Cols: c})
```

- There is also a `/v2` module (v2.0.1, 2023-10). It is **older** than v1.1.24 (2024-10), and
  `go get @latest` picks v1. Use v1.1.24.
- `Read` on the master returns an error (EIO on Linux) once the child exits. That is the signal to
  call `cmd.Wait()`. Spike A relies on this.

## coder/websocket

```go
c, err := websocket.Accept(w, r, nil)              // same-origin enforced by default
c, resp, err := websocket.Dial(ctx, url, opts)
typ, data, err := c.Read(ctx)                      // whole message; default read limit 32 KiB (SetReadLimit)
err = c.Write(ctx, websocket.MessageBinary, p)
c.Close(websocket.StatusNormalClosure, "")         // handshake; CloseNow() is abrupt
websocket.CloseStatus(err)                         // extract close code from a Read error
```

- `AcceptOptions.OriginPatterns` allows extra origins (the scheme must match since v1.8.14).
  Never use `InsecureSkipVerify` for the daemon. Cross-origin rejection is tested in Spike A.

## MCP Go SDK

```go
s := mcp.NewServer(&mcp.Implementation{Name: "agent-office", Version: v}, nil) // (impl, *ServerOptions)
mcp.AddTool(s, &mcp.Tool{Name: "create_task", Description: "..."}, handler)    // generic: In/Out structs -> JSON schema
err := s.Run(ctx, &mcp.StdioTransport{})
h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return s }, nil)
```

- `AddTool` panics on an invalid tool definition, so register tools at startup and cover them in tests.
- `NewStreamableHTTPHandler` takes a per-request server factory. That fits giving each agent its
  own identity: the factory can look at an auth token on the request and return a server for that agent.
- v1.8.0 changes behaviour for the 2026-07-28 protocol revision (JSON-RPC errors instead of plain
  HTTP 400s). Pin the version.

## modernc.org/sqlite + sqlc + goose

```go
import _ "modernc.org/sqlite"
db, err := sql.Open("sqlite", "file:agent-office.db?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate")
```

- The DSN shorthand keys (`_journal_mode`, `_busy_timeout`, `_foreign_keys`, `_synchronous`,
  `_txlock`, ...) are validated and applied on **every pooled connection**. `PRAGMA` sent via
  `db.Exec` would only affect one connection.
- sqlc: `engine: "sqlite"` with the default `database/sql` output works with any driver, modernc
  included. Run sqlc as a pinned dev tool (`go tool` directive), not as a runtime dependency.
- goose accepts `"sqlite3"`/`"sqlite"` as the dialect; migrations can be embedded with `go:embed`.

## go-github v93

```go
client, err := github.NewClient(github.WithAuthToken(os.Getenv("GITHUB_TOKEN"))) // options + error since v9x
pr, _, err := client.PullRequests.Create(ctx, owner, repo, github.CreatePullRequest{
    Title: github.Ptr("..."), Head: "phase-1/x", Base: "main",
})
```

- `NewClient(*http.Client)` and `client.WithAuthToken(...)`, which most blog posts show, are the
  **old** API. In v93 `WithAuthToken` is a client option and the token is only sent to the
  configured API origins.
- The request type is `CreatePullRequest`, passed by value. It is not `NewPullRequest`.
- `Create` is not idempotent. Before retrying, look for an existing PR for the branch (handle the
  422 "already exists" response).
