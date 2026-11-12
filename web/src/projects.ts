// Several projects in one UI (ADR 0046, 0048): which project is shown, and
// what the other projects need for the Approvals badge. Pure.
import type { AccessRequest, Agent, EventMessage, InboxItem, Project, ProjectsMessage, Spec } from "./api";
import { type Waiting, waitingOnUser } from "./waiting";

// LightWorld is what decides "waiting on the user" (ADR 0044) for a project
// that is not shown: the four inputs of waitingOnUser.
export interface LightWorld {
  agents: Agent[];
  accessRequests: AccessRequest[];
  inbox: InboxItem[];
  specs: Spec[];
}

export const emptyLight: LightWorld = { agents: [], accessRequests: [], inbox: [], specs: [] };

function upsertBy<T, K>(list: readonly T[], item: T, key: (x: T) => K): T[] {
  const k = key(item);
  const i = list.findIndex((x) => key(x) === k);
  if (i === -1) return [...list, item];
  const next = list.slice();
  next[i] = item;
  return next;
}

// reduceLight applies one message of a project's events stream; every other
// message type leaves the light world as it is.
export function reduceLight(w: LightWorld, m: EventMessage): LightWorld {
  switch (m.type) {
    case "snapshot":
      return { agents: m.agents, accessRequests: m.accessRequests, inbox: m.inbox, specs: m.specs };
    case "agent":
      return { ...w, agents: upsertBy(w.agents, m.agent, (a) => a.id) };
    case "agent_removed":
      return { ...w, agents: w.agents.filter((a) => a.id !== m.id) };
    case "access_request":
      return { ...w, accessRequests: upsertBy(w.accessRequests, m.request, (r) => r.id) };
    case "inbox":
      return { ...w, inbox: upsertBy(w.inbox, m.item, (i) => i.id) };
    case "spec":
      return { ...w, specs: upsertBy(w.specs, m.spec, (s) => s.id) };
    default:
      return w;
  }
}

// applyProjects keeps the project list in sync with the daemon's /api/events.
export function applyProjects(list: readonly Project[], m: ProjectsMessage): Project[] {
  switch (m.type) {
    case "projects":
      return m.projects;
    case "project":
      return upsertBy(list, m.project, (p) => p.id);
    case "project_removed":
      return list.filter((p) => p.id !== m.id);
    case "presence":
    case "profile":
      return list as Project[];
  }
}

// pickProject is the project the UI shows: the one this browser chose last,
// else the daemon's default (`serve --repo`), else the first that is open,
// else the first.
export function pickProject(projects: readonly Project[], chosen: string | undefined, fallback?: string): Project | undefined {
  return projects.find((p) => p.id === chosen) ?? projects.find((p) => p.id === fallback) ??
    projects.find((p) => p.state === "open") ?? projects[0];
}

// Elsewhere is another project's share of the Approvals badge.
export interface Elsewhere {
  project: Project;
  waiting: Waiting[];
  running: number; // agents that have not exited
}

// elsewhere lists every project but the shown one with what waits there,
// in the project list's order.
export function elsewhere(projects: readonly Project[], shown: string | undefined,
  worlds: Readonly<Record<string, LightWorld>>): Elsewhere[] {
  return projects.filter((p) => p.id !== shown).map((p) => {
    const w = worlds[p.id] ?? emptyLight;
    return { project: p, waiting: waitingOnUser(w.agents, w.accessRequests, w.inbox, w.specs),
      running: w.agents.filter((a) => a.state !== "exited").length };
  });
}

// waitingTotal is the Approvals badge: the shown project's waits plus every
// other project's (each counts agents, ADR 0044).
export function waitingTotal(shown: number, others: readonly Elsewhere[]): number {
  return others.reduce((n, e) => n + e.waiting.length, shown);
}
