# 0024. Office view: a lazy-loaded PixiJS scene, art drawn in code, labels in HTML

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

Phase 7 adds a pixel-art office where each agent is a character whose animation follows its state
(`docs/plans/phase-7.md`). It must show every agent live, open an agent on click, stay smooth with
10 agents, and use only original or properly licensed art. The UI is React + Vite, embedded in the
binary (ADR 0005), and stays open for hours while agents work, so an idle office must be cheap.
Agent-written text (task titles, tool names) must stay text, never markup.

## Options considered

**Renderer**

1. **PixiJS 8 (WebGL, canvas fallback).** Pros: batching, a scene graph, pointer hit testing,
   nearest-neighbour textures and a canvas renderer for machines without WebGL; named in the
   project stack for later rooms and effects. Cons: several hundred kB; a GL context to manage.
2. **Plain 2D canvas.** Pros: no dependency, small. Cons: scene graph, hit testing, batching and
   scaling written by hand; harder to grow.
3. **DOM/CSS sprites.** Pros: no canvas. Cons: hundreds of animated elements re-laid out by the
   browser; pixel-perfect scaling is fragile.

**Art**

1. **Pixel maps in TypeScript** (one character per pixel plus a palette), turned into textures at
   runtime. Pros: original by construction, tiny, reviewable in a diff, recolourable per agent
   (skin, hair, role colour). Cons: drawing by hand takes time; simpler look.
2. **A CC0 asset pack (e.g. from an asset site).** Pros: richer art quickly. Cons: licence and
   source to track per file; a fixed look that is hard to recolour per agent; binary files in the
   repository; the roadmap asks for original assets.
3. **Generated art (image models).** Cons: unclear rights and provenance; not reproducible.

**Labels**

1. **HTML nameplates over the canvas.** Pros: crisp at any scale, real text for agent-written
   titles, accessible, testable. Cons: positions must track the scene's scale.
2. **Pixi text or a bitmap font.** Cons: blurry at pixel scales or a font asset to license.

## Decision

PixiJS 8.22 with `preference: ["webgl", "canvas"]`, nearest scaling and a whole number of device
pixels per world pixel (re-read on every layout, so zoom and other screens stay sharp). The scene
ticks at most 30 times a second and **renders only when something changed** (Pixi's own render is
taken off the ticker); it renders once when built, because Pixi hit-tests from the last rendered
root. The office module is loaded with `import()` only when its tab opens and its application is
destroyed when the tab closes. The desk assignment lives for the page, not the component, so
leaving the tab moves nobody; a reload seats agents again in start order. The office picks 3 to
6 desk columns, whichever shows it largest in the pane. Art is pixel maps in `web/src/office/art.ts`,
original to this repository (`web/src/office/ART.md`). Names, titles and tooltips are HTML
nameplates positioned from the same layout as the scene; they are buttons, so the office works
from the keyboard, and a bar above the scene counts agents per state. Poses and desk assignment are pure functions, unit-tested; the scene only draws them.

## Consequences

- The board's bundle does not grow; the office chunk carries Pixi.
- No third-party art, so no licence file per asset. Adding art means editing pixel maps; a
  contributor who wants a CC0 pack later must record its source and licence next to it.
- An open office where nothing moves costs a few cheap ticks per second and no renders; while
  agents animate it draws at most 30 frames a second (animations run at 2 to 8 fps). The scene
  publishes its own frame statistics (`data-stats`), which the UI tests check.
- The scene has no authority: it only calls the same "open agent" action as the agent list.
- Accessibility rests on the HTML nameplates (buttons with tooltips) and the state bar; the
  canvas is decorative.
