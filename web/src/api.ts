// Types mirror internal/engine (AgentInfo, Task, Update, RemoveResult,
// ImportInfo, Status, Message, AccessRequest, PullRequest, Review), internal/sandbox (Event) and
// the API in internal/api.
// Written by hand for now (ADR 0005); keep them in sync.

export type AgentState = "starting" | "idle" | "working" | "waiting_permission" | "exited";
export type TaskStatus = "todo" | "in_progress" | "review" | "done" | "blocked";
export type WorktreeStatus = "active" | "kept" | "removed";
export type SandboxMode = "host" | "podman";
export type Role = "lead" | "worker" | "reviewer";

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
  role: Role;
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

// Stack pack of the daemon run (ADR 0023). Secrets are names and whether
// their file exists, never values.
export interface PackStatus {
  name: string;
  description: string;
  source: string; // "builtin" or the pack directory
  egress: string[];
  // present: the file exists and passes the checks; problem: why an existing file is unusable.
  secrets: { name: string; description?: string; required: boolean; present: boolean; problem?: string }[];
  skills: string[];
  mcp_servers: string[];
}

export interface Status {
  sandbox: SandboxMode;
  image?: string;
  egress?: string[]; // sandboxed agents' allowlist
  pack?: PackStatus;
  warnings: string[];
  github?: { repo: string; base: string; remote: string; reviewer: string; rounds: number };
}

// Review of a pull request (ADR 0021). Summary and comments are written by a
// reviewer agent, CodeRabbit or a person: render them as text only.
export interface Review {
  id: number;
  pr_id: number;
  reviewer: "internal" | "coderabbit" | "human";
  head: string;
  status: "queued" | "running" | "done" | "failed" | "timed_out";
  verdict?: "approve" | "changes_requested" | "comment";
  summary?: string;
  comments: { path: string; line: number; body: string }[];
  agent_id?: string;
  url?: string;
  followup?: "started" | "messaged" | "skipped" | "refused";
  error?: string;
  created_at: string;
  updated_at: string;
}

// PullRequest opened by the daemon for a worker's branch (ADR 0020).
export interface PullRequest {
  id: number;
  task_id?: string;
  agent_id: string;
  branch: string;
  base?: string;
  number?: number;
  url?: string;
  state: "pending" | "open" | "merged" | "closed";
  head?: string;
  checks?: "none" | "pending" | "success" | "failure" | "unavailable";
  failed_checks: string[];
  flagged: { commit: string; problem: string }[];
  rounds: number;
  error?: string;
  reviews: Review[];
  created_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  title: string;
  description: string;
  status: TaskStatus;
  parent_task_id?: string; // the goal of a sub-task
  created_by?: string; // agent id; absent: created by you
  created_at: string;
  updated_at: string;
}

// Message between agents (ADR 0018). from is absent for agent-office itself.
// The body is written by an agent: render it as text only.
export interface Message {
  id: number;
  from?: string;
  to: string;
  body: string;
  created_at: string;
  delivered_at?: string;
  delivery?: "stop_hook" | "pty";
}

// AccessRequest is an agent asking for more access (ADR 0019). The reason is
// written by the agent: render it as text only.
export interface AccessRequest {
  id: number;
  agent_id: string;
  kind: string;
  value: string;
  reason: string;
  status: "pending" | "approved" | "denied" | "expired";
  created_at: string;
  decided_at?: string;
  decided_by?: string;
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
  | { type: "snapshot"; agents: Agent[]; tasks: Task[]; accessRequests: AccessRequest[]; pullRequests: PullRequest[] }
  | { type: "agent"; agent: Agent }
  | { type: "agent_removed"; id: string }
  | { type: "task"; task: Task }
  | { type: "task_deleted"; id: string }
  | { type: "sandbox_event"; agent_id: string; event: SandboxEvent }
  | { type: "message"; message: Message }
  | { type: "access_request"; request: AccessRequest }
  | { type: "pull_request"; pullRequest: PullRequest };

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null;

function isImport(v: unknown): v is ImportInfo {
  return isObj(v) && typeof v.trigger === "string" && typeof v.outcome === "string" &&
    typeof v.commits === "number" && typeof v.at === "string" && (v.flagged === null || Array.isArray(v.flagged));
}

function isAgent(v: unknown): v is Agent {
  return isObj(v) && typeof v.id === "string" && typeof v.state === "string" && agentStates.includes(v.state) &&
    (v.sandbox === "host" || v.sandbox === "podman") && (v.role === "lead" || v.role === "worker" || v.role === "reviewer") &&
    typeof v.branch === "string" && typeof v.worktree_path === "string" && typeof v.has_terminal === "boolean" &&
    typeof v.started_at === "string" && (v.last_import === undefined || isImport(v.last_import));
}

export function isSandboxEvent(v: unknown): v is SandboxEvent {
  return isObj(v) && typeof v.kind === "string" && typeof v.detail === "string" && typeof v.at === "string";
}

export function isMessage(v: unknown): v is Message {
  return isObj(v) && typeof v.id === "number" && typeof v.to === "string" && typeof v.body === "string" &&
    typeof v.created_at === "string" && (v.from === undefined || typeof v.from === "string");
}

const accessStatuses: readonly string[] = ["pending", "approved", "denied", "expired"];

function isAccessRequest(v: unknown): v is AccessRequest {
  return isObj(v) && typeof v.id === "number" && typeof v.agent_id === "string" && typeof v.kind === "string" &&
    typeof v.value === "string" && typeof v.reason === "string" && typeof v.status === "string" &&
    accessStatuses.includes(v.status) && typeof v.created_at === "string";
}

function isReview(v: unknown): v is Review {
  return isObj(v) && typeof v.id === "number" && typeof v.reviewer === "string" && typeof v.status === "string" &&
    typeof v.head === "string" && Array.isArray(v.comments) &&
    v.comments.every((c) => isObj(c) && typeof c.path === "string" && typeof c.line === "number" && typeof c.body === "string");
}

const prStates: readonly string[] = ["pending", "open", "merged", "closed"];

export function isPullRequest(v: unknown): v is PullRequest {
  return isObj(v) && typeof v.id === "number" && typeof v.agent_id === "string" && typeof v.branch === "string" &&
    typeof v.state === "string" && prStates.includes(v.state) && Array.isArray(v.failed_checks) &&
    Array.isArray(v.flagged) && typeof v.rounds === "number" && Array.isArray(v.reviews) && v.reviews.every(isReview);
}

function isTask(v: unknown): v is Task {
  return isObj(v) && typeof v.id === "string" && typeof v.title === "string" &&
    typeof v.description === "string" && typeof v.status === "string" &&
    (taskStatuses as readonly string[]).includes(v.status) &&
    (v.parent_task_id === undefined || typeof v.parent_task_id === "string");
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
      return Array.isArray(m.agents) && m.agents.every(isAgent) && Array.isArray(m.tasks) && m.tasks.every(isTask) &&
        Array.isArray(m.access_requests) && m.access_requests.every(isAccessRequest) &&
        Array.isArray(m.pull_requests) && m.pull_requests.every(isPullRequest)
        ? { type: "snapshot", agents: m.agents, tasks: m.tasks, accessRequests: m.access_requests, pullRequests: m.pull_requests }
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
    case "message":
      return isMessage(m.message) ? { type: "message", message: m.message } : undefined;
    case "access_request":
      return isAccessRequest(m.access_request) ? { type: "access_request", request: m.access_request } : undefined;
    case "pull_request":
      return isPullRequest(m.pull_request) ? { type: "pull_request", pullRequest: m.pull_request } : undefined;
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
  startAgent: (taskId?: string, role: Role = "worker"): Promise<Agent> =>
    request("POST", "/api/agents", taskId ? { task_id: taskId, role } : undefined),
  stopAgent: (id: string): Promise<Agent> => request("POST", `/api/agents/${id}/stop`),
  removeAgent: (id: string): Promise<RemoveResult> => request("DELETE", `/api/agents/${id}`),
  discardAgent: (id: string): Promise<RemoveResult> => request("POST", `/api/agents/${id}/discard`),
  importAgent: (id: string): Promise<ImportInfo> => request("POST", `/api/agents/${id}/import`),
  saveWork: (id: string): Promise<ImportInfo> => request("POST", `/api/agents/${id}/save-work`),
  agentEvents: async (id: string): Promise<SandboxEvent[]> => {
    const evs: unknown = await request("GET", `/api/agents/${id}/events`);
    return Array.isArray(evs) ? evs.filter(isSandboxEvent) : [];
  },
  agentMessages: async (id: string): Promise<Message[]> => {
    const msgs: unknown = await request("GET", `/api/agents/${id}/messages`);
    return Array.isArray(msgs) ? msgs.filter(isMessage) : [];
  },
  approveAccess: (id: number): Promise<AccessRequest> => request("POST", `/api/access-requests/${id}/approve`),
  denyAccess: (id: number): Promise<AccessRequest> => request("POST", `/api/access-requests/${id}/deny`),
  createTask: (title: string, description: string): Promise<Task> =>
    request("POST", "/api/tasks", { title, description }),
  updateTask: (id: string, patch: Partial<Pick<Task, "title" | "description" | "status">>): Promise<Task> =>
    request("PATCH", `/api/tasks/${id}`, patch),
  deleteTask: (id: string): Promise<void> => request("DELETE", `/api/tasks/${id}`),
  openPullRequest: (taskId: string): Promise<PullRequest> => request("POST", `/api/tasks/${taskId}/pull-request`),
  requestReview: (prId: number): Promise<PullRequest> => request("POST", `/api/pull-requests/${prId}/review`),
  sendToWorker: (prId: number, reviewId: number): Promise<PullRequest> =>
    request("POST", `/api/pull-requests/${prId}/follow-up`, { review_id: reviewId }),
  acceptFlagged: (prId: number, commits: string[]): Promise<PullRequest> =>
    request("POST", `/api/pull-requests/${prId}/accept`, { commits }),
};

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}
