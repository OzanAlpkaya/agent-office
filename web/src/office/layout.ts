// Desk assignment and office geometry (ADR 0024). Pure: the scene and the
// HTML nameplates both place things from these numbers, in world pixels.
import type { Agent } from "../api";
import { wallHeight, whiteboardSize } from "./art";

// Assignment says who sits where. desks[i] is the agent at regular desk i.
export interface Assignment {
  readonly lead?: string;
  readonly desks: readonly (string | undefined)[];
}

export const emptyAssignment: Assignment = { desks: [] };

const byStart = (x: Agent, y: Agent): number =>
  Date.parse(x.started_at) - Date.parse(y.started_at) || (x.id < y.id ? -1 : x.id > y.id ? 1 : 0);

// assignDesks keeps every agent at the desk it already has for as long as it
// exists. New agents take the lowest free desk, in start order; removed agents
// free theirs. The lead desk belongs to the running lead, or else to the lead
// already there; a lead that has exited moves to a regular desk when a new
// lead starts. Applying it twice gives the same result.
export function assignDesks(prev: Assignment, agents: readonly Agent[]): Assignment {
  const present = new Set(agents.map((a) => a.id));
  const desks = prev.desks.map((id) => (id !== undefined && present.has(id) ? id : undefined));
  let lead = prev.lead !== undefined && present.has(prev.lead) ? prev.lead : undefined;

  const leads = agents.filter((a) => a.kind === "lead").slice().sort(byStart);
  const running = leads.find((a) => a.state !== "exited");
  if (running && lead !== running.id) {
    const i = desks.indexOf(running.id);
    if (i !== -1) desks[i] = undefined;
    lead = running.id; // a previous, exited lead is placed below like a new agent
  }
  if (lead === undefined) {
    const free = leads.filter((a) => !desks.includes(a.id));
    lead = free.length > 0 ? free[free.length - 1].id : undefined;
  }

  const placed = new Set<string>(desks.filter((id): id is string => id !== undefined));
  if (lead !== undefined) placed.add(lead);
  for (const a of agents.filter((x) => !placed.has(x.id)).sort(byStart)) {
    const free = desks.indexOf(undefined);
    if (free === -1) desks.push(a.id);
    else desks[free] = a.id;
  }
  while (desks.length > 0 && desks[desks.length - 1] === undefined) desks.pop();
  return { lead, desks };
}

// ---------------------------------------------------------------- geometry

export const minRows = 2;
export const cell = { w: 64, h: 52 } as const;
export const world = { margin: 16, leadRowY: wallHeight, leadRowH: 58, bottom: 10 } as const;

// Columns of regular desks the office may use; the one that shows the office
// largest in the pane wins (ties go to the one closest to four).
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

export function worldSize(g: Grid): { w: number; h: number } {
  return {
    w: g.columns * cell.w + 2 * world.margin,
    h: world.leadRowY + world.leadRowH + g.rows * cell.h + world.bottom,
  };
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

export type Slot = { kind: "lead" } | { kind: "desk"; index: number };

// slotOrigin is the top-left corner of a slot's cell.
export function slotOrigin(slot: Slot, g: Grid): { x: number; y: number } {
  if (slot.kind === "lead") return { x: (worldSize(g).w - cell.w) / 2, y: world.leadRowY + 4 };
  return {
    x: world.margin + (slot.index % g.columns) * cell.w,
    y: world.leadRowY + world.leadRowH + Math.floor(slot.index / g.columns) * cell.h,
  };
}

// The lead's whiteboard hangs on the wall above the lead desk.
export function whiteboardAt(g: Grid): { x: number; y: number } {
  return { x: (worldSize(g).w - whiteboardSize.w) / 2, y: 6 };
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
