import { describe, expect, it } from "vitest";
import { aboveDaemonCap, capNote, draftCaps, needsStop, runningLabel } from "./caps";

describe("caps", () => {
  const c = { max_agents: 4, daemon_max: 6, effective: 4, running: 3, max_sub_tasks: 20 };
  it("explains the effective cap and who counts", () => {
    expect(capNote(c)).toBe("Effective: 4 (the smaller of this project's 4 and the daemon's 6). The Tech Lead, Architect and QA count too.");
    // Phase 20: a project above the daemon-wide cap says what that means
    expect(capNote({ ...c, max_agents: 8, effective: 6 })).toBe("Effective: 6. The daemon-wide cap of 6 is below this project's 8: " +
      "at most 6 agents run in all projects together. The Tech Lead, Architect and QA count too.");
    expect(capNote({ ...c, daemon_max: 4 })).toContain("both 4");
    expect(runningLabel(c)).toBe("3 / 4 running");
  });
  it("computes the cap a settings draft would have (Phase 20)", () => {
    expect(draftCaps("8", 32, 4)).toEqual({ max_agents: 8, daemon_max: 4, effective: 4, running: 0, max_sub_tasks: 0 });
    expect(aboveDaemonCap(draftCaps("8", 32, 4) as NonNullable<ReturnType<typeof draftCaps>>)).toBe(true);
    expect(draftCaps("", 32, 4)?.effective).toBe(4); // empty: the default for projects
    expect(draftCaps(" 3 ", 32, 4)).toMatchObject({ effective: 3, max_agents: 3 });
    expect(aboveDaemonCap({ max_agents: 3, daemon_max: 4 })).toBe(false);
    expect(draftCaps("x", 32, 4)).toBeUndefined();
    expect(draftCaps("8", 32, undefined)).toBeUndefined();
    expect(draftCaps("0", 32, 4)).toBeUndefined();
  });
  it("names the settings that need the agents stopped", () => {
    expect(needsStop({ model: "haiku", max_agents: 2 }, { model: "opus", max_agents: 5, max_sub_tasks: 30 })).toEqual([]);
    expect(needsStop({ pack: "go" }, { pack: "node", model: "opus" })).toEqual(["pack"]);
    expect(needsStop({}, { egress_allow: ["a.example"], reviewer: "none" })).toEqual(["egress_allow", "reviewer"]);
  });
});
