# 0005. Web UI: React + Vite build embedded with go:embed; Vite dev server in development

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

The UI is React + Vite + TypeScript (xterm.js, later PixiJS). Releases must be a single binary.
Development needs fast UI reloads without rebuilding Go.

## Options considered

1. **Embed `web/dist` with `go:embed`; in development, run the Vite dev server and proxy `/api` and
   `/ws` to the daemon.** Pros: a single binary for release, hot reload for development. Cons:
   two processes in development; a build order to respect.
2. **Serve UI files from disk next to the binary.** Pros: no embed step. Cons: no longer a single
   binary; paths and versions can drift.
3. **Server-rendered UI (templ/htmx).** Pros: one toolchain. Cons: a poor fit for xterm.js and a
   PixiJS scene, and less relevant for the portfolio.

## Decision

Option 1. A small Go package in `web/` embeds `all:dist`. Vite builds into `web/dist/app/`
(gitignored); `web/dist/.gitkeep` is committed so the embed pattern always matches. Without a build,
the package serves a built-in placeholder page, so `go build` and `go test` work without Node.
(Amended in Phase 1: the first draft put a placeholder `index.html` into `dist/` itself, which a
Vite build would overwrite or delete.)
A `Makefile` target builds the UI first, then the binary. In development, `vite` proxies `/api` and
`/ws` to `127.0.0.1:<daemon port>`, and the daemon's origin check allows the Vite dev origin only
when started with `--dev`.

## Consequences

- Release builds need Node to produce the real UI; CI must run `npm ci && npm run build` before
  `go build`.
- API types are written twice (Go and TS) at first. Generating TypeScript types from Go is a later
  decision, once the API settles.
- xterm.js comes from npm and is bundled; the spike's CDN import is not used in the product.
