import { describe, expect, it } from "vitest";
import type { InboxItem, Plan, QARun, Spec, Task } from "./api";
import {
  currentSpec, fromForm, inboxFor, latestQA, needsAnswer, newSubTask, specsOf, splitList, toForm, unread, waitsFor,
} from "./workflow";

const at = "2026-11-08T10:00:00Z";
const task = (id: string, over: Partial<Task> = {}): Task =>
  ({ id, title: `T ${id}`, description: "", status: "todo", created_at: at, updated_at: at, ...over });
const item = (id: number, over: Partial<InboxItem> = {}): InboxItem =>
  ({ id, kind: "question", body: "?", created_at: at, agent_id: "a1", ...over });
const plan: Plan = {
  summary: "S", design: "D", risks: "R",
  sub_tasks: [
    { key: "a", title: "A", description: "do a", files: ["x.go", "dir/"], acceptance: ["one", "two"] },
    { key: "b", title: "B", description: "", depends_on: ["a"], role: "docs" },
  ],
};
const spec = (id: number, version: number, status: Spec["status"], goal = "g"): Spec =>
  ({ id, goal_id: goal, version, status, plan, markdown: "# x", created_at: at });

describe("inbox", () => {
  it("counts unread and finds questions", () => {
    const items = [item(1), item(2, { read_at: at }), item(3, { kind: "info" }), item(4, { agent_id: undefined, kind: "plan" })];
    expect(unread(items)).toBe(3);
    expect(items.filter(needsAnswer).map((i) => i.id)).toEqual([1, 2]);
    expect(needsAnswer(item(5, { answered_at: at }))).toBe(false);
  });
  it("finds a task's and a goal's items", () => {
    const items = [item(1, { task_id: "s1", goal_id: "g" }), item(2, { task_id: "g", goal_id: "g" }), item(3, { task_id: "x" })];
    expect(inboxFor(items, task("g")).map((i) => i.id)).toEqual([1, 2]);
    expect(inboxFor(items, task("s1", { parent_task_id: "g" })).map((i) => i.id)).toEqual([1]);
  });
});

describe("plans", () => {
  it("shows a proposal before the approved version", () => {
    const specs = [spec(1, 1, "superseded"), spec(2, 2, "approved"), spec(3, 3, "proposed"), spec(4, 1, "proposed", "h")];
    expect(specsOf(specs, "g").map((s) => s.version)).toEqual([3, 2, 1]);
    expect(currentSpec(specs, "g")?.id).toBe(3);
    expect(currentSpec(specs.slice(0, 2), "g")?.id).toBe(2);
    expect(currentSpec(specs, "nope")).toBeUndefined();
  });
  it("round-trips through the editor form", () => {
    const f = toForm(plan);
    expect(f.subTasks[0].files).toBe("x.go, dir/");
    expect(f.subTasks[0].acceptance).toBe("one\ntwo");
    expect(fromForm(f)).toEqual(plan);
    f.subTasks[1].dependsOn = " a , ,";
    f.subTasks[1].acceptance = "line, with comma\n\n";
    f.risks = " ";
    const p = fromForm(f);
    expect(p.sub_tasks[1].depends_on).toEqual(["a"]);
    expect(p.sub_tasks[1].acceptance).toEqual(["line, with comma"]);
    expect(p.risks).toBeUndefined();
  });
  it("adds sub-tasks with fresh keys", () => {
    const f = toForm(plan);
    f.subTasks.push({ ...newSubTask(f) });
    expect(f.subTasks[2].key).toBe("task-3");
    f.subTasks[2].key = "task-4";
    expect(newSubTask(f).key).toBe("task-5");
  });
  it("splits lists", () => {
    expect(splitList("a, b\nc,,")).toEqual(["a", "b", "c"]);
    expect(splitList("a, b\nc", true)).toEqual(["a, b", "c"]);
  });
});

describe("dependencies and QA", () => {
  it("lists unreported dependencies", () => {
    const tasks = [task("a", { status: "review" }), task("b", { status: "in_progress" }), task("c", { depends_on: ["a", "b", "gone"] })];
    expect(waitsFor(tasks[2], tasks).map((t) => t.id)).toEqual(["b"]);
    expect(waitsFor(task("d"), tasks)).toEqual([]);
  });
  it("finds the newest run of a goal", () => {
    const run = (id: number, goal: string): QARun =>
      ({ id, goal_id: goal, status: "passed", branch: "b", merged: [], bugs: [], created_at: at, updated_at: at });
    expect(latestQA([run(1, "g"), run(3, "h"), run(2, "g")], "g")?.id).toBe(2);
  });
});
