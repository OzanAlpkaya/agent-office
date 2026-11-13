import { describe, expect, it } from "vitest";
import type { AccessRequest, Project } from "./api";
import { agent } from "./office/testutil";
import { applyProjects, elsewhere, emptyLight, pickProject, reduceLight, waitingTotal } from "./projects";
import { draftOf, settingsOf } from "./Projects";

const project = (id: string, over: Partial<Project> = {}): Project => ({
  id, name: id, repo_path: `/repos/${id}`, slug: `${id}-12345678`, settings: {}, effective: {}, state: "open",
  default: false, created_at: "2026-11-11T09:00:00Z", ...over,
});
const req = (id: number, agentId: string): AccessRequest => ({
  id, agent_id: agentId, kind: "domain", value: "h.example", reason: "", status: "pending", created_at: "2026-11-11T10:00:00Z",
});

describe("projects", () => {
  it("picks the chosen project, else the daemon's default, else the first open one", () => {
    const ps = [project("a", { state: "error" }), project("b"), project("c")];
    expect(pickProject(ps, "c", "b")?.id).toBe("c");
    expect(pickProject(ps, "gone", "b")?.id).toBe("b");
    expect(pickProject(ps, undefined, undefined)?.id).toBe("b");
    expect(pickProject([project("x", { state: "error" })], undefined)?.id).toBe("x");
    expect(pickProject([], "a")).toBeUndefined();
  });

  it("follows the daemon's project list", () => {
    let list = applyProjects([], { type: "projects", projects: [project("a"), project("b")] });
    list = applyProjects(list, { type: "project", project: project("b", { name: "renamed" }) });
    list = applyProjects(list, { type: "project", project: project("c") });
    list = applyProjects(list, { type: "project_removed", id: "a" });
    expect(list.map((p) => `${p.id}:${p.name}`)).toEqual(["b:renamed", "c:c"]);
  });

  it("keeps only what decides waiting for a project that is not shown", () => {
    let w = reduceLight(emptyLight, {
      type: "snapshot", agents: [agent("w1", { state: "working" })], tasks: [], accessRequests: [], pullRequests: [],
      inbox: [], specs: [], qaRuns: [], memory: [],
    });
    w = reduceLight(w, { type: "access_request", request: req(1, "w1") });
    w = reduceLight(w, { type: "agent", agent: agent("w2", { state: "waiting_permission", sandbox: "host" }) });
    w = reduceLight(w, { type: "task_deleted", id: "t1" }); // not its business
    expect(w.agents.map((a) => a.id)).toEqual(["w1", "w2"]);
    w = reduceLight(w, { type: "access_request", request: { ...req(1, "w1"), status: "approved" } });
    expect(w.accessRequests).toHaveLength(1);
    w = reduceLight(w, { type: "agent_removed", id: "w2" });
    expect(w.agents.map((a) => a.id)).toEqual(["w1"]);
  });

  it("counts every project's waits for the badge, with the shown one excluded from elsewhere", () => {
    const ps = [project("a"), project("b"), project("c")];
    const worlds = {
      b: { ...emptyLight, agents: [agent("b1", { state: "working" }), agent("b2", { state: "idle" })], accessRequests: [req(1, "b1"), req(2, "b2")] },
      c: { ...emptyLight, agents: [agent("c1", { state: "exited" })], accessRequests: [req(3, "c1")] },
    };
    const others = elsewhere(ps, "a", worlds);
    expect(others.map((e) => [e.project.id, e.waiting.length, e.running])).toEqual([["b", 2, 2], ["c", 0, 0]]);
    expect(waitingTotal(1, others)).toBe(3);
  });
});

describe("project settings form", () => {
  it("turns empty fields into inherited settings and dollars into micro-USD", () => {
    const s = settingsOf({ pack: " node ", model: "", maxAgents: "2", maxSubTasks: "", github: "off", budgetSessionUSD: "0.25",
      budgetGoalUSD: "", egress: "registry.npmjs.org, , example.com", workerMemory: "", workerPids: "", mergeTarget: "",
      accessDecider: "", leadHosts: "" });
    expect(s).toEqual({ pack: "node", max_agents: 2, github: false, budget_session_micro_usd: 250000,
      egress_allow: ["registry.npmjs.org", "example.com"] });
  });

  it("keeps settings the form does not show and drops cleared ones", () => {
    const base = { secrets_dir: "/s", role_models: { qa: "opus" }, model: "sonnet" };
    const s = settingsOf({ ...draftOf(base), model: "" }, base);
    expect(s).toEqual({ secrets_dir: "/s", role_models: { qa: "opus" } });
  });

  it("round-trips a project's settings through the form", () => {
    const base = { pack: "go", model: "haiku", max_agents: 3, github: true, budget_goal_micro_usd: 1_500_000, egress_allow: ["a.example"] };
    expect(settingsOf(draftOf(base), base)).toEqual(base);
  });

  it("sets the worker memory and who decides access requests (Phase 16)", () => {
    const base = { worker_memory: "4g", access_decider: "lead_policy" as const, lead_access_hosts: ["storage.googleapis.com", ".example.org"] };
    expect(draftOf(base)).toMatchObject({ workerMemory: "4g", accessDecider: "lead_policy", leadHosts: "storage.googleapis.com, .example.org" });
    expect(settingsOf(draftOf(base), base)).toEqual(base);
    expect(settingsOf({ ...draftOf(base), accessDecider: "", leadHosts: "", workerMemory: "" }, base)).toEqual({});
    expect(() => settingsOf({ ...draftOf({}), workerMemory: "4 GB" })).toThrow("Worker memory");
    // Phase 17: the process limit and the merge target.
    expect(settingsOf({ ...draftOf({}), workerPids: " 2048 ", mergeTarget: " develop " })).toEqual({ worker_pids: 2048, merge_target: "develop" });
    expect(draftOf({ worker_pids: 1024, merge_target: "main" })).toMatchObject({ workerPids: "1024", mergeTarget: "main" });
    expect(() => settingsOf({ ...draftOf({}), workerPids: "32" })).toThrow("Worker processes");
    expect(() => settingsOf({ ...draftOf({}), workerPids: "1e3" })).toThrow("Worker processes");
  });

  it("refuses what is not a number", () => {
    expect(() => settingsOf({ ...draftOf({}), maxAgents: "two" })).toThrow("Max agents");
    expect(() => settingsOf({ ...draftOf({}), budgetGoalUSD: "-1" })).toThrow("Goal budget");
  });
});
