// Types mirror internal/engine (AgentInfo, Task, Update, RemoveResult,
// ImportInfo, Status, Message, AccessRequest, PullRequest, Review, UsageReport, SessionUsage,
// InboxItem, Spec, QARun, MemoryEntry, RoleInfo, ApproveResult), internal/mcp (Plan),
// internal/sandbox (Event), internal/usage (Tokens) and the API in internal/api.
// Written by hand for now (ADR 0005); keep them in sync.

export type AgentState = "starting" | "idle" | "working" | "waiting_permission" | "exited";
export type TaskStatus = "todo" | "in_progress" | "review" | "done" | "blocked";
export type WorktreeStatus = "active" | "kept" | "removed";
export type SandboxMode = "host" | "podman";
// Kind is an agent's behaviour, fixed by agent-office (ADR 0029); Role is the
// name of its role, which is data (e.g. tech-lead, worker, docs).
export type Kind = "architect" | "lead" | "worker" | "qa" | "reviewer" | "assistant";
export type Role = string;
export const kinds: readonly Kind[] = ["architect", "lead", "worker", "qa", "reviewer", "assistant"];

// defaultRole is the built-in role agent-office uses for a kind.
export const defaultRole: Record<Kind, Role> = {
  architect: "architect", lead: "tech-lead", worker: "worker", qa: "qa", reviewer: "reviewer", assistant: "assistant",
};

export const taskStatuses: readonly TaskStatus[] = ["todo", "in_progress", "review", "done", "blocked"];
const agentStates: readonly string[] = ["starting", "idle", "working", "waiting_permission", "exited"];

// AgentCLI is the agent CLI an agent runs on (ADR 0050).
export type AgentCLI = "claude" | "codex";

export interface Agent {
  id: string; // the key everywhere: API, logs, branch names
  cli?: AgentCLI; // absent from daemons before Phase 15: claude
  name: string; // a first name for people, unique among listed agents (ADR 0043)
  session_id?: string;
  task_id?: string;
  state: AgentState;
  activity?: string;
  model?: string;
  inherit_user_settings: boolean;
  sandbox: SandboxMode;
  kind: Kind;
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
  exit_signal?: string; // the signal that ended it, e.g. KILL (ADR 0051)
  oom_killed?: boolean; // the out-of-memory killer ended it
  exit_detail?: string; // why it ended, in plain words (agent-office's text)
  last_import?: ImportInfo; // sandboxed agents
  resumable: boolean; // exited and can continue its conversation (ADR 0035)
  resumed_from?: string; // the session this one continues
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
  worker_memory?: string; // sandboxed workers' memory limit (ADR 0051)
  worker_pids?: number; // sandboxed workers' process limit (Phase 17)
  pack?: PackStatus;
  warnings: string[];
  github?: { repo: string; base: string; remote: string; reviewer: string; rounds: number };
  prices?: PriceInfo;
  budgets?: Budgets;
  qa?: boolean; // goals need a passing QA run (ADR 0033)
  remote_hosts?: string[]; // tailnet names tailscale serve forwards to the daemon (ADR 0038)
}

// Paired devices (ADR 0039). A device never carries its token.
export interface Device {
  id: string;
  name: string;
  kind: "browser" | "token";
  paired_via: string; // "local" or "tailscale:<login>"
  created_at: string;
  last_seen_at: string;
  current?: boolean; // the device making the request
}

export interface AuthSession {
  authenticated: boolean;
  device?: Device;
  remote: boolean; // the request came through tailscale serve
}

export interface PairingCode {
  code: string; // ABCD-EFGH
  expires_at: string;
  remote_urls: string[];
}

// unauthorizedEvent is dispatched on window when the daemon refuses this
// browser (401, or a WebSocket closed as signed out); the app then shows its
// pairing screen.
export const unauthorizedEvent = "agent-office:unauthorized";

// StatusSignedOut is the WebSocket close code of a revoked device.
export const statusSignedOut = 4401;

export function signalUnauthorized(): void {
  window.dispatchEvent(new Event(unauthorizedEvent));
}

// Token usage (ADR 0026). Money is integer micro-USD (1 USD = 1 000 000), never a float.
export interface Tokens {
  input: number;
  output: number;
  cache_read: number;
  cache_write_5m: number;
  cache_write_1h: number;
}

export interface PriceInfo {
  as_of: string;
  source: string;
  file?: string; // the user's price file merged in
}

// Budgets in micro-USD; 0 is off. Enforced for sandboxed agents only (ADR 0027).
export interface Budgets {
  session_micro_usd: number;
  goal_micro_usd: number;
}

// SessionUsage is one agent session's usage; live updates send the same shape.
// SessionInfo is one of an agent's sessions, for its history (ADR 0036).
export interface SessionInfo {
  id: string;
  started_at: string;
  ended_at?: string;
  resumed_from?: string;
  turns: number;
  tool_calls: number;
  live: boolean; // the running process's session
  terminal: boolean; // its terminal output was saved (or is live)
}

export function isSessionInfo(v: unknown): v is SessionInfo {
  return isObj(v) && typeof v.id === "string" && typeof v.started_at === "string" && typeof v.live === "boolean" &&
    typeof v.terminal === "boolean" && typeof v.turns === "number";
}

export interface SessionUsage {
  session_id: string;
  agent_id: string;
  cli?: AgentCLI;
  task_id?: string;
  goal_id?: string;
  kind: Kind;
  role: Role;
  sandbox: SandboxMode;
  source: "api" | "transcript";
  started_at: string;
  ended_at?: string;
  turns: number;
  tool_calls: number;
  messages: number;
  tokens: Tokens;
  cost_micro_usd: number; // priced messages only
  unpriced_tokens: number; // tokens of models without a price: cost unknown, not zero
  cli_cost_micro_usd?: number; // Claude Code's own figure (host mode)
  models: string[];
}

// VendorUsage is one vendor's share of a project's usage (ADR 0050).
export interface VendorUsage {
  vendor: string; // anthropic (Claude Code) | openai (Codex)
  sessions: number;
  messages: number;
  tokens: Tokens;
  cost_micro_usd: number;
  unpriced_tokens: number;
}

export interface ModelUsage {
  model: string;
  priced: boolean;
  messages: number;
  tokens: Tokens;
  cost_micro_usd: number;
}

export interface UsageReport {
  prices: PriceInfo;
  budgets: Budgets;
  totals: { sessions: number; messages: number; tokens: Tokens; cost_micro_usd: number; unpriced_tokens: number };
  sessions: SessionUsage[];
  models: ModelUsage[];
  vendors?: VendorUsage[]; // absent from daemons before Phase 15
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
  // The worker's "no change needed" answer to this review (agent-written: render as text) and,
  // once posted, the comment on GitHub.
  reply?: string;
  reply_url?: string;
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
  // A sub-task's plan (ADR 0032). Absent in older fixtures: treat as empty.
  depends_on?: string[];
  files?: string[];
  acceptance?: string[];
  role?: string;
  spec_key?: string;
  conflicts?: Conflict[];
  conflicts_checked_at?: string;
}

// Conflict: files that would not merge (ADR 0032); with absent = the base branch.
export interface Conflict {
  with?: string;
  branch: string;
  files: string[];
}

// RoleInfo is a team role (ADR 0029); the prompt stays in the role file.
export interface RoleInfo {
  name: string;
  description: string;
  behaviour: Kind;
  model: string; // the model it runs with; "" = the CLI's default
  cli?: AgentCLI; // absent from daemons before Phase 15: claude
  reports_to: string;
  read_only: boolean;
  team_tools: string[];
  deny: string[] | null;
  source: string;
}

// InboxItem is a message to the user (ADR 0030); agent_id absent = agent-office.
// The body is agent-written: render it as text only.
export interface InboxItem {
  id: number;
  agent_id?: string;
  task_id?: string;
  goal_id?: string;
  role?: string;
  kind: "question" | "info" | "blocked" | "briefing" | "plan";
  body: string;
  created_at: string;
  read_at?: string;
  answered_at?: string;
  reply?: string;
}

// The architect's plan (ADR 0031); its text is agent- or user-written.
export interface PlanSubTask {
  key: string;
  title: string;
  description: string;
  role?: string;
  depends_on?: string[];
  files?: string[];
  acceptance?: string[];
}

export interface Plan {
  summary: string;
  design: string;
  sub_tasks: PlanSubTask[];
  risks?: string;
}

export interface Spec {
  id: number;
  goal_id: string;
  version: number;
  status: "proposed" | "approved" | "superseded";
  author?: string; // the architect; absent: you
  plan: Plan;
  markdown: string;
  branch?: string;
  commit?: string;
  path?: string;
  created_at: string;
  approved_at?: string;
}

export interface ApproveResult {
  spec: Spec;
  created: string[];
  updated: string[];
  deleted: string[];
  kept: string[];
  lead?: Agent;
  lead_error?: string;
}

// QARun is one QA run of a goal (ADR 0033); summary and bugs are agent-written.
export interface QARun {
  id: number;
  goal_id: string;
  agent_id?: string;
  status: "running" | "passed" | "failed" | "error";
  branch: string;
  head?: string;
  merged: { task_id: string; title: string; branch: string; head: string }[];
  summary?: string;
  bugs: { title: string; description: string; task_id?: string; bug_task_id?: string }[];
  error?: string;
  created_at: string;
  updated_at: string;
}

// GoalMerge is the user's squash merge of a goal into the target branch, and
// its push (ADR 0055). message is the confirmed commit message.
export interface GoalMerge {
  id: number;
  goal_id: string;
  qa_run_id?: number;
  target: string;
  source_head: string;
  target_before: string;
  commit: string;
  message: string;
  created_at: string;
  pushed_at?: string;
  push_remote?: string;
}

export function isGoalMerge(v: unknown): v is GoalMerge {
  return isObj(v) && typeof v.id === "number" && typeof v.goal_id === "string" && typeof v.target === "string" &&
    typeof v.commit === "string" && typeof v.message === "string" && typeof v.created_at === "string";
}

// MergePreview is what the Merge confirmation shows; ready false comes with
// the reason the merge would be refused.
export interface MergePreview {
  goal_id: string;
  qa_run_id?: number;
  branch: string;
  source_head: string;
  target: string;
  target_head: string;
  message: string; // the Tech Lead's (agent-written): render as text
  files: number;
  paths: string[]; // the changed paths (at most 200), agent-chosen names: render as text
  sensitive: string[]; // changed paths that run code on CI or in hooks
  untested: number; // target commits QA did not test with
  checked_out: boolean;
  ready: boolean;
  reason?: string;
  merged?: GoalMerge;
  remote: string;
  remote_url?: string;
}

// GoalAgent is an agent that worked on a goal, removed ones included (ADR 0054).
export interface GoalAgent {
  id: string;
  name?: string;
  role: string;
  kind: string;
  cli: string;
  model?: string;
  task_id?: string;
  branch: string;
  state: string;
  exit_reason?: string;
  exit_detail?: string;
  started_at: string;
  exited_at?: string;
  removed: boolean;
}

// GoalRecord is what the archive shows of a goal. Report text is the Tech Lead's.
export interface GoalRecord {
  goal: Task;
  sub_tasks: Task[];
  agents: GoalAgent[];
  qa_runs: QARun[];
  report?: { agent_id?: string; summary: string; commit_message?: string; created_at: string };
  merges: GoalMerge[];
}

export interface GoalCleanup {
  removed: { agent_id: string; name?: string; branch?: string }[];
  kept: { agent_id: string; name?: string; reason?: string }[];
}

// MemoryEntry is a project memory entry (ADR 0034); a proposal's text is the assistant's.
export interface MemoryEntry {
  id: number;
  kind: "decision" | "convention" | "lesson";
  title: string;
  body: string;
  status: "proposed" | "accepted";
  source?: string;
  roles?: string[]; // only agents of these roles get it; empty: every role (ADR 0037)
  paths?: string[]; // only agents whose task touches these files; empty: every task
  created_at: string;
  updated_at: string;
}

// MemoryDraft is what the user writes or changes in a memory entry.
export type MemoryDraft = Pick<MemoryEntry, "kind" | "title" | "body"> & { roles: string[]; paths: string[] };

// Message between agents (ADR 0018). from is absent for agent-office itself.
// The body is written by an agent: render it as text only.
export interface Message {
  id: number;
  from?: string;
  from_user?: boolean; // the user's own words (ADR 0030)
  to: string;
  body: string;
  created_at: string;
  delivered_at?: string;
  delivery?: "stop_hook" | "pty" | "prompt";
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
  decided_by?: string; // user | lead | agent-office
  decider_agent_id?: string; // the Tech Lead that decided (ADR 0053)
  decision_reason?: string; // the decider's words: render as text only
  scope?: AccessScope; // an approval's scope
  decider?: "user" | "lead"; // a pending request: who is asked; the user can always decide first
}

// AccessGrant is a stored approval (ADR 0053): a host every launch of the
// agents it covers may reach. decision_reason is the decider's words.
export interface AccessGrant {
  id: number;
  host: string;
  scope: AccessScope;
  agent_id?: string; // an agent grant's agent
  request_id: number;
  created_at: string;
  decided_by?: string; // user | lead
  decider_agent_id?: string;
  decision_reason?: string;
}

export function isAccessGrant(v: unknown): v is AccessGrant {
  return isObj(v) && typeof v.id === "number" && typeof v.host === "string" &&
    (v.scope === "agent" || v.scope === "project") && typeof v.created_at === "string";
}

// AccessScope is who an approval covers (ADR 0053): the asking agent in every
// later session, or every agent of the project.
export type AccessScope = "agent" | "project";

// Who decides a project's access requests (ADR 0053).
export type AccessDecider = "user" | "lead" | "lead_policy";

// A project's overrides of the daemon's flags (ADR 0046); a missing field
// inherits. Money is integer micro-USD (ADR 0026).
export interface ProjectSettings {
  pack?: string; // a stack pack name, or "none"
  secrets_dir?: string;
  egress_allow?: string[];
  model?: string; // "": the CLI default
  role_models?: Record<string, string>;
  max_agents?: number;
  github?: boolean;
  github_remote?: string;
  reviewer?: string;
  review_rounds?: number;
  budget_session_micro_usd?: number;
  budget_goal_micro_usd?: number;
  access_decider?: AccessDecider; // default user (ADR 0053)
  lead_access_hosts?: string[]; // lead_policy: hosts the Tech Lead may approve
  worker_memory?: string; // sandboxed workers' memory limit, e.g. 4g (ADR 0051)
  worker_pids?: number; // sandboxed workers' process limit; 0: the pack's or --sandbox-pids (Phase 17)
  merge_target?: string; // the branch the goal's Merge button squash-merges into; default main (ADR 0055)
}

// A registered project (ADR 0046): one repository with its own team.
export interface Project {
  id: string;
  name: string;
  repo_path: string;
  slug: string;
  settings: ProjectSettings; // its own overrides
  effective: ProjectSettings; // what it uses: the daemon's flags with its overrides
  state: "open" | "error";
  error?: string; // why it could not open
  default: boolean; // serve --repo's project
  created_at: string;
}

export function isProject(v: unknown): v is Project {
  return isObj(v) && typeof v.id === "string" && v.id !== "" && typeof v.name === "string" &&
    typeof v.repo_path === "string" && typeof v.slug === "string" && isObj(v.settings) && isObj(v.effective) &&
    (v.state === "open" || v.state === "error") && typeof v.default === "boolean";
}

// The daemon-wide state (GET /api/status).
export interface DaemonStatus {
  sandbox: SandboxMode;
  max_agents: number; // the cap across all projects (ADR 0047)
  running: number;
  warnings: string[];
  defaults: ProjectSettings; // the daemon's flags, every field set
  default_project?: string;
  remote_hosts: string[];
  version: string;
}

export interface UsageTotals {
  sessions: number;
  messages: number;
  tokens: Tokens;
  cost_micro_usd: number;
  unpriced_tokens: number;
}

// Totals per project and overall (GET /api/usage).
export interface DaemonUsage {
  prices: PriceInfo;
  projects: { project_id: string; name: string; state: string; totals: UsageTotals }[];
  totals: UsageTotals;
}

function isTotals(v: unknown): v is UsageTotals {
  return isObj(v) && isCount(v.sessions) && isCount(v.messages) && isTokens(v.tokens) && isCount(v.cost_micro_usd);
}

export function isDaemonUsage(v: unknown): v is DaemonUsage {
  return isObj(v) && isObj(v.prices) && isTotals(v.totals) && Array.isArray(v.projects) &&
    v.projects.every((p) => isObj(p) && typeof p.project_id === "string" && typeof p.name === "string" && isTotals(p.totals));
}

// ProjectsMessage is one message of the daemon's /api/events: the project list.
export type ProjectsMessage =
  | { type: "projects"; projects: Project[] }
  | { type: "project"; project: Project }
  | { type: "project_removed"; id: string };

export function parseProjectsMessage(raw: string): ProjectsMessage | undefined {
  let m: unknown;
  try {
    m = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (!isObj(m)) return undefined;
  switch (m.type) {
    case "projects":
      return Array.isArray(m.projects) && m.projects.every(isProject) ? { type: "projects", projects: m.projects } : undefined;
    case "project":
      return isProject(m.project) ? { type: "project", project: m.project } : undefined;
    case "project_removed":
      return typeof m.id === "string" ? { type: "project_removed", id: m.id } : undefined;
    default:
      return undefined;
  }
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
  | {
    type: "snapshot"; agents: Agent[]; tasks: Task[]; accessRequests: AccessRequest[]; pullRequests: PullRequest[];
    inbox: InboxItem[]; specs: Spec[]; qaRuns: QARun[]; memory: MemoryEntry[];
  }
  | { type: "inbox"; item: InboxItem }
  | { type: "spec"; spec: Spec }
  | { type: "qa_run"; run: QARun }
  | { type: "memory"; entry: MemoryEntry }
  | { type: "memory_deleted"; id: string }
  | { type: "agent"; agent: Agent }
  | { type: "agent_removed"; id: string }
  | { type: "task"; task: Task }
  | { type: "task_deleted"; id: string }
  | { type: "sandbox_event"; agent_id: string; event: SandboxEvent }
  | { type: "message"; message: Message }
  | { type: "access_request"; request: AccessRequest }
  | { type: "pull_request"; pullRequest: PullRequest }
  | { type: "usage"; usage: SessionUsage }
  | { type: "goal_merge"; merge: GoalMerge };

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null;

function isImport(v: unknown): v is ImportInfo {
  return isObj(v) && typeof v.trigger === "string" && typeof v.outcome === "string" &&
    typeof v.commits === "number" && typeof v.at === "string" && (v.flagged === null || Array.isArray(v.flagged));
}

function isAgent(v: unknown): v is Agent {
  return isObj(v) && typeof v.id === "string" && typeof v.state === "string" && agentStates.includes(v.state) &&
    (v.sandbox === "host" || v.sandbox === "podman") && typeof v.kind === "string" &&
    (kinds as readonly string[]).includes(v.kind) && typeof v.role === "string" &&
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

const isCount = (v: unknown): v is number => typeof v === "number" && Number.isInteger(v) && v >= 0;

function isTokens(v: unknown): v is Tokens {
  return isObj(v) && isCount(v.input) && isCount(v.output) && isCount(v.cache_read) &&
    isCount(v.cache_write_5m) && isCount(v.cache_write_1h);
}

export function isSessionUsage(v: unknown): v is SessionUsage {
  return isObj(v) && typeof v.session_id === "string" && typeof v.agent_id === "string" &&
    (v.task_id === undefined || typeof v.task_id === "string") && (v.goal_id === undefined || typeof v.goal_id === "string") &&
    typeof v.kind === "string" && typeof v.role === "string" && typeof v.sandbox === "string" &&
    typeof v.started_at === "string" &&
    isCount(v.turns) && isCount(v.tool_calls) && isCount(v.messages) && isTokens(v.tokens) &&
    isCount(v.cost_micro_usd) && isCount(v.unpriced_tokens) &&
    (v.cli_cost_micro_usd === undefined || isCount(v.cli_cost_micro_usd)) &&
    Array.isArray(v.models) && v.models.every((m) => typeof m === "string");
}

function isModelUsage(v: unknown): v is ModelUsage {
  return isObj(v) && typeof v.model === "string" && typeof v.priced === "boolean" && isCount(v.messages) &&
    isTokens(v.tokens) && isCount(v.cost_micro_usd);
}

export function isUsageReport(v: unknown): v is UsageReport {
  return isObj(v) && isObj(v.prices) && typeof v.prices.as_of === "string" && typeof v.prices.source === "string" &&
    isObj(v.budgets) && isCount(v.budgets.session_micro_usd) && isCount(v.budgets.goal_micro_usd) &&
    isObj(v.totals) && isTokens(v.totals.tokens) && isCount(v.totals.cost_micro_usd) &&
    Array.isArray(v.sessions) && v.sessions.every(isSessionUsage) && Array.isArray(v.models) && v.models.every(isModelUsage);
}

const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === "string");
const optStrings = (v: unknown): boolean => v === undefined || v === null || isStrings(v);

function isConflict(v: unknown): v is Conflict {
  return isObj(v) && typeof v.branch === "string" && isStrings(v.files) && (v.with === undefined || typeof v.with === "string");
}

function isTask(v: unknown): v is Task {
  return isObj(v) && typeof v.id === "string" && typeof v.title === "string" &&
    typeof v.description === "string" && typeof v.status === "string" &&
    (taskStatuses as readonly string[]).includes(v.status) &&
    (v.parent_task_id === undefined || typeof v.parent_task_id === "string") &&
    optStrings(v.depends_on) && optStrings(v.files) && optStrings(v.acceptance) &&
    (v.conflicts === undefined || v.conflicts === null || (Array.isArray(v.conflicts) && v.conflicts.every(isConflict)));
}

const inboxKinds: readonly string[] = ["question", "info", "blocked", "briefing", "plan"];

export function isInboxItem(v: unknown): v is InboxItem {
  return isObj(v) && typeof v.id === "number" && typeof v.kind === "string" && inboxKinds.includes(v.kind) &&
    typeof v.body === "string" && typeof v.created_at === "string" &&
    (v.agent_id === undefined || typeof v.agent_id === "string");
}

function isPlan(v: unknown): v is Plan {
  return isObj(v) && typeof v.summary === "string" && typeof v.design === "string" && Array.isArray(v.sub_tasks) &&
    v.sub_tasks.every((s) => isObj(s) && typeof s.key === "string" && typeof s.title === "string" &&
      typeof s.description === "string" && optStrings(s.depends_on) && optStrings(s.files) && optStrings(s.acceptance));
}

export function isSpec(v: unknown): v is Spec {
  return isObj(v) && typeof v.id === "number" && typeof v.goal_id === "string" && typeof v.version === "number" &&
    (v.status === "proposed" || v.status === "approved" || v.status === "superseded") && isPlan(v.plan) &&
    typeof v.markdown === "string" && typeof v.created_at === "string";
}

const qaStatuses: readonly string[] = ["running", "passed", "failed", "error"];

export function isQARun(v: unknown): v is QARun {
  return isObj(v) && typeof v.id === "number" && typeof v.goal_id === "string" && typeof v.status === "string" &&
    qaStatuses.includes(v.status) && typeof v.branch === "string" && Array.isArray(v.merged) && Array.isArray(v.bugs) &&
    v.bugs.every((b) => isObj(b) && typeof b.title === "string" && typeof b.description === "string");
}

export function isMemoryEntry(v: unknown): v is MemoryEntry {
  return isObj(v) && typeof v.id === "number" && typeof v.title === "string" && typeof v.body === "string" &&
    (v.kind === "decision" || v.kind === "convention" || v.kind === "lesson") &&
    (v.status === "proposed" || v.status === "accepted");
}

// listOf validates an optional snapshot list: absent (an older daemon) is empty.
function listOf<T>(v: unknown, ok: (x: unknown) => x is T): T[] | undefined {
  if (v === undefined || v === null) return [];
  return Array.isArray(v) && v.every(ok) ? v : undefined;
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
    case "snapshot": {
      const inbox = listOf(m.inbox, isInboxItem), specs = listOf(m.specs, isSpec);
      const qaRuns = listOf(m.qa_runs, isQARun), memory = listOf(m.memory, isMemoryEntry);
      return Array.isArray(m.agents) && m.agents.every(isAgent) && Array.isArray(m.tasks) && m.tasks.every(isTask) &&
        Array.isArray(m.access_requests) && m.access_requests.every(isAccessRequest) &&
        Array.isArray(m.pull_requests) && m.pull_requests.every(isPullRequest) && inbox && specs && qaRuns && memory
        ? {
          type: "snapshot", agents: m.agents, tasks: m.tasks, accessRequests: m.access_requests, pullRequests: m.pull_requests,
          inbox, specs, qaRuns, memory,
        }
        : undefined;
    }
    case "inbox":
      return isInboxItem(m.inbox) ? { type: "inbox", item: m.inbox } : undefined;
    case "spec":
      return isSpec(m.spec) ? { type: "spec", spec: m.spec } : undefined;
    case "qa_run":
      return isQARun(m.qa_run) ? { type: "qa_run", run: m.qa_run } : undefined;
    case "memory":
      return isMemoryEntry(m.memory) ? { type: "memory", entry: m.memory } : undefined;
    case "memory_deleted":
      return typeof m.id === "string" ? { type: "memory_deleted", id: m.id } : undefined;
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
    case "goal_merge":
      return isGoalMerge(m.goal_merge) ? { type: "goal_merge", merge: m.goal_merge } : undefined;
    case "pull_request":
      return isPullRequest(m.pull_request) ? { type: "pull_request", pullRequest: m.pull_request } : undefined;
    case "usage":
      return isSessionUsage(m.usage) ? { type: "usage", usage: m.usage } : undefined;
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
    if (res.status === 401 && !path.startsWith("/api/auth/")) signalUnauthorized();
    const data: unknown = await res.json().catch(() => undefined);
    const msg = isObj(data) && typeof data.error === "string" ? data.error : `${method} ${path}: ${res.status}`;
    throw new ApiError(msg, res.status, data);
  }
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const api = {
  health: (): Promise<Health> => request("GET", "/api/health"),
  status: async (): Promise<DaemonStatus> => request("GET", "/api/status"),
  usageAll: async (): Promise<DaemonUsage> => {
    const u: unknown = await request("GET", "/api/usage");
    if (!isDaemonUsage(u)) throw new ApiError("unexpected usage totals", 200, undefined);
    return u;
  },
  // Projects (ADR 0046).
  projects: (): Promise<Project[]> => request("GET", "/api/projects"),
  addProject: (repo: string, name: string, settings: ProjectSettings): Promise<Project> =>
    request("POST", "/api/projects", { repo, name, settings }),
  updateProject: (id: string, patch: { name?: string; settings?: ProjectSettings }): Promise<Project> =>
    request("PATCH", `/api/projects/${encodeURIComponent(id)}`, patch),
  removeProject: (id: string): Promise<void> => request("DELETE", `/api/projects/${encodeURIComponent(id)}`),
  retryProject: (id: string): Promise<Project> => request("POST", `/api/projects/${encodeURIComponent(id)}/retry`),
  // Phase 10 (ADR 0039).
  authSession: (): Promise<AuthSession> => request("GET", "/api/auth/session"),
  pair: (code: string, name: string): Promise<AuthSession> => request("POST", "/api/auth/pair", { code, name }),
  signOut: (): Promise<void> => request("POST", "/api/auth/logout"),
  devices: (): Promise<Device[]> => request("GET", "/api/devices"),
  newPairingCode: (): Promise<PairingCode> => request("POST", "/api/devices/pairing-codes"),
  revokeDevice: (id: string): Promise<void> => request("DELETE", `/api/devices/${id}`),
  signOutOthers: (): Promise<{ signed_out: number }> => request("POST", "/api/devices/sign-out-others"),
};


// projectApi is the client of one project's routes (ADR 0046): every id it
// takes belongs to that project.
export function projectApi(projectId: string) {
  const base = `/api/projects/${encodeURIComponent(projectId)}`;
  return {
    projectId,
    eventsPath: `${base}/events`,
    terminalPath: (agentId: string): string => `${base}/agents/${agentId}/terminal`,
    status: (): Promise<Status> => request("GET", `${base}/status`),
    usage: async (): Promise<UsageReport> => {
      const rep: unknown = await request("GET", `${base}/usage`);
      if (!isUsageReport(rep)) throw new ApiError("unexpected usage report", 200, undefined);
      return rep;
    },
    startAgent: (taskId?: string, role: Role = "worker"): Promise<Agent> =>
      request("POST", `${base}/agents`, taskId ? { task_id: taskId, role } : undefined),
    stopAgent: (id: string): Promise<Agent> => request("POST", `${base}/agents/${id}/stop`),
    removeAgent: (id: string): Promise<RemoveResult> => request("DELETE", `${base}/agents/${id}`),
    discardAgent: (id: string): Promise<RemoveResult> => request("POST", `${base}/agents/${id}/discard`),
    importAgent: (id: string): Promise<ImportInfo> => request("POST", `${base}/agents/${id}/import`),
    resumeAgent: (id: string, prompt = ""): Promise<Agent> => request("POST", `${base}/agents/${id}/resume`, { prompt }),
    saveWork: (id: string): Promise<ImportInfo> => request("POST", `${base}/agents/${id}/save-work`),
    agentSessions: async (id: string): Promise<SessionInfo[]> => {
      const ss: unknown = await request("GET", `${base}/agents/${id}/sessions`);
      return Array.isArray(ss) ? ss.filter(isSessionInfo) : [];
    },
    // The bytes one session's terminal received, to replay into a read-only terminal.
    sessionTerminal: async (id: string, sid: string): Promise<Uint8Array> => {
      const res = await fetch(`${base}/agents/${id}/sessions/${sid}/terminal`, { headers: { Accept: "application/octet-stream" } });
      if (res.status === 401) signalUnauthorized();
      if (!res.ok) throw new ApiError(`GET saved terminal: ${res.status}`, res.status, undefined);
      return new Uint8Array(await res.arrayBuffer());
    },
    agentEvents: async (id: string): Promise<SandboxEvent[]> => {
      const evs: unknown = await request("GET", `${base}/agents/${id}/events`);
      return Array.isArray(evs) ? evs.filter(isSandboxEvent) : [];
    },
    agentMessages: async (id: string): Promise<Message[]> => {
      const msgs: unknown = await request("GET", `${base}/agents/${id}/messages`);
      return Array.isArray(msgs) ? msgs.filter(isMessage) : [];
    },
    approveAccess: (id: number, scope?: AccessScope): Promise<AccessRequest> =>
      request("POST", `${base}/access-requests/${id}/approve`, scope ? { scope } : undefined),
    denyAccess: (id: number): Promise<AccessRequest> => request("POST", `${base}/access-requests/${id}/deny`),
    accessGrants: async (): Promise<AccessGrant[]> => {
      const gs: unknown = await request("GET", `${base}/access-grants`);
      return Array.isArray(gs) ? gs.filter(isAccessGrant) : [];
    },
    revokeGrant: (id: number): Promise<AccessGrant> => request("DELETE", `${base}/access-grants/${id}`),
    goalRecord: (goalId: string): Promise<GoalRecord> => request("GET", `${base}/tasks/${goalId}/record`),
    removeGoalAgents: (goalId: string): Promise<GoalCleanup> => request("POST", `${base}/tasks/${goalId}/remove-agents`),
    mergePreview: (goalId: string): Promise<MergePreview> => request("GET", `${base}/tasks/${goalId}/merge`),
    mergeGoal: (goalId: string, confirm: { target_head: string; source_head: string; message: string }): Promise<GoalMerge> =>
      request("POST", `${base}/tasks/${goalId}/merge`, confirm),
    pushGoal: (goalId: string): Promise<GoalMerge> => request("POST", `${base}/tasks/${goalId}/push`),
    goalMerges: async (): Promise<GoalMerge[]> => {
      const ms: unknown = await request("GET", `${base}/goal-merges`);
      return Array.isArray(ms) ? ms.filter(isGoalMerge) : [];
    },
    createTask: (title: string, description: string): Promise<Task> =>
      request("POST", `${base}/tasks`, { title, description }),
    updateTask: (id: string, patch: Partial<Pick<Task, "title" | "description" | "status">>): Promise<Task> =>
      request("PATCH", `${base}/tasks/${id}`, patch),
    deleteTask: (id: string): Promise<void> => request("DELETE", `${base}/tasks/${id}`),
    openPullRequest: (taskId: string): Promise<PullRequest> => request("POST", `${base}/tasks/${taskId}/pull-request`),
    requestReview: (prId: number): Promise<PullRequest> => request("POST", `${base}/pull-requests/${prId}/review`),
    sendToWorker: (prId: number, reviewId: number): Promise<PullRequest> =>
      request("POST", `${base}/pull-requests/${prId}/follow-up`, { review_id: reviewId }),
    acceptFlagged: (prId: number, commits: string[]): Promise<PullRequest> =>
      request("POST", `${base}/pull-requests/${prId}/accept`, { commits }),
    // Phase 12 (ADR 0029-0034).
    roles: (): Promise<RoleInfo[]> => request("GET", `${base}/roles`),
    readInbox: (id: number): Promise<InboxItem> => request("POST", `${base}/inbox/${id}/read`),
    replyInbox: (id: number, body: string): Promise<InboxItem> => request("POST", `${base}/inbox/${id}/reply`, { body }),
    messageAgent: (id: string, body: string): Promise<Message> => request("POST", `${base}/agents/${id}/message`, { body }),
    editSpec: (id: number, plan: Plan): Promise<Spec> => request("PUT", `${base}/specs/${id}`, plan),
    approveSpec: (id: number): Promise<ApproveResult> => request("POST", `${base}/specs/${id}/approve`),
    resolveConflict: (taskId: string): Promise<void> => request("POST", `${base}/tasks/${taskId}/resolve-conflict`),
    addMemory: (d: MemoryDraft): Promise<MemoryEntry> => request("POST", `${base}/memory`, d),
    editMemory: (id: number, patch: Partial<MemoryDraft>): Promise<MemoryEntry> => request("PATCH", `${base}/memory/${id}`, patch),
    acceptMemory: (id: number): Promise<MemoryEntry> => request("POST", `${base}/memory/${id}/accept`),
    deleteMemory: (id: number): Promise<void> => request("DELETE", `${base}/memory/${id}`),
    askBriefing: (): Promise<Agent> => request("POST", `${base}/assistant/briefing`),
  };
}

export type ProjectApi = ReturnType<typeof projectApi>;

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}
