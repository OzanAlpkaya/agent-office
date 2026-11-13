// The board summary (Phase 20): how many cards each Board column has for
// "All active goals", for the office bar and the lead's whiteboard. The same
// cards as the Board: active goals and their sub-tasks, plus QA round cards.
// Pure.
import { type QARun, type Task, type TaskStatus, taskStatuses } from "./api";
import { boardTasks } from "./goals";
import { qaCards } from "./qaCards";

export type BoardSummary = Record<TaskStatus, number>;

// columnTitle names the Board's columns.
export const columnTitle: Record<TaskStatus, string> = {
  todo: "To do",
  in_progress: "In progress",
  review: "Review",
  done: "Done",
  blocked: "Blocked",
};

export function boardSummary(tasks: readonly Task[], qaRuns: readonly QARun[] = []): BoardSummary {
  const shown = boardTasks(tasks, "all");
  const out = Object.fromEntries(taskStatuses.map((s) => [s, 0])) as BoardSummary;
  for (const t of shown) out[t.status]++;
  for (const c of qaCards(qaRuns, tasks, shown)) out[c.column]++;
  return out;
}

// summaryText is the summary in words, e.g. for a tooltip.
export function summaryText(s: BoardSummary): string {
  return taskStatuses.map((k) => `${columnTitle[k]}: ${s[k]}`).join(" · ");
}
