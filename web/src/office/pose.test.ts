import { describe, expect, it } from "vitest";
import type { AgentState, Role } from "../api";
import { hairColors, hairStyles, skins } from "./art";
import { hash, lookOf, poseOf } from "./pose";

describe("poseOf", () => {
  const cases: [AgentState, Role, boolean, string][] = [
    ["starting", "worker", false, "booting"],
    ["idle", "worker", false, "idle"],
    ["working", "worker", false, "typing"],
    ["working", "lead", false, "typing"],
    ["working", "reviewer", false, "reviewing"],
    ["idle", "reviewer", false, "idle"],
    ["waiting_permission", "worker", false, "waiting"],
    ["waiting_permission", "reviewer", false, "waiting"],
    // a pending access request waits for the user too, whatever the state
    ["working", "worker", true, "waiting"],
    ["idle", "lead", true, "waiting"],
    ["starting", "worker", true, "waiting"],
    // an exited agent is away even if a request was left pending
    ["exited", "worker", false, "away"],
    ["exited", "worker", true, "away"],
  ];
  it.each(cases)("%s %s (pending access: %s) is %s", (state, role, pending, want) => {
    expect(poseOf({ state, role }, pending)).toBe(want);
  });
});

describe("lookOf", () => {
  it("is stable and within the palettes", () => {
    for (let i = 0; i < 200; i++) {
      const id = `agent-${i}`;
      const l = lookOf(id);
      expect(lookOf(id)).toEqual(l);
      expect(l.skin).toBeGreaterThanOrEqual(0);
      expect(l.skin).toBeLessThan(skins.length);
      expect(l.hair).toBeLessThan(hairColors.length);
      expect(hairStyles).toContain(l.style);
      expect(l.phase).toBeGreaterThanOrEqual(0);
      expect(l.phase).toBeLessThan(1);
    }
  });

  it("varies between agents", () => {
    const looks = new Set(Array.from({ length: 50 }, (_, i) => JSON.stringify(lookOf(`a${i}`))));
    expect(looks.size).toBeGreaterThan(30);
    const styles = new Set(Array.from({ length: 50 }, (_, i) => lookOf(`a${i}`).style));
    expect(styles.size).toBe(hairStyles.length);
  });

  it("hash is FNV-1a", () => {
    expect(hash("")).toBe(0x811c9dc5);
    expect(hash("a")).toBe(0xe40c292c);
  });
});
