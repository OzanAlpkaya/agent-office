// Desk assignment and office geometry (ADR 0024). Pure: the scene and the
// HTML nameplates both place things from these numbers, in world pixels.
import type { Agent, OfficeLayout } from "../api";
import { customPlan } from "./custom";
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
//
// A custom layout (Phase 18, ADR 0057) may lack the rooms of some places
// (places lists the ones it has: the others' agents take desks) and has
// capacity desks: agents beyond them have none, and a desk that frees up goes
// to the one that has waited longest (the lowest index beyond capacity).
export function assignDesks(prev: Assignment, agents: readonly Agent[], places: readonly OwnPlace[] = ownPlaces, capacity?: number): Assignment {
  const present = new Set(agents.map((a) => a.id));
  const desks = prev.desks.map((id) => (id !== undefined && present.has(id) ? id : undefined));
  const own: Partial<Record<OwnPlace, string>> = {};
  for (const place of places) {
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
  if (capacity !== undefined) {
    // Agents beyond the desks move to a desk that freed up, longest waiting first.
    for (let i = capacity; i < desks.length; i++) {
      const id = desks[i];
      if (id === undefined) continue;
      const free = desks.indexOf(undefined);
      if (free === -1 || free >= capacity) break;
      desks[free] = id;
      desks[i] = undefined;
    }
  }
  while (desks.length > 0 && desks[desks.length - 1] === undefined) desks.pop();
  return { ...own, desks };
}

// placesOf lists the own places a floor plan has: the lead desk, and the
// stations whose rooms it has.
export function placesOf(plan: FloorPlan): OwnPlace[] {
  return ownPlaces.filter((p) => p === "lead" || plan.stations[p] !== undefined);
}

// ---------------------------------------------------------------- geometry
// The floor plan (floor.ts, ADR 0045) holds the rooms; this part picks the
// grid of desks and the scale.

export { cell } from "./floor";

export const minRows = 2;

// Columns of regular desks the office may use. Three stack the rooms, which
// suits a phone; more spread the office over a wide screen.
export const columnChoices = [3, 4, 5, 6, 7, 8, 9, 10] as const;
const preferredColumns = 4;
// Largest drawing scale, in CSS pixels per world pixel.
export const maxScale = 4;
// Floor added below the desks to cover a tall pane: in steps, at most.
const extraStep = 16;
const maxExtra = 2048;

export interface Grid {
  readonly columns: number;
  readonly rows: number;
  readonly layout?: OfficeLayout; // a project's own layout: a fixed plan (Phase 18)
  readonly extra?: number; // floor added below the workroom's desks to cover a tall pane, world pixels
}

export function gridFor(desks: number, columns: number): Grid {
  return { columns, rows: Math.max(minRows, Math.ceil(desks / columns)) };
}

export function planOf(g: Grid): FloorPlan {
  return g.layout ? customPlan(g.layout) : floorPlan(g.columns, g.rows, g.extra ?? 0);
}

// sameGrid says whether two grids give the same plan.
// A layout parsed again (any project update) is the same plan: compared by plan, not object (code review).
export const sameGrid = (a: Grid, b: Grid): boolean =>
  a.columns === b.columns && a.rows === b.rows && (a.extra ?? 0) === (b.extra ?? 0) &&
  (a.layout === b.layout || (!!a.layout && !!b.layout && planOf(a) === planOf(b)));

export function worldSize(g: Grid): { w: number; h: number } {
  const p = planOf(g);
  return { w: p.w, h: p.h };
}

// fitGrid picks the columns, the rows and the whole number k of device
// pixels per world pixel for a pane of w×h CSS pixels at device pixel ratio
// dpr (Phase 18 review): the office fills the pane's width as well as whole
// pixels allow (pixel art stays sharp), at most maxScale; then it gets desk
// rows until it covers the pane's height, so there is no empty band above or
// below it. A taller office scrolls. A custom layout is fixed: only its scale
// is chosen, by width.
export function fitGrid(w: number, h: number, dpr: number, desks: number, layout?: OfficeLayout): { grid: Grid; k: number } {
  const cap = Math.max(1, Math.floor(maxScale * dpr));
  const W = w * dpr;
  const H = h * dpr;
  if (layout) {
    const p = customPlan(layout);
    return { grid: { columns: p.columns, rows: p.rows, layout }, k: Math.max(1, Math.min(cap, Math.floor(W / p.w))) };
  }
  // Readable first: a plan drawn under 1.5 CSS pixels is a candidate only
  // when nothing else fits (a phone would otherwise fit the wide plan, tiny).
  const readable = Math.ceil(1.5 * dpr);
  const fits = (columns: number): number => Math.max(1, Math.min(cap, Math.floor(W / worldSize(gridFor(desks, columns)).w)));
  const minK = columnChoices.some((c) => fits(c) >= readable) ? readable : 1;
  let best: { columns: number; k: number; left: number } | undefined;
  for (const columns of columnChoices) {
    const ww = worldSize(gridFor(desks, columns)).w;
    const k = fits(columns);
    const left = W - ww * k; // unused width, device pixels
    if (left < 0 || k < minK) continue;
    const better = !best || left < best.left - 0.5 ||
      (Math.abs(left - best.left) <= 0.5 && (k > best.k || (k === best.k &&
        Math.abs(columns - preferredColumns) < Math.abs(best.columns - preferredColumns))));
    if (better) best = { columns, k, left };
  }
  const columns = best?.columns ?? columnChoices[0];
  const k = best?.k ?? 1;
  const grid = gridFor(desks, columns);
  // Deepen the workroom's floor (not more desks) until the office covers the
  // pane, in whole tiles so a small resize does not rebuild it.
  const short = H / k - worldSize(grid).h;
  const extra = short > 0 ? Math.min(maxExtra, Math.ceil(short / extraStep) * extraStep) : 0;
  return { grid: extra > 0 ? { ...grid, extra } : grid, k };
}

export type Slot = { kind: "lead" } | { kind: "desk"; index: number } | { kind: "station"; station: StationKind } | { kind: "user" };

// slotOrigin is the top-left corner of a desk slot's cell. A station has no
// cell; its origin is chosen so that the nameplate lands under its furniture.
export function slotOrigin(slot: Slot, g: Grid): Point {
  const p = planOf(g);
  if (slot.kind === "lead") return p.lead;
  if (slot.kind === "user") return p.userDesk;
  if (slot.kind === "station") {
    const st = p.stations[slot.station];
    if (!st) return p.lead; // not assigned: the plan has no such room
    const n = st.nameplate;
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
