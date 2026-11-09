import { describe, expect, it } from "vitest";
import type { Agent, AgentState, Kind, Task } from "./api";
import {
  agentLabel, closeTab, groupAgents, keepTabs, labelOf, matchAgent, moveTab, neighbourTab, noTabs, openTab, parseTabs,
  searchAgents,
} from "./agents";

const at = (m: number): string => new Date(Date.parse("2026-11-09T10:00:00Z") + m * 60_000).toISOString();
const agent = (id: string, name: string, kind: Kind, state: AgentState, over: Partial<Agent> = {}): Agent => ({
  id, name, kind, role: kind === "lead" ? "tech-lead" : kind, state, inherit_user_settings: false, sandbox: "podman",
  branch: `agent-office/${id}`, worktree_path: "", workspace_kind: "clone", worktree_status: "active", has_terminal: true,
  started_at: at(0), resumable: false, ...over,
});
const task = (id: string, title: string, over: Partial<Task> = {}): Task =>
  ({ id, title, description: "", status: "in_progress", created_at: at(0), updated_at: at(0), ...over });

describe("labels", () => {
  it("prefers the name and falls back to the short id", () => {
    expect(agentLabel(agent("c3d4e5f6a7b8", "Amara", "worker", "idle"))).toBe("Amara");
    expect(agentLabel(agent("c3d4e5f6a7b8", "", "worker", "idle"))).toBe("c3d4e5");
    expect(agentLabel(undefined, "abcdef123456")).toBe("abcdef");
    expect(labelOf([agent("a1", "Leo", "worker", "idle")], "a1")).toBe("Leo");
    expect(labelOf([], "ffeedd001122")).toBe("ffeedd");
  });
});

describe("tabs", () => {
  it("opens, keeps order, and replaces the least recently used tab when full", () => {
    let t = noTabs;
    for (const id of ["a", "b", "c"]) t = openTab(t, id, 3);
    t = openTab(t, "a", 3); // used again: b is now the least recently used
    t = openTab(t, "d", 3);
    expect(t.order).toEqual(["a", "c", "d"]);
    t = openTab(t, "c", 3);
    expect(t.order).toEqual(["a", "c", "d"]); // opening an open tab does not move it
  });
  it("closes, finds a neighbour and moves", () => {
    let t = noTabs;
    for (const id of ["a", "b", "c", "d"]) t = openTab(t, id);
    expect(neighbourTab(t, "b")).toBe("c");
    expect(neighbourTab(t, "d")).toBe("c");
    expect(neighbourTab(t, "x")).toBeUndefined();
    t = closeTab(t, "b");
    expect(t.order).toEqual(["a", "c", "d"]);
    expect(t.used).not.toContain("b");
    expect(moveTab(t, "d", 0).order).toEqual(["d", "a", "c"]);
    expect(moveTab(t, "a", 9).order).toEqual(["c", "d", "a"]);
    expect(moveTab(t, "zz", 0)).toBe(t);
  });
  it("drops removed agents and survives bad storage", () => {
    let t = noTabs;
    for (const id of ["a", "b"]) t = openTab(t, id);
    expect(keepTabs(t, new Set(["a", "b"]))).toBe(t);
    expect(keepTabs(t, new Set(["b"])).order).toEqual(["b"]);
    expect(parseTabs(JSON.stringify(t))).toEqual(t);
    expect(parseTabs("{oops")).toEqual(noTabs);
    expect(parseTabs(null)).toEqual(noTabs);
    expect(parseTabs(JSON.stringify({ order: ["a", 3, "a", "b"], used: ["z", "b"] }))).toEqual({ order: ["a", "b"], used: ["b"] });
  });
});

describe("switcher", () => {
  const goal = task("g1", "Add tags to saved links", { created_at: at(1) });
  const goal2 = task("g2", "Health check", { created_at: at(2) });
  const tasks = [
    goal, goal2,
    task("t1", "Tag model and migration", { parent_task_id: "g1" }),
    task("t2", "Tag filter in the list API", { parent_task_id: "g1" }),
    task("t3", "GET /healthz", { parent_task_id: "g2" }),
  ];
  const agents = [
    agent("w1aaaa000001", "Emma", "worker", "working", { task_id: "t1", started_at: at(2) }),
    agent("l1aaaa000001", "Kofi", "lead", "idle", { task_id: "g1", started_at: at(3) }),
    agent("w2aaaa000002", "Yuki", "worker", "working", { task_id: "t2", started_at: at(1) }),
    agent("w3aaaa000003", "Omar", "worker", "idle", { task_id: "t3" }),
    agent("s1aaaa000004", "Priya", "worker", "idle"),
    agent("x1aaaa000005", "Lena", "worker", "exited", { task_id: "t1", exited_at: at(5) }),
    agent("x2aaaa000006", "Ravi", "reviewer", "exited", { exited_at: at(9) }),
  ];

  it("groups running agents by goal and lists exited ones last", () => {
    const groups = groupAgents(agents, tasks);
    expect(groups.map((g) => [g.title, g.agents.map((a) => a.name)])).toEqual([
      ["Add tags to saved links", ["Kofi", "Yuki", "Emma"]],
      ["Health check", ["Omar"]],
      ["No task", ["Priya"]],
      ["Exited", ["Ravi", "Lena"]],
    ]);
  });

  it("searches name, id, role, task, goal and branch", () => {
    const names = (q: string): string[] => searchAgents(agents, tasks, q).flatMap((g) => g.agents.map((a) => a.name));
    expect(names("emma")).toEqual(["Emma"]);
    expect(names("w2aaaa")).toEqual(["Yuki"]);
    expect(names("reviewer")).toEqual(["Ravi"]);
    expect(names("filter")).toEqual(["Yuki"]);
    expect(names("health")).toEqual(["Omar"]);
    expect(names("tags lead")).toEqual(["Kofi"]);
    expect(names("agent-office/s1")).toEqual(["Priya"]);
    expect(names("nobody")).toEqual([]);
    expect(names("  ")).toHaveLength(agents.length);
    expect(matchAgent(agents[0], tasks, "EMMA WORKING")).toBe(false); // the state is not searched
  });
});
