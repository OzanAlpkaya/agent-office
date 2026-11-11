import { describe, expect, it } from "vitest";
import type { Agent, GoalMerge, QARun, Task } from "./api";
import {
  activeGoals, archivedGoals, boardTasks, busyOf, closeWarning, mergeOffered, pushOffered, validPick,
} from "./goals";
import { mergeById } from "./useAgents";

const task = (id: string, status: Task["status"], parent?: string, updated = "2026-11-10T10:00:00Z"): Task => ({
  id, title: id, description: "", status, parent_task_id: parent, created_at: "2026-11-01T10:00:00Z", updated_at: updated,
});
const agent = (id: string, taskId: string, state: Agent["state"], kind: Agent["kind"] = "worker"): Agent => ({
  id, task_id: taskId, state, kind, role: kind, sandbox: "podman", branch: "b", worktree_path: "/w", has_terminal: true,
  started_at: "2026-11-01T10:00:00Z",
} as Agent);
const qa = (goal: string, status: QARun["status"], head = "h1", id = 1): QARun => ({
  id, goal_id: goal, status, branch: "agent-office/qa", head, merged: [], bugs: [], created_at: "", updated_at: "",
});
const merge = (goal: string, source: string, id = 1, pushed?: string): GoalMerge => ({
  id, goal_id: goal, target: "main", source_head: source, target_before: "t0", commit: "c1", message: "m",
  created_at: "", pushed_at: pushed,
});

const tasks = [
  task("g1", "in_progress"), task("s1", "review", "g1"), task("s2", "done", "g1"),
  task("g2", "done", undefined, "2026-11-09T10:00:00Z"), task("s3", "done", "g2"),
  task("g3", "done", undefined, "2026-11-11T10:00:00Z"),
  task("g4", "review"), task("orphan", "todo", "gone"),
];

describe("goal view", () => {
  it("shows active goals and their sub-tasks by default", () => {
    expect(boardTasks(tasks, "all").map((t) => t.id)).toEqual(["g1", "s1", "s2", "g4", "orphan"]);
    expect(activeGoals(tasks).map((t) => t.id)).toEqual(["g1", "g4"]);
  });
  it("narrows to one goal, and falls back when it is done or gone", () => {
    expect(boardTasks(tasks, "g1").map((t) => t.id)).toEqual(["g1", "s1", "s2"]);
    expect(validPick(tasks, "g2")).toBe("all");
    expect(validPick(tasks, "nope")).toBe("all");
    expect(boardTasks(tasks, "g2").map((t) => t.id)).toEqual(boardTasks(tasks, "all").map((t) => t.id));
  });
  it("archives done goals, most recently closed first", () => {
    expect(archivedGoals(tasks).map((t) => t.id)).toEqual(["g3", "g2"]);
  });
});

describe("closing a goal", () => {
  const agents = [agent("lead", "g1", "idle", "lead"), agent("w1", "s1", "exited"), agent("w2", "s2", "working"),
    agent("rev", "s1", "working", "reviewer"), agent("other", "g4", "exited")];
  it("counts the busy agents marking done would stop", () => {
    expect(busyOf(tasks[0], agents, tasks)).toBe(1); // w2; the idle lead is expected, a reviewer finishes its review
    expect(busyOf(tasks[6], agents, tasks)).toBe(0);
  });
  it("warns only when agents run", () => {
    expect(closeWarning(0)).toBe("");
    expect(closeWarning(1)).toContain("1 agent of this goal is still working");
    expect(closeWarning(2)).toContain("2 agents of this goal are");
  });
});

describe("merge", () => {
  const g = task("g4", "review");
  it("is offered once QA passed on a reported goal, until that work is merged", () => {
    expect(mergeOffered(g, [qa("g4", "passed")], [])).toBe(true);
    expect(mergeOffered(task("g4", "in_progress"), [qa("g4", "passed")], [])).toBe(false);
    expect(mergeOffered(g, [qa("g4", "failed")], [])).toBe(false);
    expect(mergeOffered(g, [qa("g4", "passed", "h1", 1), qa("g4", "failed", "h2", 2)], [])).toBe(false);
    expect(mergeOffered(g, [qa("g4", "passed")], [merge("g4", "h1")])).toBe(false);
    expect(mergeOffered(g, [qa("g4", "passed", "h2")], [merge("g4", "h1")])).toBe(true); // newer tested work
    expect(mergeOffered(task("s1", "review", "g4"), [qa("s1", "passed")], [])).toBe(false);
  });
  it("offers Push until the latest merge is pushed", () => {
    expect(pushOffered([], "g4")).toBe(false);
    expect(pushOffered([merge("g4", "h1")], "g4")).toBe(true);
    expect(pushOffered([merge("g4", "h1", 1, "2026-11-11T10:00:00Z")], "g4")).toBe(false);
  });
  it("keeps the newest record per merge", () => {
    const pushed = merge("g4", "h1", 1, "2026-11-11T10:00:00Z");
    expect(mergeById([merge("g4", "h1", 1), merge("g1", "x", 2)], [pushed]).map((m) => [m.id, m.pushed_at])).toEqual([
      [2, undefined], [1, "2026-11-11T10:00:00Z"]]);
  });
});
