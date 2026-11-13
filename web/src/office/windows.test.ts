// Windows only on outside walls (Phase 20): for every preset, every layout
// internal/office generates (generated.gen.json) and the default plan at
// every desk grid, each window hangs on a back wall with no room behind it,
// clear of doorways, of what hangs there and of the other windows.
import { describe, expect, it } from "vitest";
import type { OfficeLayout } from "../api";
import generated from "./generated.gen.json";
import presets from "./presets.gen.json";
import * as art from "./art";
import { customPlan } from "./custom";
import { type FloorPlan, type Rect, face, floorPlan, wall } from "./floor";

const overlaps = (a: Rect, b: Rect, m = 0): boolean =>
  a.x < b.x + b.w + m && b.x < a.x + a.w + m && a.y < b.y + b.h + m && b.y < a.y + a.h + m;
const within = (a: Rect, b: Rect): boolean => a.x >= b.x && a.y >= b.y && a.x + a.w <= b.x + b.w && a.y + a.h <= b.y + b.h;

// windowProblems lists every window that breaks the rules, with why.
export function windowProblems(p: FloorPlan): string[] {
  const out: string[] = [];
  const rects = p.windows.map((w): Rect => ({ x: w.x, y: w.y, w: art.windowMap.w, h: art.windowMap.h }));
  // a room's whole area: back wall, floor and the wall below it
  const area = (r: Rect): Rect => ({ x: r.x, y: r.y - face, w: r.w, h: r.h + face + wall });
  const hung: [string, Rect][] = [
    ["whiteboard", { ...p.whiteboard, ...art.whiteboardSize }],
    ...(p.screen ? [["screen", { ...p.screen, ...art.wallScreenSize }] as [string, Rect]] : []),
    ...(p.blueprints ? [["blueprints", { ...p.blueprints, w: 26, h: 18 }] as [string, Rect]] : []),
    ...(p.testBoard ? [["test board", { ...p.testBoard, w: 28, h: 18 }] as [string, Rect]] : []),
    ["front door", { ...p.door, w: art.door.w, h: art.door.h }],
    ...(p.counter ? [["counter", { ...p.counter, w: art.counter.w, h: art.counter.h }] as [string, Rect]] : []),
  ];
  rects.forEach((w, i) => {
    const at = `window ${i} at ${w.x},${w.y}`;
    const room = p.rooms.find((r) => r.id !== "work" || p.custom ? within(w, { x: r.rect.x, y: r.rect.y - face, w: r.rect.w, h: face }) : false);
    if (!room) {
      out.push(`${at}: not on a room's back wall`);
      return;
    }
    // the strip right behind the wall's top edge, above the window
    const behind: Rect = { x: w.x, y: room.rect.y - face - 1, w: w.w, h: 1 };
    const other = p.rooms.find((r) => r !== room && overlaps(area(r.rect), behind));
    if (other) out.push(`${at}: on the ${room.id} wall with the ${other.id} behind it`);
    for (const d of p.doorways) if (overlaps(w, d, 2)) out.push(`${at}: over a doorway at ${d.x},${d.y}`);
    for (const [what, r] of hung) if (overlaps(w, r, 2)) out.push(`${at}: over the ${what}`);
    for (const wl of p.walls) if (overlaps(w, wl)) out.push(`${at}: over a wall at ${wl.x},${wl.y}`);
    rects.forEach((o, j) => { if (j > i && overlaps(w, o)) out.push(`${at}: over window ${j}`); });
  });
  return out;
}

const presetLayouts = presets as Record<string, OfficeLayout>;
const generatedLayouts = generated as Record<string, OfficeLayout>;

describe("windows on outside walls only", () => {
  it.each(Object.keys(presetLayouts))("preset %s", (name) => {
    const p = customPlan(presetLayouts[name]);
    expect(windowProblems(p)).toEqual([]);
    // the tall preset's top rooms are filled by what hangs there
    if (name !== "tall") expect(p.windows.length).toBeGreaterThan(0);
  });

  it("has generated layouts of every kind", () => {
    expect(Object.keys(generatedLayouts).length).toBeGreaterThanOrEqual(40);
  });

  it.each(Object.keys(generatedLayouts))("generated layout %s", (name) => {
    expect(windowProblems(customPlan(generatedLayouts[name]))).toEqual([]);
  });

  it("the default plan at every desk grid", () => {
    for (let columns = 3; columns <= 12; columns++) {
      for (let rows = 1; rows <= 6; rows++) {
        const p = floorPlan(columns, rows);
        expect(windowProblems(p), `${columns}x${rows}`).toEqual([]);
        // the stacked plan's top rooms are filled by the whiteboard and the blueprints
        if (!p.stacked) expect(p.windows.length, `${columns}x${rows}`).toBeGreaterThan(0);
      }
    }
  });

  it("puts no window on the back wall of a room with a room behind it", () => {
    // The classic preset's workroom and second-row rooms have rooms behind them.
    const p = customPlan(presetLayouts.classic);
    for (const id of ["work", "lobby", "kitchen"] as const) {
      const r = p.rooms.find((x) => x.id === id);
      if (!r) continue;
      const onIt = p.windows.filter((w) => w.y >= r.rect.y - face && w.y < r.rect.y && w.x >= r.rect.x && w.x < r.rect.x + r.rect.w);
      expect(onIt, id).toEqual([]);
    }
    // And the default plan's second band: no windows on the review room or the QA lab.
    const d = floorPlan(6, 2);
    for (const id of ["review", "qa", "lobby", "kitchen"] as const) {
      const r = d.rooms.find((x) => x.id === id) as { rect: Rect };
      expect(d.windows.filter((w) => w.y < r.rect.y && w.y >= r.rect.y - face && w.x >= r.rect.x && w.x < r.rect.x + r.rect.w), id).toEqual([]);
    }
  });

  it("finds a window drawn where it must not be", () => {
    const p = customPlan(presetLayouts.classic);
    const lobby = p.rooms.find((r) => r.id === "lobby")?.rect as Rect;
    const bad = { ...p, windows: [...p.windows, { x: lobby.x + 30, y: lobby.y - face + 8 }] };
    expect(windowProblems(bad).join("\n")).toMatch(/behind it|over/);
  });
});
