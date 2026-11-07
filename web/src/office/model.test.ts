import { describe, expect, it } from "vitest";
import type { AccessRequest } from "../api";
import { emptyAssignment } from "./layout";
import { buildOffice, freshMessages, maxNotes } from "./model";
import { agent, message, task } from "./testutil";

const pending = (agentId: string, status: AccessRequest["status"] = "pending"): AccessRequest => ({
  id: 1, agent_id: agentId, kind: "domain", value: "example.com", reason: "", status, created_at: "2026-11-08T10:00:00Z",
});

describe("buildOffice", () => {
  it("seats every agent with its pose and task title", () => {
    const m = buildOffice(emptyAssignment, [
      agent("L", { role: "lead", task_id: "g", state: "idle" }),
      agent("w", { task_id: "t1", started_at: "2026-11-08T10:01:00Z" }),
      agent("r", { role: "reviewer", started_at: "2026-11-08T10:02:00Z" }),
      agent("x", { state: "exited", started_at: "2026-11-08T10:03:00Z" }),
    ], [task("g", { title: "Goal" }), task("t1", { title: "Fix it", parent_task_id: "g" })], []);
    expect(m.seats.map((s) => [s.agent.id, s.slot.kind, s.pose, s.taskTitle])).toEqual([
      ["L", "lead", "idle", "Goal"],
      ["w", "desk", "typing", "Fix it"],
      ["r", "desk", "reviewing", undefined],
      ["x", "desk", "away", undefined],
    ]);
    expect(m.desks).toBe(3);
  });

  it("shows a pending access request as waiting", () => {
    const m = buildOffice(emptyAssignment, [agent("a"), agent("b")], [], [pending("a"), pending("b", "denied")]);
    expect(m.seats.map((s) => s.pose)).toEqual(["waiting", "typing"]);
  });

  it("puts the lead's sub-tasks on the whiteboard in creation order", () => {
    const sub = (id: string, min: number, status: "todo" | "done") =>
      task(id, { parent_task_id: "g", status, created_at: `2026-11-08T10:0${min}:00Z` });
    const m = buildOffice(emptyAssignment, [agent("L", { role: "lead", task_id: "g" })],
      [task("g"), sub("b", 2, "done"), sub("a", 1, "todo"), task("other")], []);
    expect(m.goal?.notes.map((n) => [n.id, n.status])).toEqual([["a", "todo"], ["b", "done"]]);
  });

  it("has room for every sub-task a lead may create", () => {
    expect(maxNotes).toBe(20);
  });

  it("caps the whiteboard", () => {
    const subs = Array.from({ length: maxNotes + 5 }, (_, i) => task(`s${i}`, { parent_task_id: "g" }));
    const m = buildOffice(emptyAssignment, [agent("L", { role: "lead", task_id: "g" })], [task("g"), ...subs], []);
    expect(m.goal?.notes).toHaveLength(maxNotes);
  });

  it("has no whiteboard without a lead", () => {
    expect(buildOffice(emptyAssignment, [agent("w", { task_id: "g" })], [task("g")], []).goal).toBeUndefined();
  });
});

describe("freshMessages", () => {
  it("returns each new message once, only if created since the office opened", () => {
    const seen = new Set<number>();
    const old = message(1, "a", "b", "2026-11-08T09:59:00Z");
    const m2 = message(2, "a", "b", "2026-11-08T10:00:05Z");
    const since = Date.parse("2026-11-08T10:00:00Z");
    // the same message is listed for its sender and its recipient
    expect(freshMessages(seen, { a: [m2, old], b: [m2, old] }, since)).toEqual([m2]);
    expect(freshMessages(seen, { a: [m2, old] }, since)).toEqual([]);
    const m3 = message(3, undefined, "a", "2026-11-08T10:00:09Z");
    expect(freshMessages(seen, { a: [m3, m2] }, since)).toEqual([m3]);
  });
});
