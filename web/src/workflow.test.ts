import { describe, expect, it } from "vitest";
import type { InboxItem, Plan, QARun, Spec, Task } from "./api";
import {
  currentSpec, fromForm, goalBusy, groupInbox, inboxFor, isOpen, latestQA, needsAnswer, newSubTask, proposedSpec, specsOf, splitList, toForm, unread,
  waitsFor, withSubTask, withoutSubTask, subTaskForm, fromSubTaskForm, pendingChanges, withdrawn,
} from "./workflow";
import { agent } from "./office/testutil";

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
    expect(proposedSpec(specs, "g")?.id).toBe(3);
    expect(proposedSpec(specs.slice(0, 2), "g")).toBeUndefined(); // only an approved version: nothing to approve
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

describe("goalBusy", () => {
  const goal = (id: string, title: string): Task => ({ id, title, description: "", status: "todo", created_at: "", updated_at: "" });
  const a = goal("g1", "Add tags"), b = goal("g2", "Add search");
  it("names the goal whose Tech Lead runs, and how to start this one", () => {
    const lead = agent("l1", { kind: "lead", role: "tech-lead", state: "idle", task_id: "g1" });
    const busy = goalBusy(b, [lead], [a, b]);
    expect(busy?.agentId).toBe("l1");
    expect(busy?.text).toContain("One goal at a time in this project: the Tech Lead of “Add tags” is still running");
    expect(busy?.text).toContain("mark that goal done or stop its Tech Lead");
    expect(goalBusy(a, [lead], [a, b])).toBeUndefined(); // its own lead: the card shows the agent
  });
  it("names a running architect too, and nothing once they exited or for sub-tasks", () => {
    const arch = agent("r1", { kind: "architect", role: "architect", state: "working", task_id: "g1" });
    expect(goalBusy(b, [arch], [a, b])?.text).toContain("the architect of “Add tags”");
    expect(goalBusy(b, [{ ...arch, state: "exited" }], [a, b])).toBeUndefined();
    expect(goalBusy({ ...b, parent_task_id: "g1" }, [arch], [a, b])).toBeUndefined();
  });
});

describe("inbox groups (Phase 16)", () => {
  const t = (m: number): string => `2026-11-08T10:${String(m).padStart(2, "0")}:00Z`;
  const items: InboxItem[] = [
    item(1, { goal_id: "g1", agent_id: "lead", created_at: t(1), read_at: t(2), answered_at: t(3), reply: "yes" }),
    item(2, { goal_id: "g1", agent_id: "lead", created_at: t(4) }), // open question
    item(3, { goal_id: "g1", agent_id: "", kind: "info", created_at: t(5), read_at: t(6) }), // agent-office, read
    item(4, { goal_id: "g2", agent_id: "arch", kind: "info", created_at: t(9), read_at: t(9) }),
    item(5, { agent_id: "asst", kind: "briefing", created_at: t(7) }),
    item(6, { agent_id: "asst", kind: "briefing", created_at: t(8), read_at: t(8) }),
    item(7, { kind: "info", agent_id: "", created_at: t(2) }), // a restart notice: no goal
  ];

  it("groups by goal, then agent, open groups first, answered and read items folded", () => {
    const v = groupInbox(items);
    expect(v.groups.map((g) => [g.goalId, g.open])).toEqual([["g1", 1], ["", 1], ["g2", 0]]);
    const g1 = v.groups[0];
    expect(g1.agents.map((a) => [a.agentId, a.open.map((i) => i.id), a.earlier.map((i) => i.id)]))
      .toEqual([["lead", [2], [1]], ["", [], [3]]]);
  });

  it("keeps briefings apart, newest first", () => {
    expect(groupInbox(items).briefings.map((i) => i.id)).toEqual([6, 5]);
    expect(groupInbox(items).groups.flatMap((g) => g.agents.flatMap((a) => [...a.open, ...a.earlier])).some((i) => i.kind === "briefing"))
      .toBe(false);
  });

  it("counts unread and unanswered items as open", () => {
    expect(isOpen(item(1, { read_at: at }))).toBe(true); // a question without an answer
    expect(isOpen(item(1, { kind: "info", read_at: at }))).toBe(false);
    expect(isOpen(item(1, { kind: "info" }))).toBe(true);
  });
});

describe("light plan edits (Phase 19)", () => {
  const plan: Plan = {
    summary: "s", design: "d", sub_tasks: [
      { key: "schema", title: "Schema", description: "" },
      { key: "api", title: "API", description: "", depends_on: ["schema"] },
    ],
  };
  it("replaces one sub-task and renames its key in dependencies", () => {
    const p = withSubTask(plan, 0, { key: "db", title: "DB schema", description: "x" });
    expect(p.sub_tasks.map((s) => s.key)).toEqual(["db", "api"]);
    expect(p.sub_tasks[1].depends_on).toEqual(["db"]);
    expect(withSubTask(plan, -1, { key: "cli", title: "CLI", description: "" }).sub_tasks).toHaveLength(3);
    expect(plan.sub_tasks[0].key).toBe("schema"); // the original is not changed
  });
  it("removes a sub-task and the dependencies on it", () => {
    const p = withoutSubTask(plan, "schema");
    expect(p.sub_tasks).toEqual([{ key: "api", title: "API", description: "" }]);
  });
  it("round-trips one sub-task through the inline editor", () => {
    const s = { key: "api", title: "API", description: "d", files: ["a.go"], acceptance: ["one", "two"], depends_on: ["schema"] };
    expect(fromSubTaskForm(subTaskForm(s))).toEqual(s);
  });
  it("shows a change request until a newer version answers it", () => {
    const v1 = { id: 1, goal_id: "g", version: 1, status: "proposed", plan, markdown: "", created_at: "", change_request: "split" } as Spec;
    expect(pendingChanges(v1, [v1])).toBe("split");
    const v2 = { ...v1, id: 2, version: 2, change_request: undefined };
    expect(pendingChanges(v1, [v1, v2])).toBeUndefined();
    // Phase 20: a request withdraws a proposed version; it still waits for the revision.
    const gone = { ...v1, status: "superseded" } as Spec;
    expect(pendingChanges(gone, [gone])).toBe("split");
    expect(pendingChanges(gone, [gone, v2])).toBeUndefined();
  });
  it("shows a withdrawn version until the revision, and never as waiting for approval (Phase 20)", () => {
    const approved = { id: 1, goal_id: "g", version: 1, status: "approved", plan, markdown: "", created_at: "" } as Spec;
    const gone = { ...approved, id: 2, version: 2, status: "superseded", change_request: "drop docs" } as Spec;
    expect(withdrawn(gone)).toBe(true);
    expect(withdrawn({ ...gone, change_request: undefined })).toBe(false);
    expect(currentSpec([approved, gone], "g")?.id).toBe(2);
    expect(proposedSpec([approved, gone], "g")).toBeUndefined();
    const v3 = { ...approved, id: 3, version: 3, status: "proposed" } as Spec;
    expect(currentSpec([approved, gone, v3], "g")?.id).toBe(3);
    // an old superseded version without a request is not current
    expect(currentSpec([{ ...approved, status: "superseded" }, { ...approved, id: 4, version: 2 }], "g")?.id).toBe(4);
  });
});
