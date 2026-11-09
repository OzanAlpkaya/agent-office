import { describe, expect, it } from "vitest";
import * as art from "./art";

const person = art.personPalette(art.skins[0], art.hairColors[0], art.shirts.worker);
const notePalette = { ...art.basePalette, U: 0, u: 0 };

const maps: [string, art.PixelMap, art.Palette][] = [
  ["body", art.body, person],
  ["bodyBlink", art.bodyBlink, person],
  ...art.legs.map((m, i): [string, art.PixelMap, art.Palette] => [`legs.${i}`, m, person]),
  ...art.hairStyles.map((s): [string, art.PixelMap, art.Palette] => [`hair.${s}`, art.hair[s], person]),
  ["chair", art.chair, art.basePalette],
  ["monitor", art.monitor, art.basePalette],
  ["keyboard", art.keyboard, art.basePalette],
  ["mug", art.mug, art.basePalette],
  ["magnifier", art.magnifier, art.basePalette],
  ["envelope", art.envelope, art.basePalette],
  ["plant", art.plant, art.basePalette],
  ["waterCooler", art.waterCooler, art.basePalette],
  ["bubbleAlert", art.bubbleAlert, art.basePalette],
  ...art.bubbleDots.map((m, i): [string, art.PixelMap, art.Palette] => [`bubbleDots.${i}`, m, art.basePalette]),
  ["desk", art.desk, art.basePalette],
  ["door", art.door, art.basePalette],
  ["window", art.windowMap, art.basePalette],
  ["whiteboard", art.whiteboardMap, art.basePalette],
  ["note", art.noteMap, notePalette],
  ["floor", art.floorTile, art.floorPalette],
  ["wall", art.wallTile, art.wallPalette],
  ["bodyBack", art.bodyBack, person],
  ["carpet", art.carpetTile, art.carpetPalette],
  ["tile", art.tileFloor, art.tilePalette],
  ["kitchenTile", art.tileFloor, art.kitchenPalette],
  ["table", art.table(88), art.basePalette],
  ["wallScreen", art.wallScreen, art.wallScreenPalette],
  ["counter", art.counter, art.basePalette],
  ["screenOff", art.screenOff(), art.screenPalette],
  ["code", art.codeMap, art.screenPalette],
  ...art.screenBoot().map((m, i): [string, art.PixelMap, art.Palette] => [`boot.${i}`, m, art.screenPalette]),
  ...art.screenPrompt().map((m, i): [string, art.PixelMap, art.Palette] => [`prompt.${i}`, m, art.screenPalette]),
  ...art.screenDialog().map((m, i): [string, art.PixelMap, art.Palette] => [`dialog.${i}`, m, art.screenPalette]),
  ...art.screenDiff().map((m, i): [string, art.PixelMap, art.Palette] => [`diff.${i}`, m, art.screenPalette]),
];

describe("pixel maps", () => {
  it.each(maps)("%s: every row has the map's width", (_name, m) => {
    expect(m.w).toBeGreaterThan(0);
    expect(m.rows.length).toBe(m.h);
    for (const row of m.rows) expect(row.length).toBe(m.w);
  });

  it.each(maps)("%s: every pixel is transparent or in the palette", (_name, m, palette) => {
    for (const row of m.rows) {
      for (const ch of row) {
        if (ch === art.transparent && !(ch in palette)) continue;
        expect(palette, `character ${JSON.stringify(ch)}`).toHaveProperty(ch);
      }
    }
  });

  it("hair styles fit over the body", () => {
    for (const s of art.hairStyles) {
      expect(art.hair[s].w).toBe(art.body.w);
      expect(art.hair[s].h).toBeLessThanOrEqual(art.body.h);
    }
    expect(art.bodyBlink.w).toBe(art.body.w);
    expect(art.bodyBlink.h).toBe(art.body.h);
  });

  it("the back of a character covers the face and keeps the body", () => {
    expect([art.bodyBack.w, art.bodyBack.h]).toEqual([art.body.w, art.body.h]);
    const face = art.bodyBack.rows.slice(2, 10).join("");
    expect(face).not.toMatch(/[sem]/);
    expect(art.bodyBack.rows.slice(10)).toEqual(art.body.rows.slice(10));
  });

  it("the meeting table is as tall as declared, at any width", () => {
    for (const w of [48, 64, 88]) expect([art.table(w).w, art.table(w).h]).toEqual([w, art.tableHeight]);
    expect(art.counter.h).toBeGreaterThan(art.counterTop);
  });

  it("screens fit the monitor's screen", () => {
    for (const m of [art.screenOff(), ...art.screenBoot(), ...art.screenPrompt(), ...art.screenDialog(), ...art.screenDiff()]) {
      expect([m.w, m.h]).toEqual([art.screenRect.w, art.screenRect.h]);
    }
    expect(art.codeMap.w).toBe(art.screenRect.w);
    // the screen area of the monitor is all "off" pixels, so nothing peeks out
    for (let y = 0; y < art.screenRect.h; y++) {
      const row = art.monitor.rows[art.screenRect.y + y];
      expect(row.slice(art.screenRect.x, art.screenRect.x + art.screenRect.w)).toBe("x".repeat(art.screenRect.w));
    }
  });

  it("the whiteboard's note grid fits inside its frame", () => {
    const g = art.noteGrid;
    expect(g.x + (g.cols - 1) * g.dx + art.noteMap.w).toBeLessThanOrEqual(art.whiteboardSize.w - 2);
    expect(g.y + (g.rows - 1) * g.dy + art.noteMap.h).toBeLessThanOrEqual(art.whiteboardSize.h - 5);
    expect(art.titleLine.y).toBeLessThan(g.y);
    expect(art.titleLine.x + art.titleLine.w).toBeLessThanOrEqual(art.whiteboardSize.w - 2);
  });

  it("built pieces are deterministic", () => {
    expect(art.screenDiff()).toEqual(art.screenDiff());
    expect(art.table(64)).toEqual(art.table(64));
    expect(art.wallScreen).toEqual(art.wallScreen);
  });
});
