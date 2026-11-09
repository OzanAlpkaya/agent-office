# Office art

All art in the office view is **original to this repository**, drawn in code for agent-office
(ADR 0024). There are no image files, sprite sheets, fonts or third-party assets: nothing to
license or attribute beyond the repository's own licence.

## How it is made

- `art.ts` holds **pixel maps**: lists of strings, one character per pixel, looked up in a
  palette (`.` is transparent). Characters (head, body, five hair styles), chair, monitor,
  keyboard, mug, magnifier, envelope, plant, water cooler and speech bubbles are drawn by hand
  this way.
- Larger or repetitive pieces (desk, floor planks, wall, windows, whiteboard, door, monitor
  screens with code, diffs, prompts and dialogs) are built by small functions from the same
  alphabet, with a fixed pseudo-random noise so they never change between runs.
- Phase 13 (ADR 0045) added, the same way: carpet and tile floors, a meeting table, a wall screen
  with a diff, the kitchen counter with its coffee machine, the back of a character (its face
  covered by hair, derived from the front) and standing arms (walking, pointing with a marker,
  holding a mug). Partition walls are plain rectangles in two colours.
- Arms and hands are drawn by `scene.ts` with whole-pixel rectangles, because they move per frame.
- Each agent's skin, hair colour and hair style come from a hash of its id; the shirt colour
  comes from its role. `textures.ts` turns each map and palette into a texture once.

## Adding or changing sprites

1. Add a map to `art.ts` (every row the same width) and, if it needs new colours, a palette entry.
2. Add it to the `maps` table in `art.test.ts`; the test checks the width of every row and that
   every character is in the palette.
3. Preview it in the office (`make ui-test` writes screenshots to `web/test-results/screens`).

If third-party art is ever added, put it in its own folder with a `SOURCE.md` naming the origin,
author and licence of every file, and use only licences that allow redistribution in a binary
(e.g. CC0).
