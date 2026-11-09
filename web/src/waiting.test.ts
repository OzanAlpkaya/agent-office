import { describe, expect, it } from "vitest";
import type { AccessRequest, InboxItem, Spec } from "./api";
import { agent } from "./office/testutil";
import { waitingOnUser } from "./waiting";

const req = (id: number, agentId: string, minute: number, status: AccessRequest["status"] = "pending"): AccessRequest => ({
  id, agent_id: agentId, kind: "domain", value: `h${id}.example`, reason: "", status,
  created_at: `2026-11-09T10:0${minute}:00Z`,
});

describe("waitingOnUser", () => {
  it("lists agents with requests or a permission prompt, one entry per agent", () => {
    const agents = [
      agent("net", { state: "working", started_at: "2026-11-09T09:00:00Z" }),
      agent("perm", { state: "waiting_permission", sandbox: "host", started_at: "2026-11-09T10:03:00Z" }),
      agent("both", { state: "waiting_permission", sandbox: "host" }),
      agent("calm", { state: "working" }),
      agent("gone", { state: "exited" }),
    ];
    const w = waitingOnUser(agents, [
      req(1, "net", 5), req(2, "net", 2), req(3, "both", 4), req(4, "calm", 1, "denied"), req(5, "gone", 1), req(6, "unknown", 1),
    ]);
    expect(w.map((x) => [x.agent.id, x.requests.map((r) => r.id), x.permission])).toEqual([
      ["net", [2, 1], false], // two requests: one entry, oldest first
      ["perm", [], true],
      ["both", [3], true],
    ]);
  });

  it("is empty when nothing waits", () => {
    expect(waitingOnUser([agent("a", { state: "idle" })], [req(1, "a", 1, "approved")])).toEqual([]);
  });

  const question = (id: number, agentId: string | undefined, minute: number, over: Partial<InboxItem> = {}): InboxItem => ({
    id, agent_id: agentId, kind: "question", body: "Which database?", created_at: `2026-11-09T10:0${minute}:00Z`, ...over,
  });
  const spec = (id: number, author: string | undefined, status: Spec["status"], version = 1): Spec => ({
    id, goal_id: "g1", version, status, author, markdown: "", created_at: "2026-11-09T10:02:00Z",
    plan: { summary: "", design: "", sub_tasks: [] } as unknown as Spec["plan"],
  });

  it("counts an agent whose question waits for an answer, once, oldest question first", () => {
    const agents = [agent("arch", { kind: "architect", state: "idle" }), agent("lead", { kind: "lead", state: "idle" })];
    const w = waitingOnUser(agents, [], [
      question(2, "arch", 4), question(1, "arch", 3),
      question(3, "lead", 1, { answered_at: "2026-11-09T10:05:00Z" }), // answered
      question(4, "lead", 1, { kind: "info" }), // not a question
      question(5, undefined, 1), // from agent-office itself
    ]);
    expect(w.map((x) => [x.agent.id, x.questions.map((q) => q.id)])).toEqual([["arch", [1, 2]]]);
  });

  it("counts an architect whose plan waits for approval, with its newest proposal", () => {
    const agents = [agent("arch", { kind: "architect", state: "idle" })];
    const w = waitingOnUser(agents, [], [], [spec(1, "arch", "superseded"), spec(2, "arch", "proposed", 2), spec(3, undefined, "proposed", 3)]);
    expect(w.map((x) => [x.agent.id, x.plan?.id])).toEqual([["arch", 2]]);
    // an approved plan waits for nobody; nor does an exited architect
    expect(waitingOnUser(agents, [], [], [spec(4, "arch", "approved")])).toEqual([]);
    expect(waitingOnUser([agent("arch", { kind: "architect", state: "exited" })], [], [question(1, "arch", 1)], [spec(2, "arch", "proposed")])).toEqual([]);
  });

  it("orders by what has waited longest, whatever it is", () => {
    const agents = [
      agent("q", { state: "idle", started_at: "2026-11-09T09:00:00Z" }),
      agent("r", { state: "working", started_at: "2026-11-09T09:00:00Z" }),
    ];
    const w = waitingOnUser(agents, [req(1, "r", 5)], [question(1, "q", 2)]);
    expect(w.map((x) => x.agent.id)).toEqual(["q", "r"]);
  });
});
