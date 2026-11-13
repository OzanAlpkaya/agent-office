// QA rounds on the board (Phase 19): each QA run of a shown goal is a card of
// its own, with its result, its bug sub-tasks and its cost. Pure, so Vitest
// covers it.
import type { QARun, Task, TaskStatus } from "./api";
import type { Tone } from "./cards";

export interface QACard {
  run: QARun;
  round: number; // 1 for the goal's first QA run
  goal: Task;
  column: TaskStatus; // in_progress while running; review when it ended; done with its goal
  label: string; // the chip
  tone: Tone;
  bugTasks: Task[]; // the sub-tasks its bugs became
}

const chip = (r: QARun): { label: string; tone: Tone } => {
  switch (r.status) {
    case "running":
      return { label: "QA running", tone: "accent" };
    case "passed":
      return { label: "Passed", tone: "ok" };
    case "failed":
      return { label: `Failed · ${r.bugs.length} bug${r.bugs.length === 1 ? "" : "s"}`, tone: "danger" };
    default:
      return { label: "No result", tone: "danger" };
  }
};

// qaCards lists the QA rounds of the goals in shown, oldest round first per goal.
export function qaCards(runs: readonly QARun[], tasks: readonly Task[], shown: readonly Task[]): QACard[] {
  const goals = new Map(shown.filter((t) => !t.parent_task_id).map((g) => [g.id, g]));
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const out: QACard[] = [];
  const rounds = new Map<string, number>();
  for (const r of runs.slice().sort((a, b) => a.id - b.id)) {
    const goal = goals.get(r.goal_id);
    if (!goal) continue;
    const round = (rounds.get(goal.id) ?? 0) + 1;
    rounds.set(goal.id, round);
    const column: TaskStatus = goal.status === "done" ? "done" : r.status === "running" ? "in_progress" : "review";
    const bugTasks = r.bugs.map((b) => (b.bug_task_id ? byId.get(b.bug_task_id) : undefined)).filter((t): t is Task => !!t);
    out.push({ run: r, round, goal, column, ...chip(r), bugTasks });
  }
  return out;
}

// openBugs counts a round's bug sub-tasks that are not reported or done.
export function openBugs(c: QACard): number {
  return c.bugTasks.filter((t) => t.status !== "review" && t.status !== "done").length;
}
