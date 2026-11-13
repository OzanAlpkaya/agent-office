// Agent and sub-task caps (Phase 19, ADR 0061, 0062): pure, so Vitest covers
// the texts the settings and the agent list show.
import type { Caps, ProjectSettings } from "./api";

// capNote explains the cap a start meets. A daemon-wide cap below the
// project's says so plainly (Phase 20): it limits every project together.
export function capNote(c: Caps): string {
  if (aboveDaemonCap(c)) {
    return `Effective: ${c.effective}. The daemon-wide cap of ${c.daemon_max} is below this project's ${c.max_agents}: ` +
      `at most ${c.daemon_max} agents run in all projects together. The Tech Lead, Architect and QA count too.`;
  }
  const why = c.max_agents === c.daemon_max
    ? `this project's and the daemon's cap are both ${c.effective}`
    : `the smaller of this project's ${c.max_agents} and the daemon's ${c.daemon_max}`;
  return `Effective: ${c.effective} (${why}). The Tech Lead, Architect and QA count too.`;
}

// aboveDaemonCap reports whether the project's Max agents is more than the
// daemon-wide cap lets run (Phase 20).
export function aboveDaemonCap(c: Pick<Caps, "max_agents" | "daemon_max">): boolean {
  return c.max_agents > c.daemon_max;
}

// draftCaps is the cap a project's settings draft would have: its Max agents,
// or the daemon's default for projects when the field is empty, against the
// daemon-wide cap. Undefined when the draft is not a number or nothing is known.
export function draftCaps(draft: string, projectDefault: number | undefined, daemonMax: number | undefined,
  running = 0): Caps | undefined {
  const t = draft.trim();
  if (daemonMax === undefined || (t !== "" && !/^\d+$/.test(t))) return undefined;
  const own = t === "" ? projectDefault : Number(t);
  if (own === undefined || own < 1) return undefined;
  return { max_agents: own, daemon_max: daemonMax, effective: Math.min(own, daemonMax), running, max_sub_tasks: 0 };
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
