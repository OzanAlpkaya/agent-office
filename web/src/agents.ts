// Agent naming, open terminal tabs and the agent switcher's search and
// grouping (ADR 0043, 0044). Pure, so the UI and the tests share it.
import type { Agent, Task } from "./api";

// shortId is how an agent's id is shown next to its name.
export const shortId = (id: string): string => id.slice(0, 6);

// agentLabel is an agent's first name, or its short id for an agent the
// daemon has not named (one from an older daemon) or does not know.
export function agentLabel(a: Pick<Agent, "id" | "name"> | undefined, id?: string): string {
  if (a?.name) return a.name;
  return shortId(a?.id ?? id ?? "");
}

// labelOf finds the agent with this id and labels it.
export function labelOf(agents: readonly Agent[], id: string): string {
  return agentLabel(agents.find((a) => a.id === id), id);
}

// ---------------------------------------------------------------- open tabs

// Tabs are the agents the user opened, in the order shown. used lists the same
// ids from least to most recently used, to pick the tab a new one replaces.
export interface Tabs {
  readonly order: readonly string[];
  readonly used: readonly string[];
}

export const noTabs: Tabs = { order: [], used: [] };

// maxTabs keeps the strip readable; the switcher reaches every other agent.
export const maxTabs = 6;

// openTab shows id as a tab and marks it used. When the strip is full, the
// least recently used other tab closes.
export function openTab(t: Tabs, id: string, max = maxTabs): Tabs {
  const used = [...t.used.filter((x) => x !== id), id];
  let order = t.order.includes(id) ? [...t.order] : [...t.order, id];
  while (order.length > max) {
    const victim = used.find((x) => x !== id && order.includes(x));
    if (victim === undefined) break;
    order = order.filter((x) => x !== victim);
    used.splice(used.indexOf(victim), 1);
  }
  return { order, used };
}

export function closeTab(t: Tabs, id: string): Tabs {
  return { order: t.order.filter((x) => x !== id), used: t.used.filter((x) => x !== id) };
}

// neighbourTab is the tab to show after closing id: the one to its right, or
// else to its left.
export function neighbourTab(t: Tabs, id: string): string | undefined {
  const i = t.order.indexOf(id);
  if (i === -1) return undefined;
  return t.order[i + 1] ?? t.order[i - 1];
}

// moveTab moves id to position to (clamped); the other tabs keep their order.
export function moveTab(t: Tabs, id: string, to: number): Tabs {
  if (!t.order.includes(id)) return t;
  const rest = t.order.filter((x) => x !== id);
  const at = Math.max(0, Math.min(rest.length, to));
  return { ...t, order: [...rest.slice(0, at), id, ...rest.slice(at)] };
}

// keepTabs drops tabs of agents that no longer exist.
export function keepTabs(t: Tabs, ids: ReadonlySet<string>): Tabs {
  if (t.order.every((x) => ids.has(x)) && t.used.every((x) => ids.has(x))) return t;
  return { order: t.order.filter((x) => ids.has(x)), used: t.used.filter((x) => ids.has(x)) };
}

// parseTabs reads stored tabs, ignoring anything malformed.
export function parseTabs(raw: string | null): Tabs {
  if (!raw) return noTabs;
  try {
    const v: unknown = JSON.parse(raw);
    if (typeof v !== "object" || v === null) return noTabs;
    const list = (x: unknown): string[] =>
      Array.isArray(x) ? x.filter((s): s is string => typeof s === "string" && s.length > 0 && s.length <= 64) : [];
    const { order, used } = v as { order?: unknown; used?: unknown };
    const o = [...new Set(list(order))].slice(0, maxTabs);
    return { order: o, used: [...new Set(list(used))].filter((x) => o.includes(x)) };
  } catch {
    return noTabs; // stored by an older or broken page: start empty
  }
}

// ---------------------------------------------------------------- switcher

export interface AgentGroup {
  key: string; // a goal's task id, "none" or "exited"
  title: string;
  agents: Agent[];
}

// The coordinating roles first within a goal, then workers in start order.
const kindRank: Record<Agent["kind"], number> = { architect: 0, lead: 1, qa: 2, assistant: 3, worker: 4, reviewer: 5 };

const byStart = (x: Agent, y: Agent): number =>
  Date.parse(x.started_at) - Date.parse(y.started_at) || (x.id < y.id ? -1 : x.id > y.id ? 1 : 0);

// goalOf is the top-level task an agent works for: its task's parent, or its
// own task.
export function goalOf(a: Agent, tasks: readonly Task[]): Task | undefined {
  const t = tasks.find((x) => x.id === a.task_id);
  if (!t) return undefined;
  return t.parent_task_id ? (tasks.find((x) => x.id === t.parent_task_id) ?? t) : t;
}

// groupAgents groups agents that are not exited by goal (goals in the order
// they were created, then agents without a task), and puts exited agents in
// one group at the end, most recently exited first.
export function groupAgents(agents: readonly Agent[], tasks: readonly Task[]): AgentGroup[] {
  const groups = new Map<string, AgentGroup & { created: number }>();
  const exited: Agent[] = [];
  for (const a of agents) {
    if (a.state === "exited") {
      exited.push(a);
      continue;
    }
    const g = goalOf(a, tasks);
    const key = g?.id ?? "none";
    let group = groups.get(key);
    if (!group) {
      group = { key, title: g?.title ?? "No task", agents: [], created: g ? Date.parse(g.created_at) : Infinity };
      groups.set(key, group);
    }
    group.agents.push(a);
  }
  const out: AgentGroup[] = [...groups.values()]
    .sort((x, y) => x.created - y.created || (x.key < y.key ? -1 : 1))
    .map(({ key, title, agents: list }) => ({
      key, title, agents: list.sort((x, y) => kindRank[x.kind] - kindRank[y.kind] || byStart(x, y)),
    }));
  if (exited.length > 0) {
    const ended = (a: Agent): number => Date.parse(a.exited_at ?? a.started_at);
    out.push({ key: "exited", title: "Exited", agents: exited.sort((x, y) => ended(y) - ended(x) || byStart(x, y)) });
  }
  return out;
}

// matchAgent says whether every word of the query appears in the agent's
// name, id, role, task, goal or branch (case-insensitive).
export function matchAgent(a: Agent, tasks: readonly Task[], query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const task = tasks.find((t) => t.id === a.task_id);
  const hay = [a.name ?? "", a.id, a.role, a.kind, task?.title ?? "", goalOf(a, tasks)?.title ?? "", a.branch]
    .join("\n").toLowerCase();
  return words.every((w) => hay.includes(w));
}

// searchAgents filters and groups agents for the switcher.
export function searchAgents(agents: readonly Agent[], tasks: readonly Task[], query: string): AgentGroup[] {
  return groupAgents(agents.filter((a) => matchAgent(a, tasks, query)), tasks);
}
