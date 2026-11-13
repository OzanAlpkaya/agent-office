// Run info of a task's agents (Phase 19): what the task modal shows about
// each agent's run. Pure, so Vitest covers it.
import type { Tokens } from "./api";

export interface CommitInfo {
  sha: string;
  subject: string; // agent-written: text only
  at: string;
}

export interface DiffStat {
  files: number;
  added: number;
  removed: number;
}

export interface SessionRun {
  id: string;
  started_at: string;
  ended_at?: string;
  resumed_from?: string;
  exit_reason?: string;
  turns: number;
  tool_calls: number;
}

export interface AgentRun {
  agent_id: string;
  name: string;
  kind: string;
  role: string;
  model?: string;
  cli: string;
  state: string;
  started_at: string;
  exited_at?: string;
  exit_reason?: string;
  working_seconds: number;
  waiting_seconds: number;
  branch: string;
  base?: string;
  last_commit?: CommitInfo;
  changes?: DiffStat;
  sessions: SessionRun[];
  reports: { summary: string; verification?: string; at: string }[];
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;

export function isAgentRun(v: unknown): v is AgentRun {
  return isObj(v) && typeof v.agent_id === "string" && typeof v.started_at === "string" &&
    typeof v.working_seconds === "number" && typeof v.waiting_seconds === "number" && Array.isArray(v.sessions) &&
    Array.isArray(v.reports);
}

// formatDuration is a short duration: 45s, 12m, 1h 05m.
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

// exitLabel turns an exit reason into words: reported_done → reported done.
export function exitLabel(reason: string | undefined): string {
  return reason ? reason.replaceAll("_", " ") : "";
}

// timeRange is "10:02 – 10:41" (or "since 10:02" while it runs), in the
// browser's time zone; dates are shown when the run spans days.
export function timeRange(start: string, end?: string, locale?: string): string {
  const a = new Date(start);
  const fmt = (d: Date, withDate: boolean): string => d.toLocaleString(locale, withDate
    ? { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }
    : { hour: "2-digit", minute: "2-digit" });
  if (!end) return `since ${fmt(a, false)}`;
  const b = new Date(end);
  const sameDay = a.toDateString() === b.toDateString();
  return `${fmt(a, !sameDay)} – ${fmt(b, !sameDay)}`;
}

// tokenParts is a run's token breakdown: input, output, cache write and read.
export function tokenParts(t: Tokens): { label: string; value: number }[] {
  return [
    { label: "input", value: t.input },
    { label: "output", value: t.output },
    { label: "cache write", value: t.cache_write_5m + t.cache_write_1h },
    { label: "cache read", value: t.cache_read },
  ];
}
