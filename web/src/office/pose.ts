// What each agent's character does, and how it looks (ADR 0024). Pure.
import type { Agent } from "../api";
import { type HairStyle, hairColors, hairStyles, skins } from "./art";

export type Pose = "booting" | "idle" | "typing" | "reviewing" | "waiting" | "away";

export const poseLabel: Record<Pose, string> = {
  booting: "Starting",
  idle: "Idle",
  typing: "Working",
  reviewing: "Reviewing",
  waiting: "Waiting for you",
  away: "Exited",
};

// poseOf maps an agent's state and role to its pose. pendingAccess is true
// while the agent has an access request waiting for the user (ADR 0019),
// which is waiting for the user just like a permission prompt.
export function poseOf(a: Pick<Agent, "state" | "role">, pendingAccess: boolean): Pose {
  if (a.state === "exited") return "away";
  if (a.state === "waiting_permission" || pendingAccess) return "waiting";
  switch (a.state) {
    case "starting":
      return "booting";
    case "idle":
      return "idle";
    case "working":
      return a.role === "reviewer" ? "reviewing" : "typing";
  }
}

export interface Look {
  skin: number; // index into skins
  hair: number; // index into hairColors
  style: HairStyle;
  phase: number; // 0..1, offsets this agent's animations from the others'
}

// hash is 32-bit FNV-1a: small, stable across sessions and browsers.
export function hash(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// lookOf derives an agent's appearance from its id, so it is the same on
// every reload and in every browser.
export function lookOf(id: string): Look {
  const h = hash(id);
  return {
    skin: h % skins.length,
    hair: Math.floor(h / 7) % hairColors.length,
    style: hairStyles[Math.floor(h / 53) % hairStyles.length],
    phase: (Math.floor(h / 331) % 1000) / 1000,
  };
}
