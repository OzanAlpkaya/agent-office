# 0025. UI tests: Vitest for logic, Playwright against a mocked daemon

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

Until Phase 6 the web UI had only a typecheck; the end-to-end test (`make e2e`) drives the daemon,
not the browser. The office view (ADR 0024) adds logic worth unit tests (poses, desk assignment,
art integrity) and behaviour that only a browser shows: a canvas, click targets, frame rate. Real
agents cost tokens and are slow and nondeterministic, so a browser test should not need them.

## Options considered

1. **Vitest for pure modules + Playwright with a mocked daemon.** Pros: Vitest reuses the Vite
   config and runs in a second; Playwright routes HTTP (`page.route`) and WebSockets
   (`page.routeWebSocket`), so a scripted daemon plays snapshots and live updates
   deterministically; real headless Chromium measures rendering. Cons: two dev dependencies and a
   browser download; the mock must follow the API types.
2. **Playwright against a real daemon with a fake agent CLI.** Pros: exercises the real API. Cons:
   needs a daemon build, a repository and a fake CLI that speaks hooks; slower; states are hard
   to script precisely.
3. **Jest + jsdom / React Testing Library.** Cons: no canvas or WebGL, so nothing of the scene is
   tested; a second bundler setup.

## Decision

Option 1. Vitest 5.1 runs `src/**/*.test.ts` in `make check`. Playwright 1.65 (`web/ui-tests/`)
runs against `vite preview` of the built UI with every daemon request answered by a fixture typed
with `src/api.ts`, through the opt-in `make ui-test` (the browser download stays outside the
repository, in `~/.cache/ms-playwright`). The same suite writes screenshots for visual review.
The real daemon is still covered by Go tests and `make e2e`.

## Consequences

- The mocked daemon can drift from the real API; typing the fixtures with `src/api.ts` and going
  through `parseEventMessage` catches shape changes on the UI side, and a live check against the
  real daemon remains part of each UI phase.
- `make check` stays fast and offline; browser tests are opt-in like `make sandbox-test`.
- Later UI phases (dashboard, PWA) get a test harness for free.
