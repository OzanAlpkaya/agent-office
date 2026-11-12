import { describe, expect, it } from "vitest";
import { type Assignment, assignDesks, cell, columnChoices, emptyAssignment, fitGrid, gridFor, inCell, maxScale, slotOrigin, whiteboardAt, worldSize } from "./layout";
import { wallHeight, whiteboardSize } from "./art";
import { agent } from "./testutil";

const at = (min: number): string => `2026-11-08T10:${String(min).padStart(2, "0")}:00Z`;

describe("assignDesks", () => {
  it("seats agents in start order on an empty office", () => {
    const a = assignDesks(emptyAssignment, [agent("c", { started_at: at(3) }), agent("a", { started_at: at(1) }), agent("b", { started_at: at(2) })]);
    expect(a).toEqual({ lead: undefined, desks: ["a", "b", "c"] });
  });

  it("keeps desks when states change and frees desks of removed agents", () => {
    const one = assignDesks(emptyAssignment, ["a", "b", "c"].map((id, i) => agent(id, { started_at: at(i) })));
    const two = assignDesks(one, [agent("a", { state: "idle" }), agent("c", { state: "waiting_permission" })]);
    expect(two.desks).toEqual(["a", undefined, "c"]);
    // a new agent takes the free desk, not a new one
    const three = assignDesks(two, [agent("a"), agent("c"), agent("d", { started_at: at(9) })]);
    expect(three.desks).toEqual(["a", "d", "c"]);
  });

  it("trims free desks at the end", () => {
    const one = assignDesks(emptyAssignment, ["a", "b", "c"].map((id, i) => agent(id, { started_at: at(i) })));
    expect(assignDesks(one, [agent("a")]).desks).toEqual(["a"]);
  });

  it("gives the lead desk to the lead", () => {
    const a = assignDesks(emptyAssignment, [agent("w1", { started_at: at(2) }), agent("L", { kind: "lead", started_at: at(1) })]);
    expect(a).toEqual({ lead: "L", desks: ["w1"] });
  });

  it("moves an exited lead to a regular desk when a new lead starts", () => {
    const first = assignDesks(emptyAssignment, [agent("L1", { kind: "lead", started_at: at(1) }), agent("w", { started_at: at(2) })]);
    const exited = assignDesks(first, [agent("L1", { kind: "lead", state: "exited" }), agent("w")]);
    expect(exited.lead).toBe("L1"); // stays while no other lead runs
    const second = assignDesks(exited, [
      agent("L1", { kind: "lead", state: "exited", started_at: at(1) }), agent("w", { started_at: at(2) }),
      agent("L2", { kind: "lead", started_at: at(5) }),
    ]);
    expect(second).toEqual({ lead: "L2", desks: ["w", "L1"] });
  });

  it("is idempotent", () => {
    const agents = [
      agent("L1", { kind: "lead", state: "exited", started_at: at(1) }), agent("w1", { started_at: at(2) }),
      agent("L2", { kind: "lead", started_at: at(5) }), agent("r", { kind: "reviewer", started_at: at(6) }),
    ];
    const once = assignDesks({ lead: "L1", desks: ["w1"] }, agents);
    expect(assignDesks(once, agents)).toEqual(once);
  });

  it("gives every agent exactly one seat", () => {
    const agents = Array.from({ length: 23 }, (_, i) => agent(`a${i}`, { kind: i === 4 ? "lead" : "worker", started_at: at(i) }));
    let a: Assignment = emptyAssignment;
    a = assignDesks(a, agents.slice(0, 10));
    a = assignDesks(a, agents.filter((_, i) => i % 3 !== 0));
    a = assignDesks(a, agents);
    const seated = [a.lead, ...a.desks].filter((x) => x !== undefined);
    expect(new Set(seated).size).toBe(seated.length);
    expect(seated.sort()).toEqual(agents.map((x) => x.id).sort());
  });
});

describe("geometry", () => {
  it("has at least two rows and grows by rows", () => {
    expect(gridFor(0, 4)).toEqual({ columns: 4, rows: 2 });
    expect(gridFor(8, 4).rows).toBe(2);
    expect(gridFor(9, 4).rows).toBe(3);
    expect(gridFor(9, 5).rows).toBe(2);
    expect(worldSize(gridFor(9, 4)).h - worldSize(gridFor(8, 4)).h).toBe(cell.h);
  });

  it.each(columnChoices.flatMap((c) => [0, 7, 13].map((n) => [c, n])))(
    "%i columns, %i desks: cells inside the world, on whole pixels, not overlapping", (columns, desks) => {
      const grid = gridFor(desks, columns);
      const size = worldSize(grid);
      const cells = [slotOrigin({ kind: "lead" }, grid),
        ...Array.from({ length: grid.rows * grid.columns }, (_, index) => slotOrigin({ kind: "desk", index }, grid))];
      for (const c of cells) {
        expect(c.x).toBeGreaterThanOrEqual(0);
        expect(c.x + cell.w).toBeLessThanOrEqual(size.w);
        expect(c.y + cell.h).toBeLessThanOrEqual(size.h);
        expect(Number.isInteger(c.x) && Number.isInteger(c.y)).toBe(true);
      }
      for (let i = 0; i < cells.length; i++) {
        for (let j = i + 1; j < cells.length; j++) {
          const overlap = Math.abs(cells[i].x - cells[j].x) < cell.w && Math.abs(cells[i].y - cells[j].y) < cell.h;
          expect(overlap, `cells ${i} and ${j}`).toBe(false);
        }
      }
      expect(slotOrigin({ kind: "lead" }, grid).y).toBeGreaterThanOrEqual(wallHeight);
      const wb = whiteboardAt(grid);
      expect(wb.x).toBeGreaterThanOrEqual(0);
      expect(wb.x + whiteboardSize.w).toBeLessThanOrEqual(size.w);
      expect(Number.isInteger(wb.x)).toBe(true);
    });

  it("keeps the nameplate on the desk", () => {
    expect(inCell.nameplate.y).toBeGreaterThan(inCell.desk.y);
  });
});

describe("fitGrid", () => {
  it("fits the whole floor in the pane, width and height, at the largest whole-pixel scale", () => {
    for (const [w, h, dpr] of [[1176, 808, 1], [1600, 1000, 2], [412, 700, 2.625], [390, 640, 3], [2400, 1300, 1]] as const) {
      const r = fitGrid(w, h, dpr, 6);
      const size = worldSize(r.grid);
      expect(size.w * r.k, `${w}x${h} width`).toBeLessThanOrEqual(w * dpr);
      expect(size.h * r.k, `${w}x${h} height`).toBeLessThanOrEqual(h * dpr);
      // one device pixel more and the plan would no longer fit (unless capped)
      const bare = worldSize({ ...r.grid, extra: 0 });
      expect(bare.w * (r.k + 1) > w * dpr || bare.h * (r.k + 1) > h * dpr || r.k === Math.floor(maxScale * dpr), `${w}x${h}`).toBe(true);
    }
  });

  it("uses the width: more columns on a wide pane, the height left filled with floor", () => {
    const desk = fitGrid(1176, 808, 1, 6);
    expect(desk.k).toBe(2);
    expect(desk.grid.columns).toBeGreaterThan(4);
    expect(1176 - worldSize(desk.grid).w * desk.k).toBeLessThan(128);
    expect(808 - worldSize(desk.grid).h * desk.k).toBeLessThan(16 * desk.k);
    expect(desk.grid.rows).toBe(2); // only the desks the team needs
  });

  it("zooms about a third larger, and then may scroll", () => {
    const fit = fitGrid(1176, 808, 1, 6);
    const big = fitGrid(1176, 808, 1, 6, undefined, "larger");
    expect(big.k).toBe(fit.k + 1);
    expect(big.grid.columns).toBe(fit.grid.columns);
    expect(worldSize(big.grid).h * big.k).toBeGreaterThan(808);
    const phone = fitGrid(412, 700, 2.625, 9);
    const phoneBig = fitGrid(412, 700, 2.625, 9, undefined, "larger");
    expect(phoneBig.k).toBe(phone.k + 1);
    expect(worldSize(phoneBig.grid).w * phoneBig.k).toBeLessThanOrEqual(412 * 2.625); // no sideways scrolling
  });

  it("stacks the rooms on a phone", () => {
    const phone = fitGrid(412, 700, 2.625, 6);
    expect(phone.grid.columns).toBe(3);
    expect(phone.k / 2.625).toBeGreaterThanOrEqual(1.5);
  });

  it("caps the scale", () => {
    expect(fitGrid(5000, 5000, 1, 3).k).toBe(maxScale);
    expect(fitGrid(5000, 5000, 2, 3).k).toBe(maxScale * 2);
  });

  it("works in device pixels", () => {
    // at 1.5 device pixels per CSS pixel, k counts device pixels
    const r = fitGrid(1300, 1100, 1.5, 9);
    const size = worldSize(r.grid);
    expect((size.w * r.k) / 1.5).toBeLessThanOrEqual(1300);
    expect(r.k).toBeGreaterThanOrEqual(4);
  });

  it("fits a short pane too, small", () => {
    expect(fitGrid(1000, 200, 1, 9).k).toBe(1);
  });

  it("never picks a plan wider than the pane when a narrower one fits", () => {
    // a phone-sized pane at one device pixel per CSS pixel: wider plans exist at that scale, none is picked
    for (const h of [500, 650, 760]) {
      const r = fitGrid(390, h, 1, 9);
      expect(worldSize(r.grid).w * r.k, `390x${h}`).toBeLessThanOrEqual(390);
      expect(worldSize(r.grid).h * r.k, `390x${h}`).toBeLessThanOrEqual(h);
    }
    // nothing fits: the narrowest plan
    expect(fitGrid(100, 100, 1, 9).grid.columns).toBe(3);
  });

  it("never goes below one device pixel", () => {
    expect(fitGrid(50, 50, 1, 20).k).toBe(1);
  });
});
