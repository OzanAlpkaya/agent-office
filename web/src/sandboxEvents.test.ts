import { describe, expect, it } from "vitest";
import { backgroundLine, isBackground, splitEvents } from "./sandboxEvents";

const ev = (kind: string, detail = "x"): { kind: string; detail: string; at: string } => ({ kind, detail, at: "2026-11-11T10:00:00Z" });

describe("sandbox events", () => {
  it("keeps Claude Code's background refusals apart from problems", () => {
    const events = [ev("egress_denied", "github.com:443"), ev("api_background"), ev("oom_kill"), ev("egress_background")];
    const { problems, background } = splitEvents(events);
    expect(problems.map((e) => e.kind)).toEqual(["egress_denied", "oom_kill"]);
    expect(background.map((e) => e.kind)).toEqual(["api_background", "egress_background"]);
    expect(isBackground(ev("api_rejected"))).toBe(false);
  });
  it("summarizes background refusals quietly", () => {
    expect(backgroundLine(0)).toBe("");
    expect(backgroundLine(1)).toContain("1 background request");
    expect(backgroundLine(3)).toContain("3 background requests");
  });
});
