// Types mirror internal/engine (AgentInfo, Task, Update, RemoveResult,
// ImportInfo, Status), internal/sandbox (Event) and the API in internal/api.
// Written by hand for now (ADR 0005); keep them in sync.

export type AgentState = "starting" | "idle" | "working" | "waiting_permission" | "exited";
export type TaskStatus = "todo" | "in_progress" | "review" | "done" | "blocked";
export type WorktreeStatus = "active" | "kept" | "removed";
export type SandboxMode = "host" | "podman";

export const taskStatuses: readonly TaskStatus[] = ["todo", "in_progress", "review", "done", "blocked"];
const agentStates: readonly string[] = ["starting", "idle", "working", "waiting_permission", "exited"];

export interface Agent {
  id: string;
  session_id?: string;
  task_id?: string;
  state: AgentState;
  activity?: string;
  model?: string;
  inherit_user_settings: boolean;
  sandbox: SandboxMode;
  branch: string;
  worktree_path: string;
  workspace_kind: "worktree" | "clone";
  worktree_status: WorktreeStatus;
  has_terminal: boolean;
  started_at: string; // UTC ISO 8601
  exited_at?: string;
  exit_code?: number;
  exit_reason?: string;
  last_import?: ImportInfo; // sandboxed agents
}

// ImportInfo is one import of a sandboxed agent's branch into the repository.
export interface ImportInfo {
  trigger: string; // exit | manual | restart | remove | save_work
  outcome: string; // no_commits | created | fast_forward | up_to_date | diverged
  branch?: string;
  head?: string;
  previous?: string;
  commits: number;
  flagged: { commit: string; problem: string }[] | null;
  at: string;
}

// SandboxEvent is a proxy refusal or an import problem. The detail comes from
// the agent (sanitized by the daemon); render it as text only.
export interface SandboxEvent {
  kind: string;
  detail: string;
  at: string;
}

export interface Status {
  sandbox: SandboxMode;
  image?: string;
  warnings: string[];
}

export interface Task {
  id: string;
  title: string;
  description: string;
  status: TaskStatus;
  created_at: string;
  updated_at: string;
}

export interface Project {
  id: string;
  name: string;
  repo_path: string;
  slug: string;
}

export interface Health {
  status: string;
  version: string;
}

export interface RemoveResult {
  worktree: "removed" | "kept";
  branch: "deleted" | "kept";
}

export type EventMessage =
  | { type: "snapshot"; agents: Agent[]; tasks: Task[] }
  | { type: "agent"; agent: Agent }
  | { type: "agent_removed"; id: string }
  | { type: "task"; task: Task }
  | { type: "task_deleted"; id: string }
  | { type: "sandbox_event"; agent_id: string; event: SandboxEvent };

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null;

function isImport(v: unknown): v is ImportInfo {
  return isObj(v) && typeof v.trigger === "string" && typeof v.outcome === "string" &&
    typeof v.commits === "number" && typeof v.at === "string" && (v.flagged === null || Array.isArray(v.flagged));
}

function isAgent(v: unknown): v is Agent {
  return isObj(v) && typeof v.id === "string" && typeof v.state === "string" && agentStates.includes(v.state) &&
    (v.sandbox === "host" || v.sandbox === "podman") &&
    typeof v.branch === "string" && typeof v.worktree_path === "string" && typeof v.has_terminal === "boolean" &&
    typeof v.started_at === "string" && (v.last_import === undefined || isImport(v.last_import));
}

export function isSandboxEvent(v: unknown): v is SandboxEvent {
  return isObj(v) && typeof v.kind === "string" && typeof v.detail === "string" && typeof v.at === "string";
}

function isTask(v: unknown): v is Task {
  return isObj(v) && typeof v.id === "string" && typeof v.title === "string" &&
    typeof v.description === "string" && typeof v.status === "string" &&
    (taskStatuses as readonly string[]).includes(v.status);
}

// parseEventMessage validates a message from /api/events; it returns undefined
// for anything that does not match the expected shape.
export function parseEventMessage(raw: string): EventMessage | undefined {
  let m: unknown;
  try {
    m = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (!isObj(m)) return undefined;
  switch (m.type) {
    case "snapshot":
      return Array.isArray(m.agents) && m.agents.every(isAgent) && Array.isArray(m.tasks) && m.tasks.every(isTask)
        ? { type: "snapshot", agents: m.agents, tasks: m.tasks }
        : undefined;
    case "agent":
      return isAgent(m.agent) ? { type: "agent", agent: m.agent } : undefined;
    case "task":
      return isTask(m.task) ? { type: "task", task: m.task } : undefined;
    case "agent_removed":
    case "task_deleted":
      return typeof m.id === "string" ? { type: m.type, id: m.id } : undefined;
    case "sandbox_event": {
      const se = m.sandbox_event;
      return isObj(se) && typeof se.agent_id === "string" && isSandboxEvent(se.event)
        ? { type: "sandbox_event", agent_id: se.agent_id, event: se.event }
        : undefined;
    }
    default:
      return undefined;
  }
}

// ApiError carries the HTTP status and the parsed error body.
export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly body: unknown) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, headers: { Accept: "application/json" } };
  if (body !== undefined) {
    init.headers = { Accept: "application/json", "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (!res.ok) {
    const data: unknown = await res.json().catch(() => undefined);
    const msg = isObj(data) && typeof data.error === "string" ? data.error : `${method} ${path}: ${res.status}`;
    throw new ApiError(msg, res.status, data);
  }
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const api = {
  health: (): Promise<Health> => request("GET", "/api/health"),
  project: (): Promise<Project> => request("GET", "/api/project"),
  status: (): Promise<Status> => request("GET", "/api/status"),
  startAgent: (taskId?: string): Promise<Agent> =>
    request("POST", "/api/agents", taskId ? { task_id: taskId } : undefined),
  stopAgent: (id: string): Promise<Agent> => request("POST", `/api/agents/${id}/stop`),
  removeAgent: (id: string): Promise<RemoveResult> => request("DELETE", `/api/agents/${id}`),
  discardAgent: (id: string): Promise<RemoveResult> => request("POST", `/api/agents/${id}/discard`),
  importAgent: (id: string): Promise<ImportInfo> => request("POST", `/api/agents/${id}/import`),
  saveWork: (id: string): Promise<ImportInfo> => request("POST", `/api/agents/${id}/save-work`),
  agentEvents: async (id: string): Promise<SandboxEvent[]> => {
    const evs: unknown = await request("GET", `/api/agents/${id}/events`);
    return Array.isArray(evs) ? evs.filter(isSandboxEvent) : [];
  },
  createTask: (title: string, description: string): Promise<Task> =>
    request("POST", "/api/tasks", { title, description }),
  updateTask: (id: string, patch: Partial<Pick<Task, "title" | "description" | "status">>): Promise<Task> =>
    request("PATCH", `/api/tasks/${id}`, patch),
  deleteTask: (id: string): Promise<void> => request("DELETE", `/api/tasks/${id}`),
};

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}
