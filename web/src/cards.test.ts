import { describe, expect, it } from "vitest";
import type { Agent, GoalMerge, InboxItem, QARun, Spec, Task } from "./api";
import { type CardContext, cardAgent, cardStatus } from "./cards";

const task = (id: string, status: Task["status"], parent?: string, over: Partial<Task> = {}): Task => ({
  id, title: id, description: "", status, parent_task_id: parent, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z",
  ...over,
});
const agent = (id: string, taskId: string, state: Agent["state"], kind: Agent["kind"] = "worker", started = "2026-11-01T10:00:00Z"): Agent => ({
  id, name: id, task_id: taskId, state, kind, role: kind, sandbox: "podman", branch: "b", worktree_path: "/w", has_terminal: true,
  started_at: started,
} as Agent);
const qa = (goal: string, status: QARun["status"], head = "h1", id = 1): QARun => ({
  id, goal_id: goal, status, branch: "agent-office/qa", head, merged: [], bugs: [], created_at: "", updated_at: "",
});
const merge = (goal: string, source: string, pushed?: string): GoalMerge => ({
  id: 1, goal_id: goal, target: "main", source_head: source, target_before: "t0", commit: "c1", message: "m", created_at: "",
  pushed_at: pushed, push_remote: pushed ? "origin" : undefined,
});

function ctx(over: Partial<CardContext> = {}): CardContext {
  return { tasks: [], agents: [], qaRuns: [], merges: [], specs: [], inbox: [], waitingAgents: new Set(), ...over };
}

describe("cardStatus", () => {
  const g = task("g1", "in_progress");
  const s = task("s1", "in_progress", "g1");
  const tasks = [g, s];

  it("puts what waits on the user first", () => {
    const spec = { id: 1, goal_id: "g1", version: 1, status: "proposed" } as Spec;
    expect(cardStatus(g, ctx({ tasks, specs: [spec], agents: [agent("w", "s1", "working")] })).key).toBe("plan");
    const q: InboxItem = { id: 1, agent_id: "w", task_id: "s1", goal_id: "g1", kind: "question", body: "?", created_at: "" };
    expect(cardStatus(s, ctx({ tasks, inbox: [q], agents: [agent("w", "s1", "working")] })).key).toBe("needs-you");
    expect(cardStatus(g, ctx({ tasks, inbox: [q] })).key).toBe("needs-you"); // a goal shows its team's questions
    expect(cardStatus(s, ctx({ tasks, inbox: [{ ...q, answered_at: "x" }] })).key).toBe("no-agent");
    const w = agent("w", "s1", "idle");
    expect(cardStatus(s, ctx({ tasks, agents: [w], waitingAgents: new Set(["w"]) })).key).toBe("needs-you");
    expect(cardStatus(s, ctx({ tasks, agents: [agent("w", "s1", "waiting_permission")] })).key).toBe("needs-you");
  });

  it("shows a goal's merge, then its QA", () => {
    const rg = task("g1", "review");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "passed")] })).label).toBe("QA passed");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "running")] })).label).toBe("QA running");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "failed")] })).label).toBe("QA failed");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "error")] })).tone).toBe("danger");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "passed")], merges: [merge("g1", "h1")] })).label).toBe("Merged");
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "passed")], merges: [merge("g1", "h1", "now")] })).label).toBe("Pushed");
    // QA passed again on newer work: the old merge no longer describes it
    expect(cardStatus(rg, ctx({ tasks: [rg], qaRuns: [qa("g1", "passed", "h2", 2)], merges: [merge("g1", "h1")] })).label).toBe("QA passed");
    // a sub-task never shows its goal's QA
    expect(cardStatus(task("s1", "review", "g1"), ctx({ tasks: [rg], qaRuns: [qa("g1", "passed")] })).label).toBe("Reported");
  });

  it("shows what the agents do, then the task's status", () => {
    expect(cardStatus(s, ctx({ tasks, agents: [agent("w", "s1", "working")] })).label).toBe("Working");
    expect(cardStatus(g, ctx({ tasks, agents: [agent("w", "s1", "working")] })).label).toBe("Working"); // its team
    expect(cardStatus(s, ctx({ tasks, agents: [agent("w", "s1", "starting")] })).label).toBe("Starting");
    expect(cardStatus(s, ctx({ tasks, agents: [agent("w", "s1", "idle")] })).label).toBe("Idle");
    expect(cardStatus(s, ctx({ tasks, agents: [agent("w", "s1", "exited")] })).label).toBe("No agent");
    expect(cardStatus(task("x", "blocked"), ctx()).label).toBe("Blocked");
    expect(cardStatus(task("x", "done"), ctx({ agents: [agent("w", "x", "working")] })).label).toBe("Done");
    expect(cardStatus(task("x", "review"), ctx()).label).toBe("In review");
    expect(cardStatus(task("x", "todo"), ctx()).label).toBe("To do");
    const dep = task("d", "todo", "g1", { depends_on: ["s1"] });
    expect(cardStatus(dep, ctx({ tasks: [...tasks, dep] }))).toMatchObject({ key: "waiting", label: "Waits on 1" });
  });
});

describe("cardAgent", () => {
  const g = task("g1", "in_progress");
  const s = task("s1", "in_progress", "g1");
  const tasks = [g, s];

  it("names a goal's Tech Lead and counts the rest of the team", () => {
    const agents = [agent("w1", "s1", "working"), agent("lead", "g1", "idle", "lead"), agent("w2", "s1", "exited")];
    expect(cardAgent(g, { tasks, agents })).toEqual({ agent: agents[1], more: 1 });
  });

  it("names a sub-task's busiest running agent, else the latest exited one", () => {
    const rv = agent("rv", "s1", "working", "reviewer");
    const w = agent("w", "s1", "idle");
    expect(cardAgent(s, { tasks, agents: [rv, w] })).toEqual({ agent: w, more: 1 });
    const old = agent("old", "s1", "exited", "worker", "2026-11-01T09:00:00Z");
    const recent = agent("new", "s1", "exited", "worker", "2026-11-01T11:00:00Z");
    expect(cardAgent(s, { tasks, agents: [old, recent] })).toEqual({ agent: recent, more: 0 });
    expect(cardAgent(task("x", "todo"), { tasks, agents: [] })).toEqual({ agent: undefined, more: 0 });
  });
});
