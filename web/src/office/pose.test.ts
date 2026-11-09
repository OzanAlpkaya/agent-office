import { describe, expect, it } from "vitest";
import type { AgentState, Kind } from "../api";
import { hairColors, hairStyles, skins } from "./art";
import { hash, lookOf, poseOf } from "./pose";

describe("poseOf", () => {
  const cases: [AgentState, Kind, boolean, string][] = [
    ["starting", "worker", false, "booting"],
    ["idle", "worker", false, "idle"],
    ["working", "worker", false, "typing"],
    ["working", "lead", false, "typing"],
    ["working", "reviewer", false, "reviewing"],
    ["idle", "reviewer", false, "idle"],
    ["waiting_permission", "worker", false, "waiting"],
    ["waiting_permission", "reviewer", false, "waiting"],
    // an agent on the waiting list (waiting.ts) waits for the user, whatever the state
    ["working", "worker", true, "waiting"],
    ["idle", "lead", true, "waiting"],
    ["starting", "worker", true, "waiting"],
    // an exited agent is away even if a request was left pending
    ["exited", "worker", false, "away"],
    ["exited", "worker", true, "away"],
  ];
  it.each(cases)("%s %s (waiting on the user: %s) is %s", (state, kind, pending, want) => {
    expect(poseOf({ state, kind }, pending)).toBe(want);
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

describe("shirtOf", () => {
  it("dresses built-in roles in their kind's colour and role-pack roles in a colour of their own", async () => {
    const { shirtOf } = await import("./pose");
    const { shirts, roleShirts } = await import("./art");
    expect(shirtOf({ kind: "worker", role: "worker" })).toBe(shirts.worker);
    expect(shirtOf({ kind: "architect", role: "architect" })).toBe(shirts.architect);
    const docs = shirtOf({ kind: "worker", role: "docs" });
    const security = shirtOf({ kind: "worker", role: "security" });
    expect(roleShirts).toContain(docs);
    expect(docs).not.toBe(shirts.worker);
    expect(security).not.toBe(docs); // different roles, different shirts (for these two names)
    expect(shirtOf({ kind: "worker", role: "docs" })).toBe(docs); // stable
  });
});
