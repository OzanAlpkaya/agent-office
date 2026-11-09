# Phase 13 plan: Office v2

- **Status:** Done on `phase-13/office-v2`, waiting for Ozan's review (not merged). Written 2026-11-09 with the recommended option for
  every open question and executed straight away, as Ozan asked ("write the Phase 13 plan using your
  recommended options, then execute it immediately without asking me anything"). Every decision is
  listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 13, the last planned phase. Progress:
  [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites:** none for the office. Re-recording the README GIF (`make demo`) needs the `go`
  pack worker image and spends real haiku tokens (about two cents).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

The office tells more of the story. It gets a floor plan with rooms that mean something: the Tech
Lead's room with the whiteboard, a review room with a meeting table, a kitchen with the coffee
machine and the front door, and the workroom with everyone's desk. Characters walk between them
along real paths, not through walls or furniture:

| Event the UI sees | Where the character goes |
|---|---|
| Its task moves to review or done (`report_done`) | to the Tech Lead's whiteboard, stands there a few seconds, then back to its desk (or out, if it exits) |
| It has been idle for a few seconds | to the coffee machine in the kitchen, once per idle spell, then back |
| A reviewer is reviewing | to the review room's table, sits there with its magnifier while it works |
| It exits | out through the kitchen's front door (replaces the straight-line walk of ADR 0024's amendment) |
| It starts while the office is open | in through the front door to its desk |
| It needs the user (permission prompt, access request) or starts working | straight back to its desk |

Desks stay assigned as in ADR 0024; the nameplate stays on the agent's desk and says where its
character is. The Phase 7 performance rule stays: the scene draws only frames that changed, so an
office where nothing moves draws nothing, and a busy office (10 agents walking) stays inside the
Phase 7 budget. It stays usable on a phone. Like Phase 7, the office is a view of data the UI
already has: no daemon changes, no new endpoint, no new authority.

Because this is the last phase, the showcase is refreshed at the end: a new README GIF from a real
team run (`make demo`) and current screenshots in `docs/screenshots/`.

## Out of scope

- Daemon changes or new events (the office derives errands from agents, tasks and messages it
  already receives). No tags, no releases (CLAUDE.md).
- Side views of characters (they face the viewer, or show their back while walking away), sitting
  animations, furniture the user can move, a configurable floor plan.
- Several goals in parallel (Phase 12 backlog), so there is one Tech Lead's room.

## Approach

1. **Floor plan (pure, tested): `web/src/office/floor.ts`.** `floorPlan(grid)` returns every room,
   wall, doorway, furniture footprint and spot in world pixels for a desk grid. Two arrangements:
   - **wide** (4 to 6 desk columns, desktop): the Tech Lead's room, the review room and the kitchen
     side by side under the back wall; the workroom below them across the whole width;
   - **stacked** (3 columns, a phone or a narrow pane): the Tech Lead's room on top, the review room
     and the kitchen below it, then the workroom.
   Rooms are separated by thin partition walls with doorways. Back walls are drawn as wall faces
   (as today), so the whiteboard, a wall screen in the review room and the front door hang on them.
   `fitGrid` keeps choosing the column count that shows the office largest; 3 columns now also
   means the stacked plan, which is what a phone gets.
2. **Paths (pure, tested): `web/src/office/paths.ts`.** A walkability grid (4 px cells) from the
   floor plan: walls, desks, chairs and tables block, inflated by a character's half width; an
   8-neighbour breadth-first search finds a route, and string pulling turns it into a few straight
   segments (checked for clearance). Every desk's exit point is in the aisle next to its chair, so
   a character stands up, steps into the aisle and walks. Routes are computed once per trip.
3. **Errands (pure, tested): `web/src/office/errands.ts`.** A small director keeps per-agent memory
   (last task status, idle since, report window, coffee taken, spot) and answers "where should this
   agent be now" from the observed world and the clock. Rules in order: waiting for the user, typing
   or booting means the desk; a fresh report means the whiteboard for 9 s; a reviewer reviewing
   means the review room; an agent idle for 4 to 8 s (offset per agent, so they do not all go at
   once) takes one coffee of 10 s per idle spell; everything else is the desk. Spots are limited
   (whiteboard 4, review seats 3 or 4, coffee 3); an errand without a free spot is skipped. An
   agent that exits while reporting finishes the report, then leaves. The first view of the world
   only records it: opening the tab replays nothing.
4. **Scene.** `scene.ts` gets characters that can leave their desk: a figure (body, back-of-head
   variant for walking away, two-frame legs, arms, a prop: mug, marker, magnifier) that walks a
   route at a steady pace (trips last 0.6 to 4.5 s), stands at a spot, or sits at the review table.
   The desk shows an empty chair while its agent is away; its monitor keeps showing the agent's
   state. Desks, furniture and figures are depth-sorted by the y of their feet, so characters walk
   behind and in front of furniture correctly. Figures are clickable (open the agent) and highlight
   with their nameplate. Envelopes fly from wherever the sender is to wherever the recipient is.
   With `prefers-reduced-motion` characters change places without walking (and leavers vanish).
   Render on demand stays: a frame is drawn only when a character moved, a pose frame changed or the
   model changed.
5. **Art.** New original pixel maps in `art.ts`: carpet and tile floors, partition walls, a meeting
   table and chairs, a wall screen with a diff, a coffee counter with a machine, the back of a head,
   standing arms. `ART.md` and `art.test.ts` cover them.
6. **React.** Room names are HTML labels over the canvas (crisp, real text, as the nameplates).
   Nameplates get `data-place` and a tooltip line ("At the coffee machine"). The scene publishes
   per-agent places and extra frame statistics (frames drawn while N characters walked) for the UI
   tests.
7. **Showcase.** `make demo` re-records the README GIF in the new office (workers walk in, report at
   the whiteboard, leave through the door); stills and mocked screenshots in `docs/screenshots/` are
   replaced with current ones (desktop office with rooms, a close-up, messages, the phone layout,
   the board and Usage), each checked for local paths and personal data.

## Changes

- `web/src/office/`: new `floor.ts`, `paths.ts`, `errands.ts` with tests; `layout.ts` (geometry from
  the floor plan), `scene.ts` (figures, depth sorting, rooms), `art.ts` (+ tests), `model.ts`,
  `Office.tsx`, `ART.md`; `web/src/styles.css` (room labels).
- `web/ui-tests/office.spec.ts`: rooms, each errand, walking in and out, reduced motion, the walking
  budget, the phone layout; `web/demo/record-team-run.ts` if the recording needs new waits.
- Docs: ADR 0045 (floor plan, paths and errands); ADR 0024 status line points to it; architecture
  (office view), `docs/plan.md` (Phase 13 DoD), `docs/screenshots/README.md`, README (status,
  pictures), `docs/demo.md`, `PROGRESS.md`, this file.

## Data / API changes

None.

## Risks & edge cases

- **Render cost of walking.** Walking is continuous animation. Mitigation: only figures that move
  mark the frame dirty; standing figures change frame at most every few seconds (a blink, a sip);
  routes are computed once per trip, not per frame. Measured: p95 of frames drawn while 10 or more
  characters walk, under 8 ms in the UI tests.
- **Idle offices that never settle.** Coffee is once per idle spell, so an idle team goes quiet
  again after its break; a test checks that an office settles to zero renders.
- **Clicks on the wrong agent.** A figure walking past a desk is above it; clicks on a figure open
  that figure's agent. Nameplates stay fixed on desks, so they are a stable target.
- **Resizes and new desk rows during a walk.** Positions are in world pixels of the old plan.
  Mitigation: on a plan change every away character is placed at its current target at once.
- **Phone width.** The stacked plan for 3 columns keeps the scale as on a phone today; the UI test
  checks a Pixel 7 viewport has no overlapping nameplates and no horizontal page scroll.
- **Agent-written text** stays in HTML nameplates and tooltips, never in the canvas.

## Test plan

- Vitest: floor plans for every column count and both arrangements (rooms inside the world and not
  overlapping, every desk in the workroom, every spot and exit point walkable, doorways in walls);
  paths (a route from every desk to every spot and the door, every segment clear, deterministic);
  errands (each rule, priorities, capacity, first view replays nothing, exit after report); art.
- Playwright (mocked daemon): room labels; a report sends the worker to the whiteboard and back; an
  idle agent goes for coffee once and comes back; a reviewer works in the review room; a new agent
  walks in; an exited agent walks out of the door; reduced motion changes places without walking
  frames; 10 agents walking stay under the budget; an office settles to no renders; clicking a
  walking or standing character opens it; the phone layout. Existing office tests keep passing.
- `make check` on every commit; `make ui-test` after visual changes and at the end; a live check
  against a real daemon (the demo run). `make e2e` is not needed: daemon, hooks, engine and
  worktrees do not change.

## Steps (each one or more commits with `make check` green)

1. Branch `phase-13/office-v2`; this plan.
2. Floor plan and paths, with tests; layout geometry from the floor plan.
3. Errands director, with tests.
4. Art for rooms and figures.
5. Scene: rooms, figures, depth sorting, errands; Office labels and places.
6. Playwright tests, budget measurement, screenshots, visual iteration (desktop and phone).
7. ADR 0045 and docs.
8. Showcase: `make demo`, new GIF and screenshots, README.
9. Independent review of the diff, fixes, `PROGRESS.md`, end-of-phase summary.

## Open questions (answered with the recommended option)

1. **Where do nameplates go when a character walks?** They stay on the desk and say where the
   character is. Moving HTML labels every frame would cost layout work per frame and make them hard
   to click; the desk is the stable "where is my agent" answer.
2. **Which events make characters move?** Only ones the UI already sees: task status changes
   (reports), agent states (idle, reviewing, waiting, exited) and new agents. No new daemon events.
3. **Tile pathfinding or hand-made waypoints?** A walkability grid with BFS and string pulling. It
   adapts to every desk count and both arrangements without hand-tuned graphs, and it can be tested
   for "never walks through a wall".
4. **How do phones fit the extra rooms?** The stacked arrangement for 3 columns (rooms on top of
   each other); the pane scrolls vertically as it already does.
5. **Coffee breaks forever?** Once per idle spell, so the office goes quiet again.
6. **New ADR or amend ADR 0024?** A new ADR 0045 for the floor plan, paths and errands; ADR 0024
   still holds for rendering, art and labels, and its status line points to 0045.

## Changes during the phase

- **Errand order.** The plan put "typing" with "waiting" and "booting" (straight to the desk). A
  worker usually reports in the middle of a turn, so a fresh report now beats typing; only waiting
  for the user and booting beat a report (ADR 0045, `errands.test.ts`).
- **Spots.** Whiteboard spots are 3, not 4: the Tech Lead's room is narrow at four desk columns,
  and its desk's exit lane must stay free.
- **No rug.** The lead's purple rug clashed with the new carpet and was removed (with its art).
- **Draw at once after a change.** The first full UI run found a click right after the Office tab
  reopened opening nothing, about one run in three: Pixi hit-tests against the transforms of the
  last render, and the first render after a remount came one tick later. The scene now draws at
  once after a model or size change (rare events, so the idle budget is unchanged).
- **Grid changes during walks.** The busy-office test found two bugs: a burst of new agents could
  grow the grid before a walk-in had its first errand, which sent it out again (a placeholder
  goal), and many agents exiting at once shrank the grid and teleported everyone walking out. A
  walk-in now has its own goal, the office keeps its rows while someone is still leaving, and a
  change of rows (same columns) only re-routes walkers.
- **Busy office measured on leavers.** Ten agents arriving one message at a time can change the
  column count part way, so the budget test measures ten agents leaving at once instead (the grid
  is held while they walk).
- **Demo recorder.** A still when the first worker reports, and eight seconds in the Office after
  the goal is reported, so the GIF shows the whiteboard and the walk-outs.
- **Parallel demo (Ozan's review).** Both earlier recordings ran the sub-tasks one after the
  other: both edited the HTTP handler, so file ownership (ADR 0032) made the second wait. The demo
  goal now gives each sub-task its own files (the health check in `internal/httpapi`, duplicate
  URLs rejected in `internal/bookmark` with an error that wraps `ErrInvalid`, which the API
  already answers with 422), and the re-recorded GIF shows both workers at once.
- **Inspiration line removed** from the README (Ozan's review).
