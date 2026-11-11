// Pure helpers for goals after review (Phase 17, ADR 0054, 0055): which goals
// the board shows, the archive, closing a goal and its merge. No React here,
// so Vitest covers them.
import type { Agent, GoalMerge, QARun, Task } from "./api";
import { latestQA } from "./workflow";

// "all" shows every active goal; otherwise a goal id.
export type GoalPick = "all" | string;

export const isGoal = (t: Task): boolean => !t.parent_task_id;

// activeGoals are the goals that are not done, in creation order.
export function activeGoals(tasks: readonly Task[]): Task[] {
  return tasks.filter((t) => isGoal(t) && t.status !== "done");
}

// archivedGoals are the done goals, most recently closed first.
export function archivedGoals(tasks: readonly Task[]): Task[] {
  return tasks.filter((t) => isGoal(t) && t.status === "done")
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.created_at.localeCompare(a.created_at));
}

// subTasksOf lists a goal's sub-tasks.
export function subTasksOf(tasks: readonly Task[], goalId: string): Task[] {
  return tasks.filter((t) => t.parent_task_id === goalId);
}

// validPick falls back to "all" when the picked goal is gone or done.
export function validPick(tasks: readonly Task[], pick: GoalPick): GoalPick {
  return pick !== "all" && activeGoals(tasks).some((g) => g.id === pick) ? pick : "all";
}

// boardTasks is what the board's columns show: active goals with their
// sub-tasks (any status), or one goal with its sub-tasks. A sub-task whose
// goal is missing is shown with the active ones, so nothing disappears.
export function boardTasks(tasks: readonly Task[], pick: GoalPick): Task[] {
  const p = validPick(tasks, pick);
  if (p !== "all") return tasks.filter((t) => t.id === p || t.parent_task_id === p);
  const goals = new Map(tasks.filter(isGoal).map((g) => [g.id, g]));
  return tasks.filter((t) => {
    if (isGoal(t)) return t.status !== "done";
    const g = goals.get(t.parent_task_id ?? "");
    return !g || g.status !== "done";
  });
}

// goalAgents are the agents of a goal and its sub-tasks.
export function goalAgents(goal: Task, agents: readonly Agent[], tasks: readonly Task[]): Agent[] {
  const ids = new Set([goal.id, ...subTasksOf(tasks, goal.id).map((t) => t.id)]);
  return agents.filter((a) => a.task_id !== undefined && ids.has(a.task_id));
}

// busyOf counts the agents of a goal that marking it done would stop in the
// middle of something: not idle (an idle Tech Lead after its report is the
// normal case) and not a reviewer, which finishes its review.
export function busyOf(goal: Task, agents: readonly Agent[], tasks: readonly Task[]): number {
  return goalAgents(goal, agents, tasks).filter((a) => a.state !== "exited" && a.state !== "idle" && a.kind !== "reviewer").length;
}

// latestMerge is a goal's newest merge.
export function latestMerge(merges: readonly GoalMerge[], goalId: string): GoalMerge | undefined {
  return merges.filter((m) => m.goal_id === goalId).sort((a, b) => b.id - a.id)[0];
}

// mergeOffered reports whether the goal card offers Merge: the Tech Lead
// reported (review, or done since) and the last QA passed on work not merged
// yet. The daemon's preview has the last word.
export function mergeOffered(goal: Task, runs: readonly QARun[], merges: readonly GoalMerge[]): boolean {
  if (!isGoal(goal) || (goal.status !== "review" && goal.status !== "done")) return false;
  const qa = latestQA(runs, goal.id);
  if (!qa || qa.status !== "passed" || !qa.head) return false;
  return latestMerge(merges, goal.id)?.source_head !== qa.head;
}

// pushOffered reports whether the goal's latest merge waits for its push.
export function pushOffered(merges: readonly GoalMerge[], goalId: string): boolean {
  const m = latestMerge(merges, goalId);
  return m !== undefined && !m.pushed_at;
}

// closeWarning is what marking a goal done asks first, or "" when nothing is busy.
export function closeWarning(busy: number): string {
  if (busy === 0) return "";
  return `${busy === 1 ? "1 agent of this goal is" : `${busy} agents of this goal are`} still working and will be ` +
    "stopped; uncommitted work is saved on their branches. Its sub-tasks are marked done too.";
}

// shortSha is a commit's first 8 characters.
export const shortSha = (sha: string): string => sha.slice(0, 8);
