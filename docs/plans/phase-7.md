# Phase 7 plan: Pixel office UI

- **Status:** Done on `phase-7/pixel-office`, waiting for Ozan's review (not merged). Written 2026-11-08 with the recommended option
  for every open question and executed straight away, as Ozan asked ("write the Phase 7 plan using
  your recommended options, then execute it immediately without asking me anything"). Every
  decision is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 7. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites:** none for the office itself. The opt-in UI tests download Playwright's headless
  Chromium once (`npx playwright install chromium-headless-shell`, about 125 MB, into
  `~/.cache/ms-playwright`, outside the repo).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

An **Office** tab next to the task board shows the team as pixel-art characters at desks. Every
agent the daemon knows has a desk; its character's pose and animation follow its live state and
role (typing while working, a raised hand while waiting for the user, leaning back while idle, a
magnifier while reviewing, an empty desk once exited). Clicking a character opens its terminal.
It stays smooth with 10 agents. Every sprite is original: drawn in code from small pixel maps, so
there is no third-party art to license.

The office is a view of data the UI already has (`/api/events`). It changes nothing in the daemon:
no new endpoint, no migration, no new authority. Agent-written text it shows (task titles, tool
names) is rendered as text by React, as everywhere else.

## Out of scope

- Moving characters between rooms, pathfinding, walking animations between desks.
- Editing the office layout, user-chosen avatars, themes, sound.
- Drag and drop of tasks onto agents, or any action other than opening an agent's view.
- Showing cost or token usage (Phase 8).
- Mobile layout (Phase 10); the office scales down but is built for a desktop window.

## Approach

1. **Rendering: PixiJS 8.22** (`pixi.js`, as named in the project stack), one `Application` per
   mounted office, `preference: ["webgl", "canvas"]` so a machine without WebGL still renders
   through Pixi's canvas renderer. Pixel-perfect: textures use nearest scaling, the world is drawn
   at a whole-number scale that fits the pane (`roundPixels`), `ticker.maxFPS = 30` (the animations
   run at 2 to 8 frames per second; 60 fps would only burn CPU in a tool that stays open all day).
   The office is **lazy-loaded** (`import()`), so the board does not download Pixi, and destroyed
   when the tab is left.
2. **Art: original pixel maps in TypeScript** (`web/src/office/art.ts`). Each sprite is a list of
   strings, one character per pixel, with a palette; a few functions turn them into canvas
   textures once per page. Characters are 16×20 px, desks 32×16, tiles 16×16. Per-agent variety
   (skin, hair, hair style) comes from a hash of the agent id; the shirt colour comes from the role,
   matching the UI's existing role colours (lead violet, worker blue, reviewer teal). No downloaded
   assets, no fonts: labels are HTML (below). `web/src/office/ART.md` records that the art is
   original to this repository and how to add sprites.
3. **Poses (pure, tested).** `poseOf(agent, pendingAccess)`:

   | Agent | Pose | Animation |
   |---|---|---|
   | `starting` | `booting` | seated, monitor booting (blinking cursor) |
   | `idle` | `idle` | leaning back, slow breathing, "…" bubble |
   | `working`, worker or lead | `typing` | hands on the keyboard, code scrolling on the monitor |
   | `working`, reviewer | `reviewing` | holding a magnifier over a page, page lines highlighted |
   | `waiting_permission`, or a pending access request | `waiting` | hand raised, blinking "!" bubble (amber) |
   | `exited` | `away` | empty chair, monitor off, dim nameplate |

   The tool name (`activity`) is shown in the nameplate tooltip. The lead's desk sits at the front
   next to a **whiteboard** with one sticky note per sub-task of its goal, coloured by task status,
   so the plan's progress is visible at a glance.
4. **Layout (pure, tested).** `assignDesks(previous, agents)` keeps every agent at the same desk for
   as long as it exists: new agents take the lowest free desk, removed agents free theirs, nothing
   reshuffles when states change. The lead takes the lead desk when it is free. Desks are laid out
   in rows of four; the office grows by a row when needed, so any number of agents fits.
5. **Messages.** A live message between two agents (ADR 0018) sends a small envelope across the
   office from the sender's desk to the recipient's. Only messages created after the office
   opened animate; history does not.
6. **React integration** (`web/src/office/Office.tsx`). The canvas fills the pane; HTML
   **nameplates** are positioned over the scene (crisp text at any scale, agent id and task title,
   a state dot, a tooltip with role, state and tool). Clicking a character or its nameplate opens
   the agent's view, as clicking an agent card does today. A legend row under the scene lists the
   agents as buttons (keyboard access: the canvas itself is not focusable). With
   `prefers-reduced-motion` the scene shows each pose's first frame and no envelopes. If Pixi
   cannot start at all, the office says so and the legend still works.
7. **Tests.**
   - **Vitest** (new dev dependency) for the pure modules: poses, desk assignment, layout geometry,
     art integrity (every sprite row has the declared width, every pixel's character is in its
     palette), message filtering. Runs in `make check`.
   - **Playwright** (new dev dependency, opt-in `make ui-test`) against the built UI with a
     **mocked daemon**: `page.route` answers `/api/*`, `page.routeWebSocket` plays `/api/events`
     (snapshot, then live updates) and a terminal. Tests: every agent of a 10-agent snapshot gets a
     nameplate with its state; a live state change updates the pose; clicking a character on the
     canvas opens its terminal; the frame rate with 10 and 20 agents (measured with
     `requestAnimationFrame` in headless Chromium) stays at the 30 fps cap, with a floor of 24;
     no console errors. The same suite writes the screenshots used to check the visual result.
   - One **live check** against a real daemon (scratch data dir) with real agents, screenshotted.

## Changes

- `web/src/office/` (new): `art.ts` (pixel maps and palettes), `textures.ts` (maps to Pixi
  textures), `pose.ts`, `layout.ts`, `scene.ts` (the Pixi scene: build, update from the world,
  animate, hit-test), `Office.tsx` (React component, nameplates, legend), `ART.md`, `*.test.ts`.
- `web/src/App.tsx`: an **Office** tab next to Board; `styles.css`: office styles.
- `web/ui-tests/` (new): Playwright config, mocked-daemon fixture, specs; `web/tsconfig.ui.json`.
- `web/package.json`: `pixi.js` 8.22.0; dev: `vitest` 5.1.0, `@playwright/test` 1.65.0; scripts
  `test`, `ui-test`; the typecheck covers the UI tests.
- `Makefile`: `web-test` in `check`; opt-in `ui-test`. `.gitignore`: Playwright output.
- Docs: ADR 0024 (office rendering and art), ADR 0025 (UI test tooling), architecture (office
  view), `web/README.md`, `docs/plan.md` (Phase 7 DoD), `PROGRESS.md`, this file.

## Data / API changes

None. The office reads the existing world: agents, tasks, messages, access requests.

## Risks & edge cases

- **Bundle size.** Pixi adds several hundred kB. Mitigation: lazy-loaded office chunk; the board's
  bundle stays as it is (measured before and after).
- **WebGL in headless Chromium** may be software-rendered (SwiftShader) or missing. Mitigation: the
  canvas renderer fallback; the frame-rate test reports which renderer ran.
- **Leaks on tab switches.** Pixi apps hold GL contexts. Mitigation: `destroy` on unmount with
  textures; a Playwright test switches tabs repeatedly and checks for console errors.
- **Desk churn.** A naive layout would move people every update. Mitigation: stable assignment,
  unit-tested.
- **Readability of pixel art.** Small sprites are hard to read. Mitigation: whole-number scaling,
  a bubble per state, colour plus shape (not colour alone) for each state, nameplates in HTML.
- **Agent-written text** (titles, tool names) in nameplates and tooltips: React text only.

## Test plan

See approach step 7. `make check` (including Vitest) on every commit; `make ui-test` before the
end of the phase and after visual changes; the live check once at the end. `make e2e` is not
needed: the daemon, hooks, engine and worktrees do not change.

## Steps (each one or more commits with `make check` green)

1. Branch `phase-7/pixel-office`; this plan.
2. ADRs 0024 (office rendering and art) and 0025 (UI test tooling); dependencies and tooling.
3. Pure modules with Vitest tests: art, poses, desk assignment, layout.
4. Pixi scene and the React office view; Office tab.
5. Playwright suite with the mocked daemon; screenshots; visual iteration.
6. Live check against a real daemon; docs (`ART.md`, README, architecture, plan, `PROGRESS.md`).

## Open questions (answered with the recommended option)

1. **Third-party art (CC0 packs) or original art?** Original pixel maps drawn in code. A CC0 pack
   would look richer sooner, but every file needs its source and licence tracked, and the roadmap
   asks for original assets first. Code-drawn sprites are tiny, diffable and recolourable per agent.
2. **PixiJS or a plain canvas?** PixiJS, as the stack names it: batching, a scene graph, hit
   testing and a canvas fallback come with it, and later phases (more rooms, effects) build on it.
   A plain 2D canvas would be smaller but means writing those parts by hand.
3. **Labels in Pixi or in HTML?** HTML overlays: crisp at every scale, real text for the
   agent-written titles, accessible and testable. Pixi text would blur at pixel scales or need a
   bitmap font.
4. **Where does the office live?** A tab next to Board, lazy-loaded, rather than replacing the
   board or the agent list: the board stays the place to act; the office is for watching.
5. **Exited agents?** They keep their desk, empty and dimmed, until removed, the same lifetime as
   their card in the agent list.
6. **How is "smooth with 10 agents" checked?** Frame rate measured in headless Chromium by
   Playwright with 10 and 20 mocked agents, plus a 30 fps cap so the office stays cheap when idle.
7. **How are UI tests run without a daemon or tokens?** A mocked daemon inside Playwright (HTTP
   routes and WebSocket routes), opt-in through `make ui-test` like the other heavy suites; the
   pure logic runs in `make check` with Vitest.

## Changes during the phase

- **ADRs as planned:** 0024 (office view), 0025 (UI test tooling).
- **Columns fit the pane** (not fixed rows of four): the first screenshots showed the office at
  2× in a wide pane, with nameplates crowding the desks. `fitGrid` picks 3 to 6 columns and the
  largest whole device-pixel scale (capped at 4 CSS pixels); the margins shrank to 16 px and the
  wall decor is laid out from the world width. Desk *indices* stay stable; only their screen
  positions reflow when the window changes.
- **No legend row:** the nameplates are buttons (keyboard and screen readers reach every agent),
  so a second list of the same agents was dropped; a bar above the scene counts agents per state.
- **Visual iteration from screenshots:** the chair was redrawn (it read as a second monitor), the
  floor planks lengthened (they read as bricks), the raised hand got a real hand and outline, the
  alert bubble moved clear of it, the reviewer's magnifier bubble was dropped (unreadable, and
  the magnifier prop already says "reviewing"), and the whiteboard got a scribbled title line.
- **Whiteboard holds 20 notes**, as many as the sub-tasks a lead may create (ADR 0017); the plan's
  27 did not fit the board legibly. `maxNotes` is derived from the note grid.
- **New dev dependency `@types/node` 26.6.6:** Playwright's types use `Buffer` (binary terminal
  frames in the mocked daemon). The app's own typecheck still excludes Node types.
- **Bundle:** the board's main chunk went from 582.4 kB to 584.8 kB; Pixi and the office are in
  lazy chunks (the office chunk is 284 kB, 85 kB gzip).
- **End-of-phase review fixes** (an independent reviewer read the diff; no blockers, the
  lifecycle, StrictMode race, text-only rendering and desk assignment held up):
  - **Render on demand:** Pixi's own render is off the ticker; the scene renders only when a
    frame changed. An office where nothing moves no longer redraws 30 times a second.
  - **The frame-rate test measured the page, not the scene:** the scene now records its own cost
    per drawn frame (`data-stats`) and the test checks that; page fps stays as a smoke check.
  - **The leak test could not see WebGL context leaks:** it now does 20 round trips, fails on
    Chrome's context warnings and clicks the canvas afterwards. That click **found a real bug**:
    a click right after the office mounted was lost, because Pixi hit-tests from the last
    rendered root and the scene had not rendered yet. The scene renders once when built.
  - **Desks reset when leaving the tab:** the assignment now lives for the page.
  - Device pixel ratio re-read on every layout; scene and nameplates updated in layout effects
    (no frame on the old grid); at most 20 envelopes in flight; notes redrawn only on change;
    maps built only on a texture-cache miss; a no-op child sort removed.
  - New tests: desks stay put across a tab round trip; clicks hit the right agent after a
    resize and a new row of desks; an office where nothing moves does not redraw.
  - Not changed: a message that arrives before its recipient has a desk is not animated (it is
    still delivered and listed; only the envelope is skipped).
- **Tests run:** `make check` (Go tests, 136 Vitest tests, typecheck including the UI tests,
  build); `make ui-test` (14 Playwright tests) three times in a row, all passing. Measured in
  headless Chromium with WebGL: a drawn frame costs the scene 0.7 ms at p95 and at most 3.4 ms
  with 10 and 20 agents; the page stays at 60 fps. `make e2e` was not run: the daemon, hooks,
  engine and worktrees did not change.
- **Live check** against a real daemon (`--sandbox podman --pack go --model haiku`, scratch data
  dir, throwaway clone of the playground): a lead planned a two-task goal and assigned two
  workers. The office followed typing → idle → exited, showed both sub-tasks on the whiteboard,
  logged no console errors, and a click on a character opened that worker's real terminal. After
  the review fixes, a restarted daemon showed the finished team at their desks and a new
  no-task agent idle at the next free desk.
