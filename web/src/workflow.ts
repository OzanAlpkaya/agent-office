// Pure helpers for the team workflow UI (Phase 12): inbox, plans, QA,
// dependencies and conflicts. No React here, so Vitest covers them.
import type { InboxItem, Plan, PlanSubTask, QARun, RoleInfo, Spec, Task } from "./api";

// unread counts the inbox items the user has not opened yet.
export function unread(items: readonly InboxItem[]): number {
  return items.filter((i) => !i.read_at).length;
}

// needsAnswer reports whether an item asks the user for something.
export function needsAnswer(i: InboxItem): boolean {
  return Boolean(i.agent_id) && !i.answered_at && (i.kind === "question" || i.kind === "blocked");
}

// inboxFor lists the items about a task: its own, and for a goal its team's.
export function inboxFor(items: readonly InboxItem[], task: Task): InboxItem[] {
  return items.filter((i) => i.task_id === task.id || (!task.parent_task_id && i.goal_id === task.id));
}

// specsOf returns a goal's plan versions, newest first.
export function specsOf(specs: readonly Spec[], goalId: string): Spec[] {
  return specs.filter((s) => s.goal_id === goalId).sort((a, b) => b.version - a.version);
}

// currentSpec is the version the goal card shows: a proposal waiting for the
// user wins over the approved one; superseded versions are history.
export function currentSpec(specs: readonly Spec[], goalId: string): Spec | undefined {
  const own = specsOf(specs, goalId);
  return own.find((s) => s.status === "proposed") ?? own.find((s) => s.status === "approved");
}

// latestQA is a goal's newest QA run.
export function latestQA(runs: readonly QARun[], goalId: string): QARun | undefined {
  return runs.filter((r) => r.goal_id === goalId).sort((a, b) => b.id - a.id)[0];
}

// waitsFor lists the dependencies of a task that are not reported yet.
export function waitsFor(task: Task, tasks: readonly Task[]): Task[] {
  return (task.depends_on ?? [])
    .map((id) => tasks.find((t) => t.id === id))
    .filter((t): t is Task => t !== undefined && t.status !== "review" && t.status !== "done");
}

// workerRoles are the roles a task can be started with (worker behaviour).
export function workerRoles(roles: readonly RoleInfo[]): RoleInfo[] {
  return roles.filter((r) => r.behaviour === "worker");
}

// splitList turns a form field into list items: one per line or comma.
export function splitList(text: string, byLine = false): string[] {
  return text.split(byLine ? /\n/ : /[\n,]/).map((s) => s.trim()).filter((s) => s !== "");
}

// SubTaskForm is a plan sub-task as the editor holds it: lists as text.
export interface SubTaskForm {
  key: string;
  title: string;
  description: string;
  role: string;
  dependsOn: string; // comma separated keys
  files: string; // comma separated
  acceptance: string; // one per line
}

export interface PlanForm {
  summary: string;
  design: string;
  risks: string;
  subTasks: SubTaskForm[];
}

export function toForm(p: Plan): PlanForm {
  return {
    summary: p.summary, design: p.design, risks: p.risks ?? "",
    subTasks: p.sub_tasks.map((s) => ({
      key: s.key, title: s.title, description: s.description, role: s.role ?? "",
      dependsOn: (s.depends_on ?? []).join(", "), files: (s.files ?? []).join(", "),
      acceptance: (s.acceptance ?? []).join("\n"),
    })),
  };
}

export function fromForm(f: PlanForm): Plan {
  const sub = (s: SubTaskForm): PlanSubTask => {
    const out: PlanSubTask = { key: s.key.trim(), title: s.title.trim(), description: s.description.trim() };
    if (s.role.trim()) out.role = s.role.trim();
    const deps = splitList(s.dependsOn), files = splitList(s.files), acc = splitList(s.acceptance, true);
    if (deps.length) out.depends_on = deps;
    if (files.length) out.files = files;
    if (acc.length) out.acceptance = acc;
    return out;
  };
  const p: Plan = { summary: f.summary.trim(), design: f.design.trim(), sub_tasks: f.subTasks.map(sub) };
  if (f.risks.trim()) p.risks = f.risks.trim();
  return p;
}

// newSubTask is an empty sub-task with a key not used in the form yet.
export function newSubTask(f: PlanForm): SubTaskForm {
  const used = new Set(f.subTasks.map((s) => s.key));
  let n = f.subTasks.length + 1;
  while (used.has(`task-${n}`)) n++;
  return { key: `task-${n}`, title: "", description: "", role: "", dependsOn: "", files: "", acceptance: "" };
}
