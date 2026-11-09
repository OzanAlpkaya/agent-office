# web

React + Vite + TypeScript UI: xterm.js for terminals, PixiJS for the office view.

- `npm run build` writes to `dist/app/`, which `web/embed.go` embeds into the binary (ADR 0005).
  Without a build, the binary serves a placeholder page.
- `npm run dev` starts Vite on http://localhost:5173 and proxies `/api` (HTTP and WebSockets) to
  the daemon. Start the daemon with `--dev` so it accepts that origin.
- API types in `src/api.ts` mirror the Go structs by hand; messages from the events WebSocket
  are validated before use.
- `src/office/` is the Office tab (ADR 0024, ADR 0045): pure modules (`art`, `pose`, `layout`,
  `model`, and from Phase 13 `floor` for the floor plan, `paths` for routes, `errands` for where
  each character should be) and the Pixi scene (`scene`, `textures`) behind a lazy-loaded React
  component (`Office.tsx`).
  The art is drawn in code; see `src/office/ART.md`.

## Tests (ADR 0025)

- `npm test` (in `make check`): Vitest unit tests, `src/**/*.test.ts`.
- `npm run ui-test` (`make ui-test`, opt-in): Playwright against `vite preview` of the build, with
  a mocked daemon (`ui-tests/daemon.ts`); no daemon or tokens needed. Install the browser once
  with `npx playwright install chromium-headless-shell` (it goes to `~/.cache/ms-playwright`).
  Screenshots are written to `test-results/screens/`.
