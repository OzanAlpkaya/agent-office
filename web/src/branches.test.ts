import { describe, expect, it } from "vitest";
import type { BranchInfo, BranchPreview } from "./api";
import { branchGroups, deleteRequest } from "./branches";

const b = (name: string, cls: BranchInfo["class"], inUse?: string): BranchInfo => ({ name, head: `${name}-sha`, class: cls, ahead: 0, in_use: inUse });

describe("branch clean-up", () => {
  const branches = [
    b("agent-office/z-merged", "merged"), b("agent-office/a-squashed", "squashed"), b("agent-office/wip", "unmerged"),
    b("agent-office/live", "merged", "its agent is running"), b("agent-office/checked-out", "unmerged", "checked out"),
  ];

  it("groups safe, unmerged and in-use branches", () => {
    const g = branchGroups(branches);
    expect(g.safe.map((x) => x.name)).toEqual(["agent-office/a-squashed", "agent-office/z-merged"]);
    expect(g.unmerged.map((x) => x.name)).toEqual(["agent-office/wip"]);
    expect(g.inUse.map((x) => x.name)).toEqual(["agent-office/checked-out", "agent-office/live"]);
  });

  it("asks for the chosen heads, names unmerged ones, and never in-use ones", () => {
    const p: BranchPreview = { target: "main", target_head: "t", branches };
    const req = deleteRequest(p, new Set(["agent-office/z-merged", "agent-office/wip", "agent-office/live"]));
    expect(req).toEqual({
      branches: [{ name: "agent-office/z-merged", head: "agent-office/z-merged-sha" }, { name: "agent-office/wip", head: "agent-office/wip-sha" }],
      unmerged: ["agent-office/wip"],
    });
    expect(deleteRequest(p, new Set(["agent-office/z-merged"])).unmerged).toEqual([]);
  });
});
