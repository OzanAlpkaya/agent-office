# 0057. Office layouts per project, in YAML, validated by the daemon

- **Status:** Accepted
- **Date:** 2026-11-12
- **Amends:** ADR 0045 (the floor plan), ADR 0024 (desks)

## Context

The office's floor plan (ADR 0045) is computed from the desk grid: two bands of rooms over the
workroom on a wide pane, three on a phone, desks growing with the team. Ozan asked for the layout
per project in YAML (no visual editor): which rooms exist, where they sit on a grid, their sizes,
the workroom's desks; validated (required rooms present, every room reachable through doorways);
an invalid layout falls back to the default with a visible warning; a few presets and a preview in
the project settings; and a decision for more workers than desks.

## Options considered

**Where the layout lives and who parses it**

1. **A file in the repository** (`.agent-office/office.yaml`). Cons: agents can edit it; a branch
   switch changes the office; nothing to validate it against before it is used.
2. **A project setting, parsed in the browser.** Cons: a YAML parser in the UI bundle, and
   validation in a second language from the daemon's settings.
3. **A project setting, parsed and validated by the daemon** (`internal/office`), which serves the
   parsed layout as JSON with the project; the UI never parses YAML. Pros: one parser, the same
   strict YAML decoding as stack packs and roles; the preview asks the daemon.

**What a layout says.** Rooms as rectangles on a grid of 16-pixel tiles, with explicit doors as
pairs of rooms (a door is cut in the middle of their shared wall), versus doors computed from
adjacency. Explicit doors make reachability something to validate, as asked.

**More workers than desks**

1. **Refuse to start** an agent without a desk. Cons: the office would limit the team.
2. **Grow the workroom** as the default plan does. Cons: a fixed layout can have rooms below it.
3. **Hot desks**: agents beyond the desks are not drawn and are listed in the office bar; a desk
   that frees up goes to the one that waited longest; the preview warns when the desks are fewer
   than the agents the project may run.

## Decision

Option 3, explicit doors, hot desks.

```yaml
version: 1
rooms:
  - { id: lead, x: 0, y: 0, w: 8, h: 7 }
  - { id: work, x: 0, y: 14, w: 15, h: 11, desks: 6, name: Open office }
doors:
  - [lead, kitchen]
```

Rooms: `lead`, `user`, `lobby`, `work` are required; `planning`, `qa`, `review`, `kitchen` are
optional. Without a planning room or QA lab the architect or QA take a workroom desk; without a
review room reviewers stay at their desks; without a kitchen there are no coffee breaks. Every
room has its back wall (44 px) at its top and a wall at its bottom, as in ADR 0045.

Validation (all problems at once, each with where it is): version 1; known, unique ids; the
required rooms; whole tiles inside 64 × 48; each room at least its furniture's size; no overlaps;
1 to 32 desks that fit the workroom (whole desk cells between its margins, rows below its back
wall); doors between existing rooms that share a wall long enough for a doorway (3 tiles along a
back wall, the doorway's height beside one); every room reachable from the lobby; the user's
office has a door (its first door is where the queue forms). Problems with rooms are reported
before doors, so one misplaced room does not cascade.

The setting `office_layout` (at most 8 KB) is validated when it changes; a layout alone does not
reopen the project, so it may change while agents run. Stored settings are only size-checked when
a project opens: a stored layout that no longer validates does not stop the project; the office
uses the default plan and shows the problems with a link to the settings. `ProjectInfo.office` is
`{source: default|custom, layout?, problems?}`. `GET /api/office-layouts` lists the presets
(classic, compact, open plan, tall); `POST /api/office-layouts/check` parses YAML for the preview.

The UI builds a floor plan from the parsed layout (`office/custom.ts`) with the same shape as the
default plan's, so the scene, the walk grid and the errands are unchanged; optional furniture and
stations are absent instead. The settings editor shows the default or an own layout, presets, the
YAML checked as you type, a schematic preview from the same floor plan code, and the desk warning.

## Consequences

- The default stays adaptive (wide or stacked, growing); a custom layout is fixed and scales.
- The presets' parsed form is checked in (`web/src/office/presets.gen.json`, written by the Go
  test with `-update`): Vitest walks every preset (every spot reachable from every desk, through
  doors), and the Go test fails when the file is out of date, so the two sides cannot drift.
- Pixel rules (back wall, desk cell, doorway) exist in Go and TypeScript; both are tested against
  the same presets.
- Deskless agents are invisible in the office itself; they are named in the office bar.

## Amendment (2026-11-12, Phase 18 review): fitting the office to the pane

The office was the largest whole-pixel drawing that fit both the pane's width and height: on a desktop
a small centred box, on a phone a band of empty space above and below. It now fills the pane's width
as well as whole pixels allow (more columns on wide screens, up to ten), keeps at least 1.5 CSS pixels
when the width allows, starts at the top, and covers a taller pane by deepening the workroom's floor
(not with empty desks); an office taller than the pane scrolls. A custom layout is scaled by width.
On a phone a nameplate shows only the name; a tap shows the task and a second opens the agent.

## Amendment (2026-11-12, Phase 18 second review): Fit and Zoom

Scaling to the width alone made the office taller than the pane at 3 CSS pixels: the workroom needed
scrolling. The office now has a zoom in its bar. **Fit** (the default): the largest whole-pixel scale
at which the whole floor fits the pane in both width and height; among plans drawn at that scale the
one that uses the width best (more columns on wide screens); height left below becomes workroom floor
only as far as it fits, so nothing scrolls (a 1440 × 900 window: 2 CSS pixels, eight columns). **Zoom
+**: about a third larger (2 → 3 CSS pixels on a desktop; 3 → 4 device pixels on a phone, which still
fits its width), and the pane scrolls. The choice is remembered per browser (localStorage). A plan
wider than the pane is never picked while a narrower one fits. On a phone a name-only plate leaves out
the role too, so plates do not overlap.


## Amendment (2026-11-13, Phase 20): windows only on outside walls

A custom layout's windows go only on the stretches of a room's back wall with no room behind it: at the
grid's top, or below tiles no room covers. They keep 4 px clear of what hangs on the wall, of every
doorway that reaches into the wall (the way into the user's office included) and of partitions. Tests
check this for every preset, for 40 random valid layouts that `internal/office` generates from a fixed
seed into `web/src/office/generated.gen.json` (`go test ./internal/office -update`; a stale file fails),
and for the default plan at every desk grid the office uses.
