import { describe, expect, it } from "vitest";
import type { QARun, Task } from "./api";
import { openBugs, qaCards } from "./qaCards";

const task = (id: string, status: Task["status"], parent?: string): Task => ({
  id, title: id, description: "", status, parent_task_id: parent, created_at: "", updated_at: "",
});
const run = (id: number, goal: string, status: QARun["status"], bugs: QARun["bugs"] = []): QARun => ({
  id, goal_id: goal, status, branch: "agent-office/qa", merged: [], bugs, created_at: "", updated_at: "",
});

describe("qaCards", () => {
  const g = task("g", "in_progress");
  const b1 = task("b1", "todo", "g");
  const b2 = task("b2", "review", "g");
  const tasks = [g, b1, b2, task("other", "in_progress")];
  it("numbers each goal's rounds and puts them in a column by their state", () => {
    const cards = qaCards([run(3, "g", "running"), run(1, "g", "failed", [
      { title: "crash", description: "", bug_task_id: "b1" }, { title: "typo", description: "", bug_task_id: "b2" },
      { title: "not filed", description: "" }]), run(2, "other", "passed")], tasks, [g, b1, b2]);
    expect(cards.map((c) => [c.run.id, c.round, c.column, c.label])).toEqual([
      [1, 1, "review", "Failed · 3 bugs"], [3, 2, "in_progress", "QA running"],
    ]);
    expect(cards[0].bugTasks.map((t) => t.id)).toEqual(["b1", "b2"]);
    expect(openBugs(cards[0])).toBe(1);
  });
  it("moves a done goal's rounds to Done", () => {
    const done = { ...g, status: "done" as const };
    expect(qaCards([run(1, "g", "passed")], [done], [done])[0]).toMatchObject({ column: "done", label: "Passed", tone: "ok" });
  });
});
