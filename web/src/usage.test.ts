import { describe, expect, it } from "vitest";
import type { SessionUsage } from "./api";
import { formatMoney, formatTokens, groupSessions, sessionList, share, totalTokens, usageBy, zeroTokens } from "./usage";

const session = (over: Partial<SessionUsage>): SessionUsage => ({
  session_id: "s1", agent_id: "a1", kind: "worker", role: "worker", sandbox: "podman", source: "api", started_at: "2026-11-08T10:00:00Z",
  turns: 1, tool_calls: 2, messages: 3, tokens: { ...zeroTokens, input: 10, output: 20 }, cost_micro_usd: 1000,
  unpriced_tokens: 0, models: ["claude-haiku-5-5"], ...over,
});

describe("formatMoney", () => {
  it("formats integer micro-USD", () => {
    expect(formatMoney(0)).toBe("$0.0000");
    expect(formatMoney(68_864)).toBe("$0.0689");
    expect(formatMoney(49)).toBe("$0.0000");
    expect(formatMoney(50)).toBe("$0.0001");
    expect(formatMoney(999_960)).toBe("$1.00");
    expect(formatMoney(1_234_567)).toBe("$1.23");
    expect(formatMoney(1_995_000)).toBe("$2.00");
    expect(formatMoney(99_994_999)).toBe("$99.99");
    expect(formatMoney(123_500_000)).toBe("$124");
    expect(formatMoney(1_234_000_000)).toBe("$1,234");
  });
});

describe("formatTokens", () => {
  it("shortens counts", () => {
    expect(formatTokens(950)).toBe("950");
    expect(formatTokens(1234)).toBe("1,234");
    expect(formatTokens(12_345)).toBe("12.3k");
    expect(formatTokens(4_560_000)).toBe("4.56M");
  });
});

describe("groupSessions", () => {
  const sessions = [
    session({ session_id: "s1", agent_id: "a1", task_id: "t1", goal_id: "g", cost_micro_usd: 100 }),
    session({ session_id: "s2", agent_id: "a1", task_id: "t1", goal_id: "g", cost_micro_usd: 50, unpriced_tokens: 7 }),
    session({ session_id: "s3", agent_id: "a2", task_id: "t2", goal_id: "g", cost_micro_usd: 400 }),
    session({ session_id: "s4", agent_id: "a3", cost_micro_usd: 10 }),
  ];
  it("sums by key, most expensive first, skipping undefined keys", () => {
    const byTask = groupSessions(sessions, (s) => s.task_id);
    expect(byTask.map((r) => [r.key, r.costMicroUSD, r.sessions])).toEqual([["t2", 400, 1], ["t1", 150, 2]]);
    expect(byTask[1]).toMatchObject({ messages: 6, turns: 2, toolCalls: 4, unpricedTokens: 7, agents: ["a1"] });
    expect(totalTokens(byTask[1].tokens)).toBe(60);
    const byGoal = groupSessions(sessions, (s) => s.goal_id);
    expect(byGoal).toHaveLength(1);
    expect(byGoal[0]).toMatchObject({ costMicroUSD: 550, agents: ["a1", "a2"] });
  });
  it("looks up per agent", () => {
    const byAgent = usageBy(sessions, (s) => s.agent_id);
    expect(byAgent.get("a1")?.costMicroUSD).toBe(150);
    expect(byAgent.get("a3")?.sessions).toBe(1);
  });
});

describe("sessionList and share", () => {
  it("orders sessions by start", () => {
    const list = sessionList({
      b: session({ session_id: "b", started_at: "2026-11-08T11:00:00Z" }),
      a: session({ session_id: "a", started_at: "2026-11-08T10:00:00Z" }),
    });
    expect(list.map((s) => s.session_id)).toEqual(["a", "b"]);
  });
  it("clamps bar shares", () => {
    expect(share(5, 10)).toBe(0.5);
    expect(share(5, 0)).toBe(0);
    expect(share(20, 10)).toBe(1);
  });
});

describe("newerUsage", () => {
  it("keeps the copy that has seen more, in any order", async () => {
    const { newerUsage } = await import("./useAgents");
    const early = session({ messages: 2, turns: 1, tool_calls: 1500 });
    const later = session({ messages: 2, turns: 2, tool_calls: 3, tokens: { ...zeroTokens, input: 10, output: 20 } });
    const grown = session({ messages: 2, tokens: { ...zeroTokens, input: 10, output: 900 } });
    expect(newerUsage(early, later)).toBe(later);
    expect(newerUsage(later, early)).toBe(later);
    expect(newerUsage(later, grown)).toBe(grown); // a message's output grew (message_delta)
    expect(newerUsage(undefined, early)).toBe(early);
  });
});
