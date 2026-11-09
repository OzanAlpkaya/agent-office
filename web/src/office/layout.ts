// Desk assignment and office geometry (ADR 0024). Pure: the scene and the
// HTML nameplates both place things from these numbers, in world pixels.
import type { Agent } from "../api";
import { type FloorPlan, type Point, type StationKind, cell, floorPlan, margin, stationKinds } from "./floor";

// Assignment says who sits where: the Tech Lead at the lead desk, the
// architect, QA and the assistant at their stations (ADR 0045), everyone else
// at a regular desk; desks[i] is the agent at regular desk i.
export interface Assignment {
  readonly lead?: string;
  readonly architect?: string;
  readonly qa?: string;
  readonly assistant?: string;
  readonly desks: readonly (string | undefined)[];
}

export const emptyAssignment: Assignment = { desks: [] };

// The kinds with a place of their own, and the place's key in an Assignment.
export const ownPlaces = ["lead", ...stationKinds] as const;
export type OwnPlace = (typeof ownPlaces)[number];

const byStart = (x: Agent, y: Agent): number =>
  Date.parse(x.started_at) - Date.parse(y.started_at) || (x.id < y.id ? -1 : x.id > y.id ? 1 : 0);

// assignDesks keeps every agent at the desk it already has for as long as it
// exists. New agents take the lowest free desk, in start order; removed agents
// free theirs. A kind's own place (the lead desk, a station) belongs to the
// running agent of that kind, or else to the one already there; one that has
// exited moves to a regular desk when a new one starts. Applying it twice
// gives the same result.
export function assignDesks(prev: Assignment, agents: readonly Agent[]): Assignment {
  const present = new Set(agents.map((a) => a.id));
  const desks = prev.desks.map((id) => (id !== undefined && present.has(id) ? id : undefined));
  const own: Partial<Record<OwnPlace, string>> = {};
  for (const place of ownPlaces) {
    let holder = prev[place] !== undefined && present.has(prev[place] as string) ? prev[place] : undefined;
    const ofKind = agents.filter((a) => a.kind === place).slice().sort(byStart);
    const running = ofKind.find((a) => a.state !== "exited");
    if (running && holder !== running.id) {
      const i = desks.indexOf(running.id);
      if (i !== -1) desks[i] = undefined;
      holder = running.id; // a previous, exited one is placed below like a new agent
    }
    if (holder === undefined) {
      const free = ofKind.filter((a) => !desks.includes(a.id));
      holder = free.length > 0 ? free[free.length - 1].id : undefined;
    }
    if (holder !== undefined) own[place] = holder;
  }

  const placed = new Set<string>(desks.filter((id): id is string => id !== undefined));
  for (const id of Object.values(own)) placed.add(id);
  for (const a of agents.filter((x) => !placed.has(x.id)).sort(byStart)) {
    const free = desks.indexOf(undefined);
    if (free === -1) desks.push(a.id);
    else desks[free] = a.id;
  }
  while (desks.length > 0 && desks[desks.length - 1] === undefined) desks.pop();
  return { ...own, desks };
}

// ---------------------------------------------------------------- geometry
// The floor plan (floor.ts, ADR 0045) holds the rooms; this part picks the
// grid of desks and the scale.

export { cell } from "./floor";

export const minRows = 2;

// Columns of regular desks the office may use; the one that shows the office
// largest in the pane wins (ties go to the one closest to four). Three
// columns stack the rooms, which suits a phone.
export const columnChoices = [3, 4, 5, 6] as const;
const preferredColumns = 4;
// Largest drawing scale, in CSS pixels per world pixel.
export const maxScale = 4;

export interface Grid {
  readonly columns: number;
  readonly rows: number;
}

export function gridFor(desks: number, columns: number): Grid {
  return { columns, rows: Math.max(minRows, Math.ceil(desks / columns)) };
}

export function planOf(g: Grid): FloorPlan {
  return floorPlan(g.columns, g.rows);
}

export function worldSize(g: Grid): { w: number; h: number } {
  const p = planOf(g);
  return { w: p.w, h: p.h };
}

// fitGrid picks the columns and the whole number k of device pixels per world
// pixel for a pane of w×h CSS pixels at device pixel ratio dpr. It never goes
// below 2 CSS pixels per world pixel while the width allows it; the pane then
// scrolls vertically.
export function fitGrid(w: number, h: number, dpr: number, desks: number): { grid: Grid; k: number } {
  const cap = Math.max(1, Math.floor(maxScale * dpr));
  let best: { grid: Grid; k: number } | undefined;
  const better = (c: { grid: Grid; k: number }): boolean =>
    !best || c.k > best.k ||
    (c.k === best.k && Math.abs(c.grid.columns - preferredColumns) < Math.abs(best.grid.columns - preferredColumns));
  for (const columns of columnChoices) {
    const grid = gridFor(desks, columns);
    const size = worldSize(grid);
    const k = Math.min(cap, Math.floor(Math.min((w * dpr) / size.w, (h * dpr) / size.h)));
    if (better({ grid, k })) best = { grid, k };
  }
  const fit = best ?? { grid: gridFor(desks, preferredColumns), k: 1 };
  if (fit.k >= 2 * dpr) return fit;
  // Too small to read: fill the width instead, at most 2 CSS pixels.
  best = undefined;
  for (const columns of columnChoices) {
    const grid = gridFor(desks, columns);
    const k = Math.min(Math.ceil(2 * dpr), Math.floor((w * dpr) / worldSize(grid).w));
    if (better({ grid, k })) best = { grid, k };
  }
  const wide = best ?? fit;
  return wide.k > fit.k ? wide : { ...fit, k: Math.max(1, fit.k) };
}

export type Slot = { kind: "lead" } | { kind: "desk"; index: number } | { kind: "station"; station: StationKind };

// slotOrigin is the top-left corner of a desk slot's cell. A station has no
// cell; its origin is chosen so that the nameplate lands under its furniture.
export function slotOrigin(slot: Slot, g: Grid): Point {
  const p = planOf(g);
  if (slot.kind === "lead") return p.lead;
  if (slot.kind === "station") {
    const n = p.stations[slot.station].nameplate;
    return { x: n.x - inCell.nameplate.x, y: n.y - inCell.nameplate.y };
  }
  return p.desks[slot.index] ?? {
    x: margin + (slot.index % g.columns) * cell.w,
    y: p.desks[0].y + Math.floor(slot.index / g.columns) * cell.h,
  };
}

// The lead's whiteboard hangs on the wall above the lead desk.
export function whiteboardAt(g: Grid): Point {
  return planOf(g).whiteboard;
}

// Positions inside a cell.
export const inCell = {
  desk: { x: 10, y: 18 },
  monitor: { x: 12, y: 8 },
  chair: { x: 30, y: 6 },
  person: { x: 30, y: 3 },
  keyboard: { x: 32, y: 21 },
  mug: { x: 47, y: 17 },
  mugRaised: { x: 38, y: 10 },
  magnifier: { x: 23, y: 10 },
  bubble: { x: 45, y: -5 },
  bubbleRaised: { x: 50, y: -9 }, // clear of a raised hand
  nameplate: { x: 32, y: 30 }, // centre of the desk's front panel
  hit: { x: 8, y: -4, w: 48, h: 42 },
} as const;
