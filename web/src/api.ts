// Types mirror internal/engine.AgentInfo and the API in internal/api.
// Written by hand for now (ADR 0005); keep them in sync with the Go structs.

export type AgentState = "starting" | "idle" | "working" | "waiting_permission" | "exited";

export interface Agent {
  id: string;
  session_id: string;
  state: AgentState;
  activity?: string;
  repo: string;
  model?: string;
  inherit_user_settings: boolean;
  started_at: string;
  exited_at?: string;
  exit_code?: number;
}

export interface Health {
  status: string;
  version: string;
}

export type EventMessage =
  | { type: "snapshot"; agents: Agent[] }
  | { type: "agent"; agent: Agent };

const states: readonly string[] = ["starting", "idle", "working", "waiting_permission", "exited"];

function isAgent(v: unknown): v is Agent {
  if (typeof v !== "object" || v === null) return false;
  const a = v as Record<string, unknown>;
  return typeof a.id === "string" && typeof a.session_id === "string" && typeof a.state === "string" &&
    states.includes(a.state) && typeof a.repo === "string" && typeof a.started_at === "string";
}

// parseEventMessage validates a message from /api/events; it returns undefined
// for anything that does not match the expected shape.
export function parseEventMessage(raw: string): EventMessage | undefined {
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (typeof v !== "object" || v === null) return undefined;
  const m = v as Record<string, unknown>;
  if (m.type === "snapshot" && Array.isArray(m.agents) && m.agents.every(isAgent)) {
    return { type: "snapshot", agents: m.agents };
  }
  if (m.type === "agent" && isAgent(m.agent)) {
    return { type: "agent", agent: m.agent };
  }
  return undefined;
}

async function request<T>(method: string, path: string): Promise<T> {
  const res = await fetch(path, { method, headers: { Accept: "application/json" } });
  if (!res.ok) {
    const body: unknown = await res.json().catch(() => undefined);
    const msg = typeof body === "object" && body !== null && "error" in body && typeof body.error === "string"
      ? body.error
      : `${method} ${path}: ${res.status}`;
    throw new Error(msg);
  }
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const api = {
  health: () => request<Health>("GET", "/api/health"),
  startAgent: () => request<Agent>("POST", "/api/agents"),
  stopAgent: (id: string) => request<Agent>("POST", `/api/agents/${id}/stop`),
  removeAgent: (id: string) => request<void>("DELETE", `/api/agents/${id}`),
};

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}
