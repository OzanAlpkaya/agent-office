import { describe, expect, it } from "vitest";
import type { AccessRequest } from "./api";
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
});
