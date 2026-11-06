# Spike A: PTY ↔ WebSocket ↔ xterm.js

Throwaway code. Question: can the daemon run a process in a PTY, stream it to a browser, accept
input and resize, and survive the browser tab going away?

```sh
go test -race ./...        # automated round-trip
go run .                   # then open http://127.0.0.1:7681/?session=demo
```

## Design under test

- A **session** owns one process in a PTY and outlives WebSocket connections.
- Server → browser: raw PTY bytes in **binary** frames. Browser → server: **text** frames with JSON
  `{"type":"input","data":"..."}` or `{"type":"resize","cols":N,"rows":M}`.
- One goroutine reads the PTY, appends to a 256 KiB scrollback ring and fans out to subscribers.
  Appending and broadcasting happen under one lock, and attaching takes the scrollback snapshot
  under the same lock. The result is no gap and no duplicated output between replay and live stream.
- A slow subscriber whose buffer (256 chunks) fills up is **dropped**. A slow browser never
  blocks the PTY, and with it the agent.
- `websocket.Accept` with default options enforces same-origin. Without that, any website open in
  the browser could drive a local shell (cross-site WebSocket hijacking).

## Results (2026-11-06, Go 1.27.3, creack/pty v1.1.24, coder/websocket v1.8.15)

| Check | Result |
|---|---|
| Input reaches the shell, output returns (`echo hello-$((40+2))` → `hello-42`) | pass |
| Resize propagates (`stty size` → `40 120`) | pass |
| Detach + re-attach: scrollback replayed, same shell still alive | pass |
| Process exit → WebSocket normal closure | pass |
| Cross-origin handshake rejected with 403 | pass |
| `-race` clean | pass |
| xterm.js page in a real browser | **not checked**: the browser automation extension wasn't connected. The page is served and the pinned CDN assets return 200. Open it manually to confirm. |

## Findings / open questions for Phase 1

- **Replay of raw bytes is not a screen snapshot.** Raw scrollback works for a shell. For a
  full-screen TUI like Claude Code, replaying bytes into a new terminal of a different size can
  garble the screen, and the trim point can cut an escape sequence in half. Options for Phase 1:
  (a) send a redraw (resize nudge / SIGWINCH) after attach, (b) keep a headless terminal emulator
  server-side and send a serialized screen on attach. Start with (a); measure before building (b).
- Multiple viewers per session work (fan-out). Input from several viewers is not arbitrated, which
  is fine for a single-user tool.
- The daemon must listen on loopback only; remote access is Phase 10's job (Tailscale + auth).
