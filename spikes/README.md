# Spikes

Throwaway experiments from Phase 0. Each folder is its own Go module (dependencies stay out of
the root `go.mod`) and is never imported by production code. Output with captured data goes to
each spike's `out/` folder, which is gitignored.

| Spike | Question | Result |
|---|---|---|
| [pty-ws](pty-ws/) | PTY ↔ WebSocket ↔ xterm.js with re-attach | Works; ADR 0002 |
| [hooks](hooks/) | Claude Code hook events, payloads, transports, Stop injection | ADR 0003 |
| [session-files](session-files/) | Transcript format, token usage and cost | Input for Phase 8 |
| [sandbox](sandbox/) | Rootless Podman vs Claude Code's sandbox | ADR 0007 |
