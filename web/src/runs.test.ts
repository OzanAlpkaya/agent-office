import { describe, expect, it } from "vitest";
import { exitLabel, formatDuration, isAgentRun, timeRange, tokenParts } from "./runs";

describe("run info", () => {
  it("formats durations", () => {
    expect([formatDuration(0), formatDuration(45), formatDuration(720), formatDuration(3900)]).toEqual(["0s", "45s", "12m", "1h 05m"]);
  });
  it("names exit reasons and time ranges", () => {
    expect(exitLabel("reported_done")).toBe("reported done");
    expect(timeRange("2026-11-12T10:02:00Z", undefined, "en-GB")).toMatch(/^since \d\d:\d\d$/);
    expect(timeRange("2026-11-12T10:02:00Z", "2026-11-12T10:41:00Z", "en-GB")).toMatch(/^\d\d:\d\d – \d\d:\d\d$/);
    expect(timeRange("2026-11-10T10:02:00Z", "2026-11-12T10:41:00Z", "en-GB")).toMatch(/Nov/);
  });
  it("breaks tokens down with both cache writes together", () => {
    expect(tokenParts({ input: 10, output: 20, cache_read: 30, cache_write_5m: 4, cache_write_1h: 5 })).toEqual([
      { label: "input", value: 10 }, { label: "output", value: 20 }, { label: "cache write", value: 9 }, { label: "cache read", value: 30 },
    ]);
  });
  it("validates what the daemon sends", () => {
    expect(isAgentRun({ agent_id: "a", started_at: "x", working_seconds: 1, waiting_seconds: 2, sessions: [], reports: [] })).toBe(true);
    expect(isAgentRun({ agent_id: "a" })).toBe(false);
  });
});
