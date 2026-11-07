// Desk assignment and office geometry (ADR 0024). Pure: the scene and the
// HTML nameplates both place things from these numbers, in world pixels.
import type { Agent } from "../api";
import { wallHeight } from "./art";

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

  const leads = agents.filter((a) => a.role === "lead").slice().sort(byStart);
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

export const columns = 4;
export const minRows = 2;
export const cell = { w: 64, h: 52 } as const;
export const world = { w: 320, margin: 32, leadRowY: wallHeight, leadRowH: 58, bottom: 10 } as const;

export function rowsFor(a: Assignment): number {
  return Math.max(minRows, Math.ceil(a.desks.length / columns));
}

export function worldSize(rows: number): { w: number; h: number } {
  return { w: world.w, h: world.leadRowY + world.leadRowH + rows * cell.h + world.bottom };
}

export type Slot = { kind: "lead" } | { kind: "desk"; index: number };

// slotOrigin is the top-left corner of a slot's cell.
export function slotOrigin(slot: Slot): { x: number; y: number } {
  if (slot.kind === "lead") return { x: (world.w - cell.w) / 2, y: world.leadRowY + 4 };
  return {
    x: world.margin + (slot.index % columns) * cell.w,
    y: world.leadRowY + world.leadRowH + Math.floor(slot.index / columns) * cell.h,
  };
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
  nameplate: { x: 32, y: 30 }, // centre of the desk's front panel
  hit: { x: 8, y: -4, w: 48, h: 42 },
} as const;
