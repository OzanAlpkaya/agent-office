// Custom office layouts (Phase 18, ADR 0057): the presets, parsed by the
// daemon (internal/office writes presets.gen.json), become floor plans that
// characters can walk: every spot reachable from every desk, through doors.
import { describe, expect, it } from "vitest";
import type { Agent, OfficeLayout } from "../api";
import presets from "./presets.gen.json";
import { customPlan, deskCapacity } from "./custom";
import { type Point, cell, roomAt } from "./floor";
import { assignDesks, fitGrid, placesOf, planOf, slotOrigin } from "./layout";
import { buildOffice } from "./model";
import { clear, isFree, route, walkGrid } from "./paths";
import { exitOf, targets } from "./testkit";

const layouts = presets as Record<string, OfficeLayout>;

describe.each(Object.keys(layouts))("preset %s", (name) => {
  const l = layouts[name];
  const p = customPlan(l);

  it("has the layout's rooms, its desks in the workroom and its size", () => {
    expect(p.custom).toBe(true);
    expect(p.capacity).toBe(l.desks);
    expect(p.w).toBe(l.columns * 16);
    expect(p.h).toBe(l.rows * 16);
    expect(p.rooms.map((r) => r.id).sort()).toEqual(l.rooms.map((r) => r.id).sort());
    expect(p.desks).toHaveLength(l.desks);
    const room = (pt: Point) => roomAt(p, pt)?.id;
    for (const d of p.desks) {
      expect(room(d)).toBe("work");
      expect(room({ x: d.x + cell.w - 1, y: d.y + cell.h - 1 })).toBe("work");
    }
    expect(room(p.lead)).toBe("lead");
    expect(room(p.userDesk)).toBe("user");
    expect(room(p.entrance)).toBe("lobby");
    for (const s of p.spots.coffee) expect(room(s)).toBe("kitchen");
    for (const s of p.spots.review) expect(room(s)).toBe("review");
    expect(p.spots.queue.length).toBeGreaterThanOrEqual(4);
    // the queue starts just outside the user's door, in the next room
    const door = { x: p.userDoor.x + p.userDoor.w / 2, y: p.userDoor.y + p.userDoor.h / 2 };
    const q0 = p.spots.queue[0];
    expect(Math.hypot(q0.x - door.x, q0.y - door.y), JSON.stringify({ q0, door })).toBeLessThanOrEqual(40);
    expect(room(q0)).not.toBe("user");
  });

  it("has a station and furniture only for the rooms it has", () => {
    const has = (id: string): boolean => l.rooms.some((r) => r.id === id);
    expect(p.stations.architect !== undefined).toBe(has("planning"));
    expect(p.stations.qa !== undefined).toBe(has("qa"));
    expect(p.stations.assistant).toBeDefined();
    expect(p.table !== undefined).toBe(has("review"));
    expect(p.counter !== undefined).toBe(has("kitchen"));
    expect(p.spots.coffee.length > 0).toBe(has("kitchen"));
    expect(placesOf(p)).toEqual(["lead", ...(has("planning") ? ["architect"] : []), ...(has("qa") ? ["qa"] : []), "assistant"]);
  });

  it("can stand at every spot and desk exit, and walk from every desk to every place", () => {
    const g = walkGrid(p);
    for (const [what, t] of targets(p)) expect(isFree(g, t), what).toBe(true);
    for (const [i, q] of p.spots.queue.entries()) expect(isFree(g, q), `queue ${i}`).toBe(true);
    for (const d of [p.lead, ...p.desks]) {
      expect(isFree(g, exitOf(d)), `desk ${JSON.stringify(d)}`).toBe(true);
      for (const [what, t] of targets(p)) {
        const r = route(p, exitOf(d), t);
        expect(r.length, `${what} from ${JSON.stringify(d)}`).toBeGreaterThan(1);
        for (let i = 1; i < r.length; i++) expect(clear(g, r[i - 1], r[i]), `${what} from ${JSON.stringify(d)}`).toBe(true);
      }
    }
  });

  it("matches the daemon's desk capacity", () => {
    const work = l.rooms.find((r) => r.id === "work");
    expect(work).toBeDefined();
    expect(deskCapacity(work as { w: number; h: number })).toBeGreaterThanOrEqual(l.desks);
  });

  it("fits a pane at whole pixels, and is cached", () => {
    const { grid, k } = fitGrid(1200, 800, 2, 3, l);
    expect(grid.layout).toBe(l);
    expect(planOf(grid)).toBe(p);
    expect(k).toBeGreaterThanOrEqual(2);
    expect(customPlan(JSON.parse(JSON.stringify(l)) as OfficeLayout)).toBe(p);
    expect(slotOrigin({ kind: "user" }, grid)).toEqual(p.userDesk);
  });
});

const agent = (id: string, kind: Agent["kind"] = "worker", minute = 0): Agent => ({
  id, name: id, kind, role: kind === "lead" ? "tech-lead" : kind, state: "working", sandbox: "podman", branch: "b", worktree_path: "/w",
  has_terminal: true, started_at: new Date(Date.parse("2026-11-12T10:00:00Z") + minute * 60_000).toISOString(),
} as Agent);

describe("more workers than desks", () => {
  it("leaves the newest without a desk and gives a freed desk to the one waiting longest", () => {
    const workers = [0, 1, 2, 3, 4].map((i) => agent(`w${i}`, "worker", i));
    let a = assignDesks({ desks: [] }, workers, ["lead"], 3);
    expect(a.desks).toEqual(["w0", "w1", "w2", "w3", "w4"]); // w3 and w4 have none (indexes 3 and 4)
    a = assignDesks(a, workers.filter((w) => w.id !== "w1"), ["lead"], 3);
    expect(a.desks).toEqual(["w0", "w3", "w2", undefined, "w4"]); // w3 waited longest
    a = assignDesks(a, workers.filter((w) => w.id !== "w1"), ["lead"], 3); // stable
    expect(a.desks).toEqual(["w0", "w3", "w2", undefined, "w4"]);
  });

  it("seats only agents with a desk; the rest are listed", () => {
    const compact = layouts.compact;
    const agents = [agent("lead", "lead"), agent("arch", "architect", 1), ...[2, 3, 4, 5, 6, 7, 8, 9].map((i) => agent(`w${i}`, "worker", i))];
    const m = buildOffice({ desks: [] }, agents, [], [], { layout: compact });
    // no planning room: the architect takes a desk like a worker
    expect(m.seats.find((s) => s.agent.id === "arch")?.slot.kind).toBe("desk");
    expect(m.seats.filter((s) => s.slot.kind === "desk")).toHaveLength(compact.desks);
    expect(m.deskless.map((a) => a.id)).toEqual(["w7", "w8", "w9"]); // 9 need a desk, 6 have one
  });

  it("the default plan grows instead", () => {
    const agents = [...Array(12).keys()].map((i) => agent(`w${i}`, "worker", i));
    const m = buildOffice({ desks: [] }, agents, [], []);
    expect(m.deskless).toEqual([]);
    expect(m.seats).toHaveLength(12);
  });
});
