// Errands (ADR 0045): where each agent's character should be right now,
// derived from what the UI already sees (agent states, task statuses) and the
// clock. Pure apart from its memory of earlier observations; the scene walks
// characters to wherever this says.
import type { TaskStatus } from "../api";
import type { Spot } from "./floor";
import type { Pose } from "./pose";

export type Place = "desk" | Spot;

export interface Errand {
  readonly place: Place;
  readonly spot: number; // index into the floor plan's spots for the place; -1 at the desk
}

export const atDesk: Errand = { place: "desk", spot: -1 };

// Observed is one agent as the office sees it.
export interface Observed {
  readonly id: string;
  readonly present: boolean; // has a seat (not exited)
  readonly pose?: Pose; // when present
  readonly taskStatus?: TaskStatus;
  readonly phase: number; // 0..1 per agent (lookOf), spreads breaks out
  readonly waitingSince?: number; // when its wait on the user began (ms), the queue's order
}

export type Capacity = Readonly<Record<Spot, number>>;

// Seconds.
export const timing = {
  report: 9, // at the whiteboard after a report, walking included
  coffeeAfter: 4, // idle this long, plus up to coffeeSpread, before a coffee
  coffeeSpread: 4,
  coffee: 10, // a coffee break, walking included
} as const;

const reported = (s: TaskStatus | undefined): boolean => s === "review" || s === "done";

interface Memo {
  present: boolean;
  pose?: Pose;
  status?: TaskStatus;
  phase: number;
  idleSince?: number;
  waitingSince?: number; // when it joined the queue at the user's door
  waitedSince?: number; // when its wait began, as the Approvals drawer sorts it (ms)
  reportUntil?: number;
  reportAt?: number; // when the report came in, to queue reporters fairly
  place: Place;
  spot: number;
}

// Director remembers what it observed and answers where everyone should be.
export class Director {
  private readonly memo = new Map<string, Memo>();
  private observed = false;

  // observe records the world at time now (seconds). The first observation
  // only records: opening the office replays nothing.
  observe(agents: readonly Observed[], now: number): void {
    const first = !this.observed;
    this.observed = true;
    const seen = new Set<string>();
    for (const a of agents) {
      seen.add(a.id);
      let m = this.memo.get(a.id);
      const isNew = m === undefined;
      if (!m) {
        m = { present: a.present, phase: a.phase, place: "desk", spot: -1 };
        this.memo.set(a.id, m);
      }
      // A task that moves to review or done: the agent reported (report_done).
      if (!first && !isNew && !reported(m.status) && reported(a.taskStatus)) {
        m.reportUntil = now + timing.report;
        m.reportAt = now;
      }
      m.status = a.taskStatus;
      m.present = a.present;
      m.pose = a.present ? a.pose : undefined;
      m.phase = a.phase;
      if (m.pose === "idle") m.idleSince ??= now;
      else m.idleSince = undefined;
      if (m.pose === "waiting") m.waitingSince ??= now;
      else m.waitingSince = undefined;
      m.waitedSince = a.waitingSince;
    }
    for (const id of this.memo.keys()) if (!seen.has(id)) this.memo.delete(id);
  }

  // errands says where every agent should be at time now. Agents that have
  // left are listed only while they finish a report; everyone else who is
  // not listed should leave the office.
  errands(now: number, capacity: Capacity): Map<string, Errand> {
    const want = new Map<string, Place>();
    for (const [id, m] of this.memo) {
      const p = this.wish(m, now);
      if (p !== undefined) want.set(id, p);
      else if (!m.present) this.memo.delete(id);
    }
    // Spots: whoever holds one keeps it; the others queue by when they asked, then id.
    const out = new Map<string, Errand>();
    const taken: Record<Spot, Set<number>> = { whiteboard: new Set(), coffee: new Set(), review: new Set(), queue: new Set() };
    const waiting: [string, Memo, Spot][] = [];
    for (const [id, place] of want) {
      const m = this.memo.get(id) as Memo;
      if (place === "queue") {
        waiting.push([id, m, place]); // a queue moves up: places are handed out afresh
      } else if (place === "desk") {
        m.place = "desk";
        m.spot = -1;
        out.set(id, atDesk);
      } else if (m.place === place && m.spot >= 0 && m.spot < capacity[place] && !taken[place].has(m.spot)) {
        taken[place].add(m.spot);
        out.set(id, { place, spot: m.spot });
      } else waiting.push([id, m, place]);
    }
    const order = (m: Memo, place: Spot): number =>
      place === "whiteboard" ? (m.reportAt ?? 0)
        : place === "coffee" ? (m.idleSince ?? 0) + this.coffeeDelay(m)
        : place === "queue" ? (m.waitedSince !== undefined ? m.waitedSince / 1000 : (m.waitingSince ?? 0)) : 0;
    waiting.sort(([ia, ma, pa], [ib, mb, pb]) => order(ma, pa) - order(mb, pb) || (ia < ib ? -1 : ia > ib ? 1 : 0));
    for (const [id, m, place] of waiting) {
      let spot = -1;
      for (let i = 0; i < capacity[place]; i++) {
        if (!taken[place].has(i)) {
          spot = i;
          break;
        }
      }
      if (spot === -1) {
        // no free spot: the errand is skipped
        if (!m.present) continue; // a leaver without a spot just leaves
        m.place = "desk";
        m.spot = -1;
        out.set(id, atDesk);
        continue;
      }
      taken[place].add(spot);
      m.place = place;
      m.spot = spot;
      out.set(id, { place, spot });
    }
    return out;
  }

  private coffeeDelay(m: Memo): number {
    return timing.coffeeAfter + timing.coffeeSpread * m.phase;
  }

  // wish is where an agent wants to be, before spots are handed out.
  private wish(m: Memo, now: number): Place | undefined {
    const reporting = m.reportUntil !== undefined && now < m.reportUntil;
    if (!reporting) m.reportUntil = undefined;
    if (!m.present) return reporting ? "whiteboard" : undefined;
    switch (m.pose) {
      case "waiting":
        return "queue"; // the user comes first: to the user's door (ADR 0045)
      case "booting":
        return "desk"; // a fresh start
      default:
    }
    if (reporting) return "whiteboard";
    switch (m.pose) {
      case "reviewing":
        return "review";
      case "idle": {
        if (m.idleSince === undefined) return "desk";
        const from = m.idleSince + this.coffeeDelay(m);
        return now >= from && now < from + timing.coffee ? "coffee" : "desk";
      }
      default:
        return "desk";
    }
  }
}
