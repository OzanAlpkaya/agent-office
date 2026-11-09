import { describe, expect, it } from "vitest";
import type { TaskStatus } from "../api";
import { type Capacity, Director, type Observed, atDesk, timing } from "./errands";
import type { Pose } from "./pose";

const cap: Capacity = { whiteboard: 3, coffee: 3, review: 2, queue: 3 };
const seat = (id: string, pose: Pose, taskStatus?: TaskStatus, phase = 0): Observed => ({ id, present: true, pose, taskStatus, phase });
const gone = (id: string, taskStatus?: TaskStatus): Observed => ({ id, present: false, taskStatus, phase: 0 });

const coffeeAt = timing.coffeeAfter; // phase 0

describe("Director", () => {
  it("keeps everyone at their desk on the first view, and replays nothing", () => {
    const d = new Director();
    d.observe([seat("a", "typing", "review"), seat("b", "idle", "done"), seat("c", "booting")], 0);
    const e = d.errands(0, cap);
    expect([...e.values()]).toEqual([atDesk, atDesk, atDesk]);
  });

  it("sends a reviewer who is reviewing to the review room, and back when it stops", () => {
    const d = new Director();
    d.observe([seat("r", "reviewing")], 0);
    expect(d.errands(0, cap).get("r")).toEqual({ place: "review", spot: 0 });
    d.observe([seat("r", "typing")], 1);
    expect(d.errands(1, cap).get("r")).toEqual(atDesk);
  });

  it("sends a reporting agent to the whiteboard for a while", () => {
    const d = new Director();
    d.observe([seat("w", "typing", "in_progress")], 0);
    d.observe([seat("w", "typing", "review")], 10);
    expect(d.errands(10, cap).get("w")).toEqual({ place: "whiteboard", spot: 0 });
    expect(d.errands(10 + timing.report - 0.1, cap).get("w")).toEqual({ place: "whiteboard", spot: 0 });
    expect(d.errands(10 + timing.report, cap).get("w")).toEqual(atDesk);
  });

  it("only a change into review or done is a report", () => {
    const d = new Director();
    d.observe([seat("w", "typing", "review")], 0);
    d.observe([seat("w", "typing", "done")], 1); // review → done is the same report
    expect(d.errands(1, cap).get("w")).toEqual(atDesk);
    d.observe([seat("w", "typing", "in_progress")], 2);
    d.observe([seat("w", "typing", "done")], 3);
    expect(d.errands(3, cap).get("w")?.place).toBe("whiteboard");
  });

  it("an agent that exits right after reporting finishes the report, then leaves", () => {
    const d = new Director();
    d.observe([seat("w", "typing", "in_progress")], 0);
    // the report and the exit can arrive in the same update
    d.observe([gone("w", "review")], 5);
    expect(d.errands(5, cap).get("w")).toEqual({ place: "whiteboard", spot: 0 });
    expect(d.errands(5 + timing.report, cap).has("w")).toBe(false);
    // and stays gone
    d.observe([gone("w", "review")], 20);
    expect(d.errands(20, cap).has("w")).toBe(false);
  });

  it("an agent that exits without a report is simply not listed", () => {
    const d = new Director();
    d.observe([seat("w", "typing")], 0);
    d.observe([gone("w")], 1);
    expect(d.errands(1, cap).has("w")).toBe(false);
  });

  it("the user comes first: waiting beats a report and goes to the user's door; a fresh start stays at the desk", () => {
    const d = new Director();
    d.observe([seat("w", "typing", "in_progress"), seat("n", "booting", "in_progress")], 0);
    d.observe([seat("w", "waiting", "review"), seat("n", "booting", "review")], 1);
    expect(d.errands(1, cap).get("w")).toEqual({ place: "queue", spot: 0 });
    expect(d.errands(1, cap).get("n")).toEqual(atDesk);
    // once the question is answered, the report window still runs
    d.observe([seat("w", "typing", "review"), seat("n", "booting", "review")], 2);
    expect(d.errands(2, cap).get("w")?.place).toBe("whiteboard");
  });

  it("an idle agent takes one coffee, then sits down for good", () => {
    const d = new Director();
    d.observe([seat("i", "idle")], 0);
    expect(d.errands(coffeeAt - 0.1, cap).get("i")).toEqual(atDesk);
    expect(d.errands(coffeeAt, cap).get("i")).toEqual({ place: "coffee", spot: 0 });
    expect(d.errands(coffeeAt + timing.coffee - 0.1, cap).get("i")?.place).toBe("coffee");
    expect(d.errands(coffeeAt + timing.coffee, cap).get("i")).toEqual(atDesk);
    expect(d.errands(coffeeAt + 1000, cap).get("i")).toEqual(atDesk);
  });

  it("work interrupts a coffee, and a new idle spell earns a new one", () => {
    const d = new Director();
    d.observe([seat("i", "idle")], 0);
    expect(d.errands(coffeeAt + 1, cap).get("i")?.place).toBe("coffee");
    d.observe([seat("i", "typing")], coffeeAt + 2);
    expect(d.errands(coffeeAt + 2, cap).get("i")).toEqual(atDesk);
    d.observe([seat("i", "idle")], 100);
    expect(d.errands(100 + coffeeAt, cap).get("i")?.place).toBe("coffee");
  });

  it("spreads coffee breaks out by phase", () => {
    const d = new Director();
    d.observe([seat("a", "idle", undefined, 0), seat("b", "idle", undefined, 1)], 0);
    const e = d.errands(coffeeAt + 0.5, cap);
    expect(e.get("a")?.place).toBe("coffee");
    expect(e.get("b")).toEqual(atDesk);
    expect(d.errands(coffeeAt + timing.coffeeSpread, cap).get("b")?.place).toBe("coffee");
  });

  it("hands out spots once and keeps them; without a free spot the errand is skipped", () => {
    const d = new Director();
    const ids = ["a", "b", "c", "d"];
    d.observe(ids.map((id) => seat(id, "typing", "in_progress")), 0);
    d.observe([seat("c", "typing", "review")], 1);
    d.observe(ids.map((id) => seat(id, "typing", id === "c" ? "review" : "in_progress")), 1);
    d.observe(ids.map((id) => seat(id, "typing", "review")), 2); // a, b, d report a second later
    const e = d.errands(2, cap);
    expect(e.get("c")).toEqual({ place: "whiteboard", spot: 0 });
    const spots = ["a", "b", "d"].map((id) => e.get(id));
    expect(spots.filter((s) => s?.place === "whiteboard").map((s) => s?.spot).sort()).toEqual([1, 2]);
    expect(spots.filter((s) => s?.place === "desk")).toHaveLength(1);
    // c keeps its spot on every later call
    expect(d.errands(3, cap).get("c")).toEqual({ place: "whiteboard", spot: 0 });
  });

  it("two reviewers fill two seats; a third works at its desk", () => {
    const d = new Director();
    d.observe([seat("r1", "reviewing"), seat("r2", "reviewing"), seat("r3", "reviewing")], 0);
    const e = d.errands(0, cap);
    expect([e.get("r1"), e.get("r2"), e.get("r3")]).toEqual([
      { place: "review", spot: 0 }, { place: "review", spot: 1 }, atDesk,
    ]);
  });

  it("forgets agents the world no longer lists", () => {
    const d = new Director();
    d.observe([seat("a", "typing")], 0);
    d.observe([], 1);
    expect(d.errands(1, cap).size).toBe(0);
  });

  it("queues agents waiting on the user in the order they started waiting, and the queue moves up", () => {
    const d = new Director();
    d.observe([seat("b", "waiting"), seat("a", "typing"), seat("c", "typing")], 0); // already waiting: in the queue at once
    d.observe([seat("b", "waiting"), seat("a", "waiting"), seat("c", "typing")], 5);
    d.observe([seat("b", "waiting"), seat("a", "waiting"), seat("c", "waiting")], 6);
    const e = d.errands(6, cap);
    expect([e.get("b"), e.get("a"), e.get("c")]).toEqual([
      { place: "queue", spot: 0 }, { place: "queue", spot: 1 }, { place: "queue", spot: 2 },
    ]);
    // b is answered: the others step forward
    d.observe([seat("b", "typing"), seat("a", "waiting"), seat("c", "waiting")], 7);
    const f = d.errands(7, cap);
    expect([f.get("b"), f.get("a"), f.get("c")]).toEqual([atDesk, { place: "queue", spot: 0 }, { place: "queue", spot: 1 }]);
  });

  it("a queue longer than the line waits at the desk", () => {
    const d = new Director();
    d.observe(["a", "b", "c", "d"].map((id) => seat(id, "waiting")), 0);
    const e = d.errands(0, cap);
    expect([...e.values()].filter((x) => x.place === "queue")).toHaveLength(3);
    expect(e.get("d")).toEqual(atDesk);
  });
});
