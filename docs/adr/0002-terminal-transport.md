# 0002. Terminal transport: PTY + WebSocket, sessions outlive connections

- **Status:** Accepted; scrollback saved per session since ADR 0036
- **Date:** 2026-11-06

## Context

Each agent is an interactive TUI (Claude Code). The user must watch it live in the browser, type
into it, resize it, and close or reopen tabs without killing the agent. Several viewers may watch
the same agent. Evidence: `spikes/pty-ws`.

## Options considered

1. **PTY in the daemon + WebSocket** (creack/pty + coder/websocket). Pros: full control, a single
   binary, bidirectional, binary-safe. Cons: we write the session/scrollback logic ourselves.
2. **Server-Sent Events + POST for input.** Pros: simple, and plain HTTP. Cons: text-only (needs
   base64), two channels per terminal, resize/input latency.
3. **External terminal server** (ttyd/gotty) per agent. Pros: ready-made. Cons: an extra process
   and port per agent, harder auth, no integration with state and scrollback.
4. **tmux as the session host.** Pros: sessions survive a daemon restart, and attaching from a
   real terminal is free. Cons: an extra dependency, output capture through `pipe-pane`/control
   mode is clumsier, and tmux's own key bindings and escape handling sit in between.

## Decision

Option 1. One goroutine per PTY reads output into a 256 KiB scrollback ring and fans it out to
subscribers. Snapshot-on-attach and broadcast share one lock, so there is no gap and no duplicate
output. Server → browser frames are raw bytes (binary). Browser → server frames are JSON text:
`input` and `resize`. A slow viewer is disconnected instead of blocking the PTY. The daemon listens
on loopback only, and `websocket.Accept` keeps its default same-origin check (tested).

## Consequences

- Agent sessions die with the daemon. Restart and resume (`claude --resume <session-id>`) is a
  Phase 9 topic. tmux remains the fallback if that turns out to matter more than expected.
- Replaying raw bytes is not a screen snapshot. For a full-screen TUI, Phase 1 starts with a
  redraw nudge on attach (resize / SIGWINCH). A server-side headless terminal emulator is the
  heavier option if needed.
- Input from multiple viewers is not arbitrated (single-user tool).
