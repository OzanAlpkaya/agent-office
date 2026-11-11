// What a compact board card shows (Phase 18): one status chip, the agent
// working on the task and its cost. Pure, so Vitest covers it; the modal
// shows everything else.
import type { Agent, GoalMerge, InboxItem, QARun, Spec, Task } from "./api";
import { goalAgents, latestMerge } from "./goals";
import { latestQA, needsAnswer, proposedSpec, waitsFor } from "./workflow";

export type Tone = "ok" | "warn" | "danger" | "accent" | "violet" | "teal" | "muted";

export interface CardStatus {
  key: string; // stable, for tests and data attributes
  label: string;
  tone: Tone;
  title: string; // the chip's tooltip: why
}

export interface CardContext {
  tasks: readonly Task[];
  agents: readonly Agent[];
  qaRuns: readonly QARun[];
  merges: readonly GoalMerge[];
  specs: readonly Spec[];
  inbox: readonly InboxItem[];
  waitingAgents: ReadonlySet<string>; // agents waiting on the user (the Approvals list)
}

const isGoal = (t: Task): boolean => !t.parent_task_id;
const live = (a: Agent): boolean => a.state !== "exited";

// teamOf is whose state a card reflects: a sub-task's own agents; a goal's
// own and its sub-tasks' (the goal is done when they are).
export function teamOf(task: Task, ctx: Pick<CardContext, "agents" | "tasks">): Agent[] {
  return isGoal(task) ? goalAgents(task, ctx.agents, ctx.tasks) : ctx.agents.filter((a) => a.task_id === task.id);
}

const st = (key: string, label: string, tone: Tone, title: string): CardStatus => ({ key, label, tone, title });

// cardStatus picks the card's chip; the first rule that applies wins.
export function cardStatus(task: Task, ctx: CardContext): CardStatus {
  const goal = isGoal(task);
  const team = teamOf(task, ctx);
  const running = team.filter(live);
  // 1. Something waits on the user: a plan, a question, an approval or a permission prompt.
  const questions = ctx.inbox.filter((i) => needsAnswer(i) && (i.task_id === task.id || (goal && i.goal_id === task.id)));
  if (goal && task.status !== "done" && proposedSpec(ctx.specs, task.id)) {
    return st("plan", "Plan to approve", "warn", "The architect's plan waits for your approval");
  }
  if (task.status !== "done" && (questions.length > 0 || running.some((a) => ctx.waitingAgents.has(a.id)))) {
    return st("needs-you", "Needs you", "warn", "An agent waits for your answer or approval");
  }
  // 2. A goal's merge and QA.
  if (goal) {
    const m = latestMerge(ctx.merges, task.id);
    const qa = latestQA(ctx.qaRuns, task.id);
    if (m && (!qa || !qa.head || qa.head === m.source_head)) {
      return m.pushed_at
        ? st("pushed", "Pushed", "violet", `Merged into ${m.target} and pushed to ${m.push_remote ?? "the remote"}`)
        : st("merged", "Merged", "violet", `Merged into ${m.target}; not pushed`);
    }
    if (qa?.status === "running") return st("qa-running", "QA running", "accent", "QA is testing the reported branches");
    if (qa?.status === "failed" || qa?.status === "error") {
      return st("qa-failed", "QA failed", "danger", qa.status === "error" ? "QA could not finish" : `QA found ${qa.bugs.length} bug(s)`);
    }
    if (qa?.status === "passed" && task.status !== "done") return st("qa-passed", "QA passed", "ok", "QA passed: ready to merge");
  }
  // 3. What its agents do.
  if (task.status !== "done") {
    if (running.some((a) => a.state === "waiting_permission")) {
      return st("needs-you", "Needs you", "warn", "An agent waits for a permission answer in its terminal");
    }
    if (running.some((a) => a.state === "working")) return st("working", "Working", "accent", "An agent is working on it");
    if (running.some((a) => a.state === "starting")) return st("starting", "Starting", "muted", "An agent is starting");
  }
  // 4. The task's own status.
  switch (task.status) {
    case "blocked":
      return st("blocked", "Blocked", "danger", "Marked blocked");
    case "done":
      return st("done", "Done", "ok", "Done");
    case "review":
      return st("reported", goal ? "In review" : "Reported", "teal", goal ? "The Tech Lead reported the goal" : "Its agent reported it");
    default:
  }
  const deps = waitsFor(task, ctx.tasks);
  if (deps.length > 0) {
    return st("waiting", `Waits on ${deps.length}`, "muted", `Starts after: ${deps.map((d) => d.title).join(", ")}`);
  }
  if (task.status === "in_progress") {
    return running.length > 0
      ? st("idle", "Idle", "muted", "Its agents are idle")
      : st("no-agent", "No agent", "muted", "In progress, but no agent runs on it");
  }
  return st("todo", "To do", "muted", "Not started");
}

const kindOrder: Record<string, number> = { lead: 0, architect: 1, qa: 2, worker: 3, reviewer: 4, assistant: 5 };
const stateOrder: Record<string, number> = { working: 0, waiting_permission: 1, starting: 2, idle: 3, exited: 4 };

// CardAgent is the one agent a card names, and how many more work on it.
export interface CardAgent {
  agent?: Agent;
  more: number; // other agents of the card that are not exited
}

// cardAgent names the agent working on a task: for a goal its Tech Lead (or
// architect, or QA), for a sub-task its worker; a running one before an
// exited one, a busy one before an idle one. Exited agents count only when
// nobody runs, so a reported sub-task still says who built it.
export function cardAgent(task: Task, ctx: Pick<CardContext, "agents" | "tasks">): CardAgent {
  const team = teamOf(task, ctx);
  const own = team.filter((a) => a.task_id === task.id);
  const pick = (xs: readonly Agent[]): Agent | undefined =>
    xs.slice().sort((a, b) => (live(b) ? 1 : 0) - (live(a) ? 1 : 0) ||
      (kindOrder[a.kind] ?? 9) - (kindOrder[b.kind] ?? 9) || (stateOrder[a.state] ?? 9) - (stateOrder[b.state] ?? 9) ||
      Date.parse(b.started_at) - Date.parse(a.started_at))[0];
  const agent = pick(own.length > 0 ? own : team);
  const more = team.filter((a) => live(a) && a.id !== agent?.id).length;
  return { agent, more };
}
