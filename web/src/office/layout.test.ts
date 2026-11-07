import { describe, expect, it } from "vitest";
import { type Assignment, assignDesks, cell, columns, emptyAssignment, inCell, rowsFor, slotOrigin, world, worldSize } from "./layout";
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
    const a = assignDesks(emptyAssignment, [agent("w1", { started_at: at(2) }), agent("L", { role: "lead", started_at: at(1) })]);
    expect(a).toEqual({ lead: "L", desks: ["w1"] });
  });

  it("moves an exited lead to a regular desk when a new lead starts", () => {
    const first = assignDesks(emptyAssignment, [agent("L1", { role: "lead", started_at: at(1) }), agent("w", { started_at: at(2) })]);
    const exited = assignDesks(first, [agent("L1", { role: "lead", state: "exited" }), agent("w")]);
    expect(exited.lead).toBe("L1"); // stays while no other lead runs
    const second = assignDesks(exited, [
      agent("L1", { role: "lead", state: "exited", started_at: at(1) }), agent("w", { started_at: at(2) }),
      agent("L2", { role: "lead", started_at: at(5) }),
    ]);
    expect(second).toEqual({ lead: "L2", desks: ["w", "L1"] });
  });

  it("is idempotent", () => {
    const agents = [
      agent("L1", { role: "lead", state: "exited", started_at: at(1) }), agent("w1", { started_at: at(2) }),
      agent("L2", { role: "lead", started_at: at(5) }), agent("r", { role: "reviewer", started_at: at(6) }),
    ];
    const once = assignDesks({ lead: "L1", desks: ["w1"] }, agents);
    expect(assignDesks(once, agents)).toEqual(once);
  });

  it("gives every agent exactly one seat", () => {
    const agents = Array.from({ length: 23 }, (_, i) => agent(`a${i}`, { role: i === 4 ? "lead" : "worker", started_at: at(i) }));
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
  it("has at least two rows and grows by rows of four", () => {
    expect(rowsFor({ desks: [] })).toBe(2);
    expect(rowsFor({ desks: Array(8).fill("x") })).toBe(2);
    expect(rowsFor({ desks: Array(9).fill("x") })).toBe(3);
    expect(worldSize(3).h - worldSize(2).h).toBe(cell.h);
  });

  it("places desks inside the world without overlapping", () => {
    const rows = 3;
    const size = worldSize(rows);
    const cells = [slotOrigin({ kind: "lead" }), ...Array.from({ length: rows * columns }, (_, index) => slotOrigin({ kind: "desk", index }))];
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
    expect(slotOrigin({ kind: "lead" }).y).toBeGreaterThanOrEqual(world.leadRowY);
  });

  it("keeps the nameplate on the desk", () => {
    expect(inCell.nameplate.y).toBeGreaterThan(inCell.desk.y);
  });
});
