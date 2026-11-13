// Agent and sub-task caps (Phase 19, ADR 0061, 0062): pure, so Vitest covers
// the texts the settings and the agent list show.
import type { Caps, ProjectSettings } from "./api";

// capNote explains the cap a start meets.
export function capNote(c: Caps): string {
  const why = c.max_agents === c.daemon_max
    ? `this project's and the daemon's cap are both ${c.effective}`
    : c.max_agents < c.daemon_max
      ? `the smaller of this project's ${c.max_agents} and the daemon's ${c.daemon_max}`
      : `the daemon's cap of ${c.daemon_max} is below this project's ${c.max_agents}`;
  return `Effective: ${c.effective} (${why}). The Tech Lead, Architect and QA count too.`;
}

// runningLabel is the agent list's header count, e.g. "3 / 4 running".
export function runningLabel(c: Caps): string {
  return `${c.running} / ${c.effective} running`;
}

// Settings that change what a project's engine is built from: changing them
// needs its agents stopped. Everything else applies to the next agent start.
export const reopenSettings: readonly (keyof ProjectSettings)[] = [
  "pack", "secrets_dir", "egress_allow", "github", "github_remote", "reviewer", "review_rounds", "role_clis",
];

// needsStop lists the settings in next that differ from cur and need the
// project's agents stopped, as the daemon decides it (ADR 0061).
export function needsStop(cur: ProjectSettings, next: ProjectSettings): (keyof ProjectSettings)[] {
  return reopenSettings.filter((k) => JSON.stringify(cur[k] ?? null) !== JSON.stringify(next[k] ?? null));
}
