import { describe, expect, it } from "vitest";
import type { QARun, Task, TaskStatus } from "./api";
import { boardTasks } from "./goals";
import { qaCards } from "./qaCards";
import { boardSummary, summaryText } from "./summary";

const at = "2026-11-13T10:00:00Z";
const task = (id: string, status: TaskStatus, parent?: string): Task =>
  ({ id, title: id, description: "", status, created_at: at, updated_at: at, ...(parent ? { parent_task_id: parent } : {}) }) as Task;
const run = (id: number, goal: string, status: QARun["status"]): QARun =>
  ({ id, goal_id: goal, status, branch: "b", merged: [], bugs: [], created_at: at, updated_at: at }) as QARun;

describe("boardSummary", () => {
  const tasks = [
    task("g1", "in_progress"), task("a", "todo", "g1"), task("b", "in_progress", "g1"), task("c", "review", "g1"),
    task("d", "done", "g1"), task("e", "blocked", "g1"),
    // a done goal and its sub-tasks are in the archive, not on the board
    task("g2", "done"), task("f", "done", "g2"), task("h", "todo", "g2"),
    task("solo", "todo"),
  ];
  const runs = [run(1, "g1", "failed"), run(2, "g1", "running"), run(3, "g2", "passed")];

  it("counts what the Board's columns show for all active goals, QA rounds included", () => {
    expect(boardSummary(tasks, runs)).toEqual({ todo: 2, in_progress: 3, review: 2, done: 1, blocked: 1 });
    // the same as the Board's own computation
    const shown = boardTasks(tasks, "all");
    const cards = qaCards(runs, tasks, shown);
    for (const s of ["todo", "in_progress", "review", "done", "blocked"] as const) {
      expect(boardSummary(tasks, runs)[s]).toBe(shown.filter((t) => t.status === s).length + cards.filter((c) => c.column === s).length);
    }
  });

  it("is all zeros for an empty board", () => {
    expect(boardSummary([])).toEqual({ todo: 0, in_progress: 0, review: 0, done: 0, blocked: 0 });
  });

  it("reads as words", () => {
    expect(summaryText(boardSummary(tasks, runs))).toBe("To do: 2 · In progress: 3 · Review: 2 · Done: 1 · Blocked: 1");
  });
});
