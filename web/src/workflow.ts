// Pure helpers for the team workflow UI (Phase 12): inbox, plans, QA,
// dependencies and conflicts. No React here, so Vitest covers them.
import type { Agent, InboxItem, Plan, PlanSubTask, QARun, RoleInfo, Spec, Task } from "./api";

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
// A version withdrawn by a change request (Phase 20) is current until the
// Architect submits the next one.
export function currentSpec(specs: readonly Spec[], goalId: string): Spec | undefined {
  const own = specsOf(specs, goalId);
  if (own[0] && withdrawn(own[0])) return own[0];
  return own.find((s) => s.status === "proposed") ?? own.find((s) => s.status === "approved");
}

// withdrawn reports whether a version was taken back by the user's change
// request (Phase 20): superseded, with the request on it.
export function withdrawn(s: Spec): boolean {
  return s.status === "superseded" && Boolean(s.change_request);
}

// proposedSpec is the goal's plan version waiting for approval, if any.
export function proposedSpec(specs: readonly Spec[], goalId: string): Spec | undefined {
  return specsOf(specs, goalId).find((s) => s.status === "proposed");
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

// Light plan edits (Phase 19): one sub-task or the summary at a time, each a
// new version. Pure, so Vitest covers them.

// withSubTask replaces the sub-task at i (or adds one at the end, i = -1).
// A renamed key is renamed in the other sub-tasks' dependencies too.
export function withSubTask(p: Plan, i: number, st: PlanSubTask): Plan {
  const old = i >= 0 ? p.sub_tasks[i]?.key : undefined;
  const subs = p.sub_tasks.map((s, j) => {
    if (j === i) return st;
    if (old && old !== st.key && s.depends_on?.includes(old)) {
      return { ...s, depends_on: s.depends_on.map((d) => (d === old ? st.key : d)) };
    }
    return s;
  });
  if (i < 0) subs.push(st);
  return { ...p, sub_tasks: subs };
}

// withoutSubTask drops a sub-task and the dependencies on it.
export function withoutSubTask(p: Plan, key: string): Plan {
  return {
    ...p,
    sub_tasks: p.sub_tasks.filter((s) => s.key !== key).map((s) => {
      if (!s.depends_on?.includes(key)) return s;
      const deps = s.depends_on.filter((d) => d !== key);
      const { depends_on: _old, ...rest } = s;
      return deps.length > 0 ? { ...rest, depends_on: deps } : rest;
    }),
  };
}

// subTaskForm and fromSubTaskForm convert one sub-task for the inline editor.
export function subTaskForm(s: PlanSubTask): SubTaskForm {
  return toForm({ summary: "", design: "", sub_tasks: [s] }).subTasks[0];
}
export function fromSubTaskForm(f: SubTaskForm): PlanSubTask {
  return fromForm({ summary: "", design: "", risks: "", subTasks: [f] }).sub_tasks[0];
}

// pendingChanges is the change request the Architect still has to answer on
// a goal's current version: none once a newer version exists.
export function pendingChanges(spec: Spec, specs: readonly Spec[]): string | undefined {
  if (!spec.change_request) return undefined;
  const newer = specs.some((x) => x.goal_id === spec.goal_id && x.version > spec.version);
  return newer ? undefined : spec.change_request;
}

// GoalBusy says why a goal cannot get its own Tech Lead or architect now:
// one of them runs for another goal of the project (one goal at a time per
// project, ADR 0017), and which agent to open.
export interface GoalBusy {
  agentId: string;
  text: string;
}

export function goalBusy(goal: Task, agents: readonly Agent[], tasks: readonly Task[]): GoalBusy | undefined {
  if (goal.parent_task_id) return undefined;
  const other = agents.find((a) => (a.kind === "lead" || a.kind === "architect") && a.state !== "exited" &&
    a.task_id !== goal.id);
  if (!other) return undefined;
  const title = tasks.find((t) => t.id === other.task_id)?.title;
  const who = other.kind === "lead" ? "Tech Lead" : "architect";
  const of = title ? ` of “${title}”` : "";
  return {
    agentId: other.id,
    text: `One goal at a time in this project: the ${who}${of} is still running. To start this goal, mark that ` +
      `goal done or stop its ${who}; or add this repository as another project to run both at once.`,
  };
}

// InboxAgentGroup is one sender's items in a goal: what still needs the user,
// and the rest (answered or read), shown collapsed.
export interface InboxAgentGroup {
  agentId: string; // "" for agent-office itself
  open: InboxItem[];
  earlier: InboxItem[];
}

// InboxGoalGroup is a goal's items ("" for items of no goal).
export interface InboxGoalGroup {
  goalId: string;
  agents: InboxAgentGroup[];
  open: number;
  latest: string; // the newest item's created_at (UTC ISO)
}

export interface InboxView {
  groups: InboxGoalGroup[];
  briefings: InboxItem[]; // the assistant's briefings, newest first, kept apart
}

// isOpen reports whether an item still needs the user: unread, or a
// question or blocked message without an answer.
export function isOpen(i: InboxItem): boolean {
  return needsAnswer(i) || !i.read_at;
}

// groupInbox groups the inbox by goal, then by agent (Phase 16): groups with
// open items first, then the most recent; within a group, items newest first.
// Briefings are kept apart.
export function groupInbox(items: readonly InboxItem[]): InboxView {
  const newestFirst = [...items].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id - a.id);
  const briefings = newestFirst.filter((i) => i.kind === "briefing");
  const goals = new Map<string, InboxGoalGroup>();
  for (const i of newestFirst) {
    if (i.kind === "briefing") continue;
    const gid = i.goal_id ?? "";
    let g = goals.get(gid);
    if (!g) {
      g = { goalId: gid, agents: [], open: 0, latest: i.created_at };
      goals.set(gid, g);
    }
    const aid = i.agent_id ?? "";
    let a = g.agents.find((x) => x.agentId === aid);
    if (!a) {
      a = { agentId: aid, open: [], earlier: [] };
      g.agents.push(a);
    }
    if (isOpen(i)) {
      a.open.push(i);
      g.open++;
    } else {
      a.earlier.push(i);
    }
  }
  const groups = [...goals.values()];
  for (const g of groups) {
    g.agents.sort((x, y) => Number(y.open.length > 0) - Number(x.open.length > 0));
  }
  groups.sort((x, y) => Number(y.open > 0) - Number(x.open > 0) || y.latest.localeCompare(x.latest));
  return { groups, briefings };
}
