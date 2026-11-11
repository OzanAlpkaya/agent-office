import { describe, expect, it } from "vitest";
import * as art from "./art";
import { type Point, type Rect, cell, floorPlan, roomAt, stationKinds } from "./floor";
import { columnChoices } from "./layout";
import { clear, isFree, length, route, walkGrid, along } from "./paths";
import { exitOf, targets } from "./testkit";

const plans: [number, number][] = columnChoices.flatMap((c) => [2, 4].map((r): [number, number] => [c, r]));

const inside = (p: Point, r: Rect): boolean => p.x >= r.x && p.x < r.x + r.w && p.y >= r.y && p.y < r.y + r.h;


describe.each(plans)("floor plan with %i columns and %i rows", (columns, rows) => {
  const p = floorPlan(columns, rows);

  it("has eight rooms inside the world that do not overlap", () => {
    expect(p.rooms.map((r) => r.id).sort()).toEqual(["kitchen", "lead", "lobby", "planning", "qa", "review", "user", "work"]);
    expect(p.stacked).toBe(columns === 3);
    for (const r of p.rooms) {
      expect(r.rect.x).toBeGreaterThanOrEqual(0);
      expect(r.rect.y).toBeGreaterThanOrEqual(0);
      expect(r.rect.x + r.rect.w).toBeLessThanOrEqual(p.w);
      expect(r.rect.y + r.rect.h).toBeLessThanOrEqual(p.h);
      expect(r.rect.w).toBeGreaterThanOrEqual(56);
    }
    for (let i = 0; i < p.rooms.length; i++) {
      for (let j = i + 1; j < p.rooms.length; j++) {
        const a = p.rooms[i].rect, b = p.rooms[j].rect;
        const overlap = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
        expect(overlap, `${p.rooms[i].id} and ${p.rooms[j].id}`).toBe(false);
      }
    }
  });

  it("puts every desk in the workroom and the lead's desk, whiteboard and spots in the lead's room", () => {
    expect(p.desks).toHaveLength(columns * rows);
    const room = (pt: Point) => roomAt(p, pt)?.id;
    for (const d of p.desks) {
      expect(room(d)).toBe("work");
      expect(room({ x: d.x + cell.w - 1, y: d.y + cell.h - 1 })).toBe("work");
    }
    expect(room(p.lead)).toBe("lead");
    expect(room({ x: p.whiteboard.x, y: p.whiteboard.y + art.wallHeight })).toBe("lead");
    expect(room({ x: p.whiteboard.x + art.whiteboardSize.w - 1, y: p.whiteboard.y + art.wallHeight })).toBe("lead");
    for (const s of p.spots.whiteboard) expect(room(s)).toBe("lead");
    for (const s of p.spots.review) expect(room(s)).toBe("review");
    for (const s of p.spots.coffee) expect(room(s)).toBe("kitchen");
    expect(room(p.entrance)).toBe("lobby");
    expect(room(p.stations.architect.seat)).toBe("planning");
    expect(room(p.stations.qa.seat)).toBe("qa");
    expect(room(p.stations.assistant.seat)).toBe("lobby");
    for (const k of stationKinds) {
      const f = p.stations[k].furniture;
      expect(room({ x: f.x, y: f.y }), k).toBe(room(p.stations[k].seat));
      expect(room({ x: f.x + f.w - 1, y: f.y + f.h - 1 }), k).toBe(room(p.stations[k].seat));
    }
    expect(room(p.userDesk)).toBe("user");
    // the queue starts in the lobby, right under the user's door
    expect(room(p.spots.queue[0])).toBe("lobby");
    expect(Math.abs(p.spots.queue[0].x - (p.userDoor.x + p.userDoor.w / 2))).toBeLessThanOrEqual(2);
    expect(room({ x: p.userDoor.x + 1, y: p.userDoor.y - 1 })).toBe("user");
    expect(room({ x: p.userDoor.x + 1, y: p.userDoor.y + p.userDoor.h })).toBe("lobby");
    expect(room({ x: p.table.x, y: p.table.y })).toBe("review");
    expect(room({ x: p.table.x + p.table.w - 1, y: p.table.y })).toBe("review");
  });

  it("has enough places for errands", () => {
    expect(p.spots.whiteboard.length).toBeGreaterThanOrEqual(3);
    expect(p.spots.coffee.length).toBeGreaterThanOrEqual(3);
    expect(p.spots.review.length).toBeGreaterThanOrEqual(2);
    // a long queue: more agents than that wait at their desk (the sign still counts them)
    expect(p.spots.queue.length).toBeGreaterThanOrEqual(16);
  });

  it("draws on whole pixels", () => {
    const pts = [p.lead, ...p.desks, p.whiteboard, p.screen, p.counter, p.door, p.cooler, ...p.plants, ...p.windows,
      p.userDesk, p.blueprints, p.testBoard, ...stationKinds.map((k) => p.stations[k].furniture),
      ...p.rooms.map((r) => r.label)];
    for (const pt of pts) expect(Number.isInteger(pt.x) && Number.isInteger(pt.y), JSON.stringify(pt)).toBe(true);
    for (const r of [...p.faces, ...p.walls, ...p.doorways, p.table]) expect(Number.isInteger(r.x + r.y + r.w + r.h)).toBe(true);
  });

  it("can stand at every spot, the entrance and every desk's exit", () => {
    const g = walkGrid(p);
    for (const [name, t] of targets(p)) expect(isFree(g, t), name).toBe(true);
    for (const d of p.desks) expect(isFree(g, exitOf(d)), `desk ${JSON.stringify(d)}`).toBe(true);
  });

  it("finds a clear route from every desk to every place", () => {
    const g = walkGrid(p);
    for (const d of [p.lead, ...p.desks.filter((_, i) => i % 2 === 0 || i === p.desks.length - 1)]) {
      for (const [name, t] of targets(p)) {
        const r = route(p, exitOf(d), t);
        expect(r[0]).toEqual(exitOf(d));
        expect(r[r.length - 1]).toEqual(t);
        for (let i = 1; i < r.length; i++) expect(clear(g, r[i - 1], r[i]), `${name} from ${JSON.stringify(d)}, segment ${i}`).toBe(true);
        // no wild detours: at most three times the straight line, plus the way through rooms
        expect(length(r)).toBeLessThan(3 * Math.hypot(t.x - exitOf(d).x, t.y - exitOf(d).y) + 400);
      }
    }
  });

  it("goes through doorways, not walls", () => {
    // from the lead's desk to the kitchen, every route point is on the floor
    const r = route(p, exitOf(p.lead), p.spots.coffee[0]);
    const g = walkGrid(p);
    for (const pt of r) expect(isFree(g, pt)).toBe(true);
    for (const w of p.walls) {
      for (let i = 1; i < r.length; i++) {
        const mid = { x: (r[i - 1].x + r[i].x) / 2, y: (r[i - 1].y + r[i].y) / 2 };
        expect(inside(mid, w)).toBe(false);
      }
    }
  });
});

describe("paths", () => {
  it("are deterministic", () => {
    const p = floorPlan(4, 3);
    expect(route(p, exitOf(p.desks[5]), p.entrance)).toEqual(route(p, exitOf(p.desks[5]), p.entrance));
  });

  it("walk along a route by distance", () => {
    const pts = [{ x: 0, y: 0 }, { x: 10, y: 0 }, { x: 10, y: 10 }];
    expect(length(pts)).toBe(20);
    expect(along(pts, 5)).toEqual({ at: { x: 5, y: 0 }, segment: 0 });
    expect(along(pts, 15)).toEqual({ at: { x: 10, y: 5 }, segment: 1 });
    expect(along(pts, 99).at).toEqual({ x: 10, y: 10 });
  });

  it("without a route the character is simply there, never walking through a wall", () => {
    const p = floorPlan(4, 2);
    expect(route(p, exitOf(p.desks[0]), { x: -400, y: -400 })).toEqual([{ x: -400, y: -400 }]);
  });

  it("the plan is cached per grid", () => {
    expect(floorPlan(5, 2)).toBe(floorPlan(5, 2));
  });
});
