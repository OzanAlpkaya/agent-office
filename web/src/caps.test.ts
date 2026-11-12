import { describe, expect, it } from "vitest";
import { capNote, needsStop, runningLabel } from "./caps";

describe("caps", () => {
  const c = { max_agents: 4, daemon_max: 6, effective: 4, running: 3, max_sub_tasks: 20 };
  it("explains the effective cap and who counts", () => {
    expect(capNote(c)).toBe("Effective: 4 (the smaller of this project's 4 and the daemon's 6). The Tech Lead, Architect and QA count too.");
    expect(capNote({ ...c, max_agents: 8, effective: 6 })).toContain("the daemon's cap of 6 is below this project's 8");
    expect(capNote({ ...c, daemon_max: 4 })).toContain("both 4");
    expect(runningLabel(c)).toBe("3 / 4 running");
  });
  it("names the settings that need the agents stopped", () => {
    expect(needsStop({ model: "haiku", max_agents: 2 }, { model: "opus", max_agents: 5, max_sub_tasks: 30 })).toEqual([]);
    expect(needsStop({ pack: "go" }, { pack: "node", model: "opus" })).toEqual(["pack"]);
    expect(needsStop({}, { egress_allow: ["a.example"], reviewer: "none" })).toEqual(["egress_allow", "reviewer"]);
  });
});
