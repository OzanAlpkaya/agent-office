# web

React + Vite + TypeScript UI (xterm.js for terminals; PixiJS arrives in Phase 7).

- `npm run build` writes to `dist/app/`, which `web/embed.go` embeds into the binary (ADR 0005).
  Without a build, the binary serves a placeholder page.
- `npm run dev` starts Vite on http://localhost:5173 and proxies `/api` (HTTP and WebSockets) to
  the daemon. Start the daemon with `--dev` so it accepts that origin.
- API types in `src/api.ts` mirror the Go structs by hand; messages from the events WebSocket
  are validated before use.
