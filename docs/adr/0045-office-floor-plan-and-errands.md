# 0045. Office v2: a floor plan with rooms, paths on a grid, errands from what the UI sees

- **Status:** Accepted
- **Date:** 2026-11-09

## Context

Phase 13 (`docs/plans/phase-13.md`) asks the office to tell more of the story: characters should
walk between places that mean something (the Tech Lead's whiteboard to report, a coffee machine
when idle, the review room when reviewing, out of the office when they exit), in an office with
separate rooms. Desks stay assigned (ADR 0024). The Phase 7 performance rule stays: the scene draws
only frames that changed, an office where nothing moves draws nothing, and a busy office stays
within the budget (p95 under 8 ms per drawn frame). It must stay usable on a phone (ADR 0040), and
like Phase 7 it is a view: no new daemon events, no new authority.

Three questions: how the floor plan adapts to any number of desks and to a phone; how characters
find their way without walking through walls and furniture; and which events send them where.

## Options considered

**Floor plan**

1. **A plan computed from the desk grid, in two arrangements.** The Tech Lead's room, a review
   room and a kitchen along a back wall, the workroom below; side by side for 4 to 6 desk columns,
   stacked (lead's room, then review room and kitchen) for 3 columns. Pros: works for every desk
   count; the stacked plan keeps a phone's scale; the existing "pick the columns that show the
   office largest" rule picks the arrangement too. Cons: two arrangements to test.
2. **A fixed, hand-drawn map with a fixed number of desks.** Pros: art can be richer. Cons: a team
   larger than the map has nowhere to sit; a phone gets a tiny office.
3. **Rooms as separate tabs or panels.** Cons: walking between rooms is the point.

**Paths**

1. **A walkability grid (4 px cells) from the plan, breadth-first search, string pulling.** Walls,
   back walls, desks with their chairs and furniture block, grown by a character's half width; an
   8-neighbour BFS without corner cutting finds a route; pulling the string leaves a few straight
   segments. Pros: adapts to every plan; testable ("every segment of every route is clear");
   deterministic; a route costs well under a millisecond and is computed once per trip. Cons: a
   grid to build per plan (cached).
2. **Hand-placed waypoints per plan.** Cons: must be redone for every column and row count and both
   arrangements; easy to leave a hole.
3. **Straight lines (as the Phase 7 amendment's walk-out).** Cons: through walls and desks.

**Errands**

1. **Derived in the UI from data it already receives.** A report is a task moving into review or
   done (what `report_done` does); idleness, reviewing and waiting come from agent states; arrivals
   and exits from the agent list. Pros: no daemon change; works for agents of every role. Cons:
   the UI infers rather than being told, so a task moved by hand also counts as a report.
2. **New daemon events (e.g. "reported", "went idle").** Cons: daemon and API changes for a
   decoration; the existing events already carry the information.

## Decision

Option 1 in each case.

- `floor.ts` builds the plan for a grid: rooms (with names and purposes for HTML labels), back
  walls, partitions with doorways, the whiteboard, a wall screen, the review table and its seats,
  the kitchen counter, the front door and the standing spots. Three columns give the stacked plan.
- `paths.ts` builds the walkability grid and routes. Every desk has an exit point in the aisle to
  the right of its chair; a character stands up there, walks, and sits down again from there.
- `errands.ts` (a small director with memory) answers "where should this agent be now", in this
  order: waiting for the user or booting means the desk; a fresh report means the whiteboard for
  9 s (walking included); typing means the desk; a reviewer reviewing sits in the review room; an
  idle agent takes one coffee of 10 s per idle spell, 4 to 8 s after it went idle (offset per
  agent); anything else is the desk. Spots are limited (whiteboard 3, coffee 3, review seats 2 to
  4); whoever holds a spot keeps it, the others queue by when they asked, and an errand without a
  free spot is skipped. An agent that exits while reporting finishes the report, then leaves. The
  first observation only records, so opening the tab replays nothing; agents already reviewing
  appear in the review room at once.
- The scene walks characters at 64 world px/s (trips last 0.6 to 4.5 s), shows their back when
  they walk up, gives them a prop where they stand (a marker, a mug, a magnifier), and depth-sorts
  desks, furniture and characters by the y of their feet. New agents walk in through the front
  door; leavers walk out and fade. With reduced motion characters change places without walking.
- Nameplates stay on desks (a stable target) and say where their character is (`data-place`,
  tooltip). Characters away from their desk are clickable and highlight with their nameplate.
- Render on demand stays: only walkers and changed pose frames mark a frame. The scene also draws
  at once after a model or size change, because Pixi hit-tests against the last render's transforms
  (a click right after the tab opened could miss otherwise).
- While someone walks out, the office keeps its rows; it shrinks once they are gone. A change of
  rows (same columns) re-routes walkers; a change of columns places everyone at their target.

## Consequences

- The office has four rooms with a purpose and characters with somewhere to go; the story of a run
  (arrive, work, report at the whiteboard, review in the review room, coffee, leave) is visible
  without reading the board.
- Measured in headless Chromium (WebGL): with ten characters walking at once, p95 0.4 to 0.6 ms
  and at most about 6 ms per drawn frame (UI test "a busy office"); an office that has emptied
  draws no frames. An idle team takes its coffee and goes quiet again.
- A task moved to review by hand also sends its agent to the whiteboard. That is acceptable: it is
  what a report looks like from the board.
- The office is taller than in Phase 7 (a band of rooms above the workroom), so a desktop pane
  shows it at a slightly smaller scale; a phone gets the stacked plan at about the same scale as
  before.
- ADR 0024 still holds for the renderer, the art and the HTML labels; its amendment's straight
  walk-out is replaced by routed walks.
