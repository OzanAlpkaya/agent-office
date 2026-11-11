// A mocked agent-office daemon for browser tests (ADR 0025): every /api
// request is answered here, and /api/events plays a snapshot followed by
// whatever updates a test pushes. Fixtures are typed with the UI's own API
// types, so a change to them shows up here.
import type { Page, Route, WebSocketRoute } from "@playwright/test";
import type {
  AccessGrant, AccessRequest, Agent, AgentState, GoalMerge, GoalRecord, MergePreview, DaemonStatus, DaemonUsage, Device, InboxItem, Kind, MemoryEntry, Message, ModelUsage, Project,
  ProjectSettings, QARun, RoleInfo, SandboxEvent, SessionUsage, Spec, Status, Task, TaskStatus, Tokens, UsageReport,
} from "../src/api";
import { defaultRole } from "../src/api";

const t0 = Date.parse("2026-11-08T10:00:00Z");
const iso = (minutes: number): string => new Date(t0 + minutes * 60_000).toISOString();

// Names like the daemon's (ADR 0043): a fixed one per id, so a test sees the
// same name whichever tests ran before it. team() names its agents itself.
const names = [
  "Zeynep", "Finn", "Nia", "Hugo", "Mei", "Tunde", "Sofia", "Erik", "Aroha", "Karim", "Ines", "Wei", "Chidi", "Astrid",
];
export function nameFor(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return names[h % names.length];
}

export function agent(id: string, kind: Kind, state: AgentState, over: Partial<Agent> = {}): Agent {
  return {
    id, name: nameFor(id), kind, role: defaultRole[kind], state, model: "haiku", inherit_user_settings: false, sandbox: "podman", resumable: state === "exited",
    branch: `agent-office/${id}`, worktree_path: `/data/agents/${id}`, workspace_kind: "clone",
    worktree_status: "active", has_terminal: true, started_at: iso(0), ...over,
  };
}

export function task(id: string, title: string, status: TaskStatus, over: Partial<Task> = {}): Task {
  return { id, title, description: "", status, created_at: iso(0), updated_at: iso(0), ...over };
}

export interface World {
  agents: Agent[];
  tasks: Task[];
  accessRequests: AccessRequest[];
  messages: Message[];
  usage?: SessionUsage[];
  models?: ModelUsage[];
  inbox?: InboxItem[];
  specs?: Spec[];
  qaRuns?: QARun[];
  memory?: MemoryEntry[];
  // Phase 17.
  merges?: GoalMerge[];
  grants?: AccessGrant[];
  records?: Record<string, GoalRecord>; // by goal id
  previews?: Record<string, MergePreview>; // by goal id
  events?: Record<string, SandboxEvent[]>; // recorded sandbox events by agent id
}

const role = (name: string, behaviour: Kind, read_only = false): RoleInfo => ({
  name, behaviour, read_only, description: `${name} role`, model: "", reports_to: "lead", team_tools: [], deny: null,
  source: "builtin",
});

export const roles: RoleInfo[] = [
  role("architect", "architect", true), role("assistant", "assistant", true), role("docs", "worker"), role("qa", "qa"),
  role("reviewer", "reviewer"), role("security", "worker"), role("tech-lead", "lead", true), role("worker", "worker"),
];

const zero: Tokens = { input: 0, output: 0, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 };

export function session(id: string, a: Agent, goal: string | undefined, over: Partial<SessionUsage> = {}): SessionUsage {
  return {
    session_id: id, agent_id: a.id, task_id: a.task_id, goal_id: goal, kind: a.kind, role: a.role, sandbox: a.sandbox,
    source: a.sandbox === "podman" ? "api" : "transcript", started_at: a.started_at, turns: 1, tool_calls: 2, messages: 3,
    tokens: zero, cost_micro_usd: 0, unpriced_tokens: 0, models: ["claude-haiku-5-5"], ...over,
  };
}

// teamUsage gives the team() world token usage: the lead, two workers and a
// reviewer of the goal, and a worker without a task whose model has no price.
export function teamUsage(w: World): World {
  const by = (id: string): Agent => w.agents.find((a) => a.id === id) as Agent;
  const g = "g00000000001";
  w.usage = [
    session("s-lead", by("a1ead0000001"), g, { tokens: { ...zero, input: 1200, output: 800, cache_read: 30_000 }, cost_micro_usd: 12_500 }),
    session("s-w1", by("b2c3d4000001"), g, { tokens: { ...zero, input: 4000, output: 9000, cache_write_5m: 2000 }, cost_micro_usd: 1_234_567, turns: 4, tool_calls: 31 }),
    session("s-w2", by("c3d4e5000002"), g, { tokens: { ...zero, input: 500, output: 700 }, cost_micro_usd: 68_864 }),
    session("s-host", by("d4e5f6000003"), g, { tokens: { ...zero, input: 10, output: 20 }, cost_micro_usd: 100, cli_cost_micro_usd: 120 }),
    session("s-solo", by("c9d0e1000008"), undefined, { tokens: { ...zero, input: 900, output: 100 }, cost_micro_usd: 0, unpriced_tokens: 1000, models: ["claude-local-1"] }),
  ];
  w.models = [
    { model: "claude-haiku-5-5", priced: true, messages: 12, tokens: { ...zero, input: 5710, output: 10520 }, cost_micro_usd: 1_316_031 },
    { model: "claude-local-1", priced: false, messages: 3, tokens: { ...zero, input: 900, output: 100 }, cost_micro_usd: 0 },
  ];
  return w;
}

function report(w: World): UsageReport {
  const sessions = w.usage ?? [];
  const sum = (f: (s: SessionUsage) => number): number => sessions.reduce((n, s) => n + f(s), 0);
  return {
    prices: { as_of: "2026-11-08", source: "https://platform.claude.com/docs/en/about-claude/pricing" },
    budgets: { session_micro_usd: 0, goal_micro_usd: 5_000_000 },
    totals: {
      sessions: sessions.length, messages: sum((s) => s.messages), cost_micro_usd: sum((s) => s.cost_micro_usd),
      unpriced_tokens: sum((s) => s.unpriced_tokens),
      tokens: {
        input: sum((s) => s.tokens.input), output: sum((s) => s.tokens.output), cache_read: sum((s) => s.tokens.cache_read),
        cache_write_5m: sum((s) => s.tokens.cache_write_5m), cache_write_1h: sum((s) => s.tokens.cache_write_1h),
      },
    },
    sessions, models: w.models ?? [],
    vendors: [...new Set(sessions.map((s) => (s.cli === "codex" ? "openai" : "anthropic")))].sort().map((vendor) => {
      const of = sessions.filter((s) => (s.cli === "codex" ? "openai" : "anthropic") === vendor);
      const add = (f: (s: SessionUsage) => number): number => of.reduce((n, s) => n + f(s), 0);
      return {
        vendor, sessions: of.length, messages: add((s) => s.messages), cost_micro_usd: add((s) => s.cost_micro_usd),
        unpriced_tokens: add((s) => s.unpriced_tokens),
        tokens: { input: add((s) => s.tokens.input), output: add((s) => s.tokens.output), cache_read: add((s) => s.tokens.cache_read),
          cache_write_5m: add((s) => s.tokens.cache_write_5m), cache_write_1h: add((s) => s.tokens.cache_write_1h) },
      };
    }),
  };
}

// team builds a realistic team: a lead planning a goal, workers in every
// state, two reviewers and one worker that has finished. n adds idle
// workers beyond the first ten.
export function team(n = 10): World {
  const goal = task("g00000000001", "Add tags to saved links", "in_progress");
  const subs: [string, string, TaskStatus][] = [
    ["t00000000001", "Tag model and migration", "in_progress"],
    ["t00000000002", "Tag filter in the list API", "in_progress"],
    ["t00000000003", "Tags in the CLI output", "review"],
    ["t00000000004", "Validate tag names", "done"],
    ["t00000000005", "Docs for tags", "todo"],
    ["t00000000006", "Rename tags", "todo"],
    ["t00000000007", "Tag colours", "blocked"],
  ];
  const tasks = [goal, ...subs.map(([id, title, s], i) => task(id, title, s, { parent_task_id: goal.id, created_by: "a1ead0000001", created_at: iso(1 + i) }))];
  const agents: Agent[] = [
    agent("a1ead0000001", "lead", "idle", { name: "Kofi", task_id: goal.id, started_at: iso(0) }),
    agent("b2c3d4000001", "worker", "working", { name: "Emma", task_id: "t00000000001", activity: "Bash", started_at: iso(2) }),
    agent("c3d4e5000002", "worker", "working", { name: "Yuki", task_id: "t00000000002", activity: "Edit", started_at: iso(3) }),
    agent("d4e5f6000003", "worker", "waiting_permission", { name: "Omar", task_id: "t00000000005", sandbox: "host", started_at: iso(4) }),
    agent("e5f6a7000004", "reviewer", "working", { name: "Priya", task_id: "t00000000003", activity: "Read", started_at: iso(5) }),
    agent("f6a7b8000005", "worker", "idle", { name: "Lena", task_id: "t00000000006", started_at: iso(6) }),
    agent("a7b8c9000006", "worker", "starting", { name: "Mateo", task_id: "t00000000007", started_at: iso(7) }),
    agent("b8c9d0000007", "worker", "working", { name: "Amara", task_id: "t00000000004", activity: "Grep", started_at: iso(8) }),
    agent("c9d0e1000008", "reviewer", "idle", { name: "Jisoo", started_at: iso(9) }),
    agent("d0e1f2000009", "worker", "exited", { name: "Ravi", task_id: "t00000000004", exit_reason: "reported_done", exit_code: 0, started_at: iso(1) }),
  ];
  for (let i = agents.length; i < n; i++) {
    agents.push(agent(`e${String(i).padStart(11, "0")}`, "worker", i % 2 ? "working" : "idle",
      { name: names[(i - 10) % names.length], started_at: iso(10 + i) }));
  }
  const accessRequests: AccessRequest[] = [{
    id: 1, agent_id: "b8c9d0000007", kind: "domain", value: "pkg.go.dev", reason: "read the docs of a module",
    status: "pending", created_at: iso(9),
  }];
  // The daemon expires an agent's requests when it exits, so a request always has its agent.
  const listed = new Set(agents.slice(0, n).map((a) => a.id));
  return { agents: agents.slice(0, n), tasks, accessRequests: accessRequests.filter((r) => listed.has(r.agent_id)), messages: [] };
}

export const status: Status = {
  sandbox: "podman", image: "localhost/agent-office-pack-go:02150a97ddc8", egress: ["proxy.golang.org"],
  pack: { name: "go", description: "Go 1.27", source: "builtin", egress: ["proxy.golang.org"], secrets: [], skills: ["go-sandbox"], mcp_servers: [] },
  warnings: [], remote_hosts: ["box.tail1234.ts.net"],
};

// The daemon's flags as project settings (ADR 0046).
export const defaults: ProjectSettings = {
  pack: "go", secrets_dir: "/home/dev/.config/agent-office/secrets", egress_allow: [], model: "haiku", role_models: {},
  max_agents: 8, github: false, github_remote: "origin", reviewer: "internal", review_rounds: 2,
  budget_session_micro_usd: 0, budget_goal_micro_usd: 5_000_000,
};

export function project(id: string, name: string, over: Partial<Project> = {}): Project {
  return {
    id, name, repo_path: `/home/dev/${name}`, slug: `${name}-1234abcd`, settings: {}, effective: { ...defaults }, state: "open",
    default: false, created_at: iso(0), ...over,
  };
}

// The pairing code the mock accepts (ADR 0039).
export const mockCode = "ABCD-EFGH";

// MockDaemon serves one world to the page.
export class MockDaemon {
  private events?: WebSocketRoute;
  readonly terminals: string[] = []; // agent ids whose terminal was opened
  readonly requests: string[] = []; // "METHOD path body" of every write the page sent
  readonly projectRequests: string[] = []; // "project METHOD path" of every write to a project's routes
  refuseProjectUpdate?: string; // PATCH /api/projects/{id} answers 409 with this error (agents running)
  roles: RoleInfo[] = roles; // what GET roles answers
  readonly terminalInput: string[] = []; // text frames the page sent to terminals
  // A terminal program for tests: what follows the greeting (e.g. mouse tracking
  // on, like Claude Code's fullscreen mode) and how it answers input.
  terminalPreamble = "";
  onTerminalInput?: (data: string, reply: (bytes: string) => void) => void;
  private readonly terminalSockets = new Map<string, WebSocketRoute>();
  paired = true; // whether this browser is paired (ADR 0039)
  devices: Device[] = [
    { id: "d1", name: "Chrome on Linux", kind: "browser", paired_via: "local", created_at: iso(0), last_seen_at: iso(30) },
    { id: "d2", name: "Pixel 9", kind: "browser", paired_via: "tailscale:ozan@github", created_at: iso(5), last_seen_at: iso(20) },
  ];

  // Projects (ADR 0046): the first is p1, linkshelf, with the world the test
  // gave; addProject adds more, each with its own world and events stream.
  readonly projects: { project: Project; world: World }[];
  private hubEvents?: WebSocketRoute;
  private readonly projectEvents = new Map<string, WebSocketRoute>();

  constructor(readonly world: World) {
    this.projects = [{ project: project("p1", "linkshelf", { default: true }), world }];
  }

  // addProject registers another project before (or after) install.
  addProject(p: Project, world: World): void {
    this.projects.push({ project: p, world });
    this.hubEvents?.send(JSON.stringify({ type: "project", project: p }));
  }

  private worldOf(id: string): World | undefined {
    return this.projects.find((x) => x.project.id === id)?.world;
  }

  private snapshot(w: World): string {
    return JSON.stringify({
      type: "snapshot", agents: w.agents, tasks: w.tasks, access_requests: w.accessRequests, pull_requests: [], inbox: w.inbox ?? [],
      specs: w.specs ?? [], qa_runs: w.qaRuns ?? [], memory: w.memory ?? [],
    });
  }

  // sendTo sends an update on one project's events stream.
  sendTo(projectId: string, msg: object): void {
    const ws = projectId === "p1" ? this.events : this.projectEvents.get(projectId);
    if (!ws) throw new Error(`mock daemon: the page has not opened the events of ${projectId}`);
    ws.send(JSON.stringify({ ...msg, project_id: projectId }));
  }

  async install(page: Page): Promise<void> {
    const json = (body: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      let p = url.pathname;
      const method = route.request().method();
      const me = this.devices[0];
      if (p === "/api/auth/session") {
        return route.fulfill(json(this.paired ? { authenticated: true, device: me, remote: false } : { authenticated: false, remote: false }));
      }
      if (p === "/api/auth/pair") {
        const body = route.request().postDataJSON() as { code: string; name: string };
        this.requests.push(`POST ${p} ${body.name}`);
        if (body.code.replace("-", "").toUpperCase() !== mockCode.replace("-", "")) {
          return route.fulfill({ status: 401, contentType: "application/json", body: '{"error":"the pairing code is wrong or has expired"}' });
        }
        this.paired = true;
        return route.fulfill({ ...json({ authenticated: true, device: { ...me, name: body.name }, remote: false }), status: 201 });
      }
      if (!this.paired && p !== "/api/health") {
        return route.fulfill({ status: 401, contentType: "application/json", body: '{"error":"not paired"}' });
      }
      if (p === "/api/devices") return route.fulfill(json(this.devices.map((d) => ({ ...d, current: d.id === me.id }))));
      if (p === "/api/devices/pairing-codes") {
        return route.fulfill({ ...json({ code: "WXYZ-2345", expires_at: new Date(Date.now() + 300_000).toISOString(),
          remote_urls: ["https://box.tail1234.ts.net/"] }), status: 201 });
      }
      if (p === "/api/devices/sign-out-others") {
        this.requests.push(`POST ${p}`);
        const n = this.devices.length - 1;
        this.devices = [me];
        return route.fulfill(json({ signed_out: n }));
      }
      const dev = /^\/api\/devices\/([^/]+)$/.exec(p);
      if (dev && method === "DELETE") {
        this.requests.push(`DELETE ${p}`);
        this.devices = this.devices.filter((d) => d.id !== dev[1]);
        if (dev[1] === me.id) this.paired = false;
        return route.fulfill({ status: 204 });
      }
      if (p === "/api/health") return route.fulfill(json({ status: "ok", version: "phase-7-test" }));
      if (p === "/api/status") {
        const running = this.projects.reduce((n, x) => n + x.world.agents.filter((a) => a.state !== "exited").length, 0);
        const d: DaemonStatus = { sandbox: "podman", max_agents: 8, running, warnings: [], defaults, default_project: "p1",
          remote_hosts: ["box.tail1234.ts.net"], version: "phase-7-test" };
        return route.fulfill(json(d));
      }
      if (p === "/api/usage") {
        const per = this.projects.map((x) => ({ project_id: x.project.id, name: x.project.name, state: x.project.state, totals: report(x.world).totals }));
        const totals = per.reduce((t, x) => ({
          sessions: t.sessions + x.totals.sessions, messages: t.messages + x.totals.messages,
          cost_micro_usd: t.cost_micro_usd + x.totals.cost_micro_usd, unpriced_tokens: t.unpriced_tokens + x.totals.unpriced_tokens,
          tokens: { ...t.tokens, input: t.tokens.input + x.totals.tokens.input, output: t.tokens.output + x.totals.tokens.output },
        }), report({ agents: [], tasks: [], accessRequests: [], messages: [] }).totals);
        const u: DaemonUsage = { prices: report(this.world).prices, projects: per, totals };
        return route.fulfill(json(u));
      }
      if (p === "/api/projects") {
        if (method === "POST") {
          const body = route.request().postDataJSON() as { repo: string; name: string; settings: ProjectSettings };
          this.requests.push(`POST ${p} ${JSON.stringify(body)}`);
          const name = body.name || body.repo.split("/").pop() || "repo";
          const np = project(`p${this.projects.length + 1}`, name, { repo_path: body.repo, settings: body.settings,
            effective: { ...defaults, ...body.settings } });
          this.addProject(np, { agents: [], tasks: [], accessRequests: [], messages: [] });
          return route.fulfill({ ...json(np), status: 201 });
        }
        return route.fulfill(json(this.projects.map((x) => x.project)));
      }
      const proj = /^\/api\/projects\/([^/]+)(\/.*)?$/.exec(p);
      if (proj && (proj[2] === undefined || proj[2] === "/retry")) {
        const i = this.projects.findIndex((x) => x.project.id === proj[1]);
        if (i === -1) return route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"project not found"}' });
        if (method === "GET") return route.fulfill(json(this.projects[i].project));
        this.requests.push(`${method} ${p} ${route.request().postData() ?? ""}`.trim());
        if (method === "DELETE") {
          this.projects.splice(i, 1);
          this.hubEvents?.send(JSON.stringify({ type: "project_removed", id: proj[1] }));
          return route.fulfill({ status: 204 });
        }
        if (method === "PATCH" && this.refuseProjectUpdate) {
          return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: this.refuseProjectUpdate }) });
        }
        const patch = (route.request().postDataJSON() ?? {}) as { name?: string; settings?: ProjectSettings };
        const cur = this.projects[i].project;
        const next = { ...cur, name: patch.name ?? cur.name, settings: patch.settings ?? cur.settings,
          effective: { ...defaults, ...(patch.settings ?? cur.settings) } };
        this.projects[i].project = next;
        this.hubEvents?.send(JSON.stringify({ type: "project", project: next }));
        return route.fulfill(json(next));
      }
      // A project's routes are served from its world, under the paths the
      // mock had before projects (so recorded requests read the same).
      let w = this.world;
      if (proj) {
        const pw = this.worldOf(proj[1]);
        if (!pw) return route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"project not found"}' });
        w = pw;
        p = `/api${proj[2]}`;
        if (method !== "GET") this.projectRequests.push(`${proj[1]} ${method} ${p}`);
      }
      if (p === "/api/status") return route.fulfill(json(status));
      if (p === "/api/usage") return route.fulfill(json(report(w)));
      if (p === "/api/roles") return route.fulfill(json(this.roles));
      if (method !== "GET") {
        this.requests.push(`${method} ${p} ${route.request().postData() ?? ""}`.trim());
        if (/^\/api\/inbox\/\d+\/(reply|read)$/.test(p)) {
          const id = Number(p.split("/")[3]);
          const it = (w.inbox ?? []).find((x) => x.id === id);
          const body = route.request().postDataJSON() as { body?: string } | null;
          const next = { ...it, read_at: iso(30), ...(body?.body ? { reply: body.body, answered_at: iso(30) } : {}) };
          return route.fulfill(json(next));
        }
        if (method === "PUT" && /^\/api\/specs\/\d+$/.test(p)) {
          const s = w.specs?.[0];
          return route.fulfill(json({ ...s, id: 99, version: (s?.version ?? 0) + 1, plan: route.request().postDataJSON() }));
        }
        if (/^\/api\/specs\/\d+\/approve$/.test(p)) {
          return route.fulfill(json({ spec: w.specs?.[0], created: ["t1"], updated: [], deleted: [], kept: [] }));
        }
        if (p.endsWith("/resolve-conflict") || (method === "DELETE" && p.startsWith("/api/memory/"))) {
          return route.fulfill({ status: 204 });
        }
        const decide = /^\/api\/access-requests\/(\d+)\/(approve|deny)$/.exec(p);
        if (decide) {
          const r = w.accessRequests.find((x) => x.id === Number(decide[1]));
          return route.fulfill(json({ ...r, status: decide[2] === "approve" ? "approved" : "denied", decided_at: iso(30) }));
        }
        const resume = /^\/api\/agents\/([^/]+)\/resume$/.exec(p);
        if (resume) {
          const a = w.agents.find((x) => x.id === resume[1]);
          return route.fulfill(json({ ...a, state: "starting", resumable: false, session_id: "11111111-0000-4000-8000-000000000001" }));
        }
        const patchTask = /^\/api\/tasks\/([^/]+)$/.exec(p);
        if (patchTask && method === "PATCH") {
          const i = w.tasks.findIndex((x) => x.id === patchTask[1]);
          if (i === -1) return route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' });
          w.tasks[i] = { ...w.tasks[i], ...(route.request().postDataJSON() as Partial<Task>), updated_at: iso(50) };
          if (w === this.world) this.send({ type: "task", task: w.tasks[i] });
          return route.fulfill(json(w.tasks[i]));
        }
        const goalOp = /^\/api\/tasks\/([^/]+)\/(merge|push|remove-agents)$/.exec(p);
        if (goalOp) return this.goalOp(route, w, goalOp[1], goalOp[2]);
        const grant = /^\/api\/access-grants\/(\d+)$/.exec(p);
        if (grant && method === "DELETE") {
          const g = (w.grants ?? []).find((x) => x.id === Number(grant[1]));
          w.grants = (w.grants ?? []).filter((x) => x.id !== Number(grant[1]));
          return g ? route.fulfill(json(g)) : route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' });
        }
        if (/^\/api\/memory\/\d+\/accept$/.test(p)) {
          const id = Number(p.split("/")[3]);
          return route.fulfill(json({ ...(w.memory ?? []).find((m) => m.id === id), status: "accepted" }));
        }
      }
      if (p === "/api/goal-merges") return route.fulfill(json(w.merges ?? []));
      if (p === "/api/access-grants") return route.fulfill(json(w.grants ?? []));
      const rec = /^\/api\/tasks\/([^/]+)\/(record|merge)$/.exec(p);
      if (rec) {
        const v = rec[2] === "record" ? w.records?.[rec[1]] : w.previews?.[rec[1]];
        return v ? route.fulfill(json(v)) : route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' });
      }
      const ss = /^\/api\/agents\/([^/]+)\/sessions$/.exec(p);
      if (ss) {
        const a = w.agents.find((x) => x.id === ss[1]);
        if (!a) return route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' });
        const live = a.has_terminal && a.state !== "exited";
        return route.fulfill(json([
          { id: a.session_id ?? `${a.id}-s2`, started_at: iso(20), resumed_from: `${a.id}-s1`, turns: 3, tool_calls: 4,
            live, terminal: true, ...(live ? {} : { ended_at: iso(25) }) },
          { id: `${a.id}-s1`, started_at: iso(0), ended_at: iso(10), turns: 2, tool_calls: 1, live: false, terminal: true },
        ]));
      }
      const st = /^\/api\/agents\/([^/]+)\/sessions\/([^/]+)\/terminal$/.exec(p);
      if (st) {
        return route.fulfill({ status: 200, contentType: "application/octet-stream", body: `saved output of ${st[2]}\r\n` });
      }
      const m = /^\/api\/agents\/([^/]+)\/(events|messages)$/.exec(p);
      if (m) {
        return route.fulfill(json(m[2] === "messages" ? w.messages.filter((x) => x.to === m[1] || x.from === m[1]) : w.events?.[m[1]] ?? []));
      }
      return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: `mock: no route for ${p}` }) });
    });
    await page.routeWebSocket("**/api/events", (ws) => {
      if (!this.paired) {
        void ws.close({ code: 4401, reason: "signed out" });
        return;
      }
      this.hubEvents = ws;
      ws.send(JSON.stringify({ type: "projects", projects: this.projects.map((x) => x.project) }));
    });
    await page.routeWebSocket(/\/api\/projects\/[^/]+\/events$/, (ws) => {
      const pid = /\/api\/projects\/([^/]+)\/events$/.exec(ws.url())?.[1] ?? "";
      const w = this.worldOf(pid);
      if (!this.paired || !w) {
        void ws.close({ code: this.paired ? 1011 : 4401, reason: this.paired ? "no such project" : "signed out" });
        return;
      }
      if (pid === "p1") this.events = ws;
      else this.projectEvents.set(pid, ws);
      ws.send(this.snapshot(w));
    });
    await page.routeWebSocket(/\/api\/projects\/[^/]+\/agents\/[^/]+\/terminal$/, (ws) => {
      const id = /\/agents\/([^/]+)\/terminal$/.exec(ws.url())?.[1] ?? "";
      this.terminals.push(id);
      ws.send(Buffer.from(`\x1b[32mmock terminal of ${id}\x1b[0m\r\n> ${this.terminalPreamble}`));
      this.terminalSockets.set(id, ws);
      ws.onMessage((m) => {
        this.terminalInput.push(String(m));
        const msg = JSON.parse(String(m)) as { type: string; data?: string };
        if (msg.type === "input" && msg.data) this.onTerminalInput?.(msg.data, (b) => ws.send(Buffer.from(b)));
      });
      // An exited agent's terminal replays its scrollback, then closes like the daemon's.
      if (this.world.agents.find((a) => a.id === id)?.state === "exited") {
        ws.send(Buffer.from("Resume this session with:\r\nclaude --resume 00000000-0000-4000-8000-000000000000\r\n"));
        void ws.close({ code: 1000, reason: "process exited" });
      }
    });
  }

  // terminalOutput sends program output to an open terminal, unprompted.
  terminalOutput(id: string, bytes: string): void {
    this.terminalSockets.get(id)?.send(Buffer.from(bytes));
  }

  // signOut ends this browser's pairing the way a revoke from another device does.
  signOut(): void {
    this.paired = false;
    void this.events?.close({ code: 4401, reason: "signed out" });
  }

  private send(msg: object): void {
    if (!this.events) throw new Error("mock daemon: the page has not opened the events of p1");
    this.events.send(JSON.stringify({ ...msg, project_id: "p1" }));
  }

  updateAgent(id: string, patch: Partial<Agent>): void {
    const i = this.world.agents.findIndex((a) => a.id === id);
    this.world.agents[i] = { ...this.world.agents[i], ...patch };
    this.send({ type: "agent", agent: this.world.agents[i] });
  }

  updateTask(id: string, patch: Partial<Task>): void {
    const i = this.world.tasks.findIndex((t) => t.id === id);
    this.world.tasks[i] = { ...this.world.tasks[i], ...patch };
    this.send({ type: "task", task: this.world.tasks[i] });
  }

  addAgent(a: Agent): void {
    this.world.agents.push(a);
    this.send({ type: "agent", agent: a });
  }

  removeAgent(id: string): void {
    this.world.agents = this.world.agents.filter((a) => a.id !== id);
    this.send({ type: "agent_removed", id });
  }

  // usage sends a live usage update for one session (ADR 0026).
  usage(s: SessionUsage): void {
    this.world.usage = [...(this.world.usage ?? []).filter((x) => x.session_id !== s.session_id), s];
    this.send({ type: "usage", usage: s });
  }

  // inboxItem sends a live message to the user (ADR 0030).
  inboxItem(it: InboxItem): void {
    this.world.inbox = [it, ...(this.world.inbox ?? []).filter((x) => x.id !== it.id)];
    this.send({ type: "inbox", inbox: it });
  }

  // accessRequest sends a new or decided access request (ADR 0019).
  accessRequest(r: AccessRequest): void {
    this.world.accessRequests = [...this.world.accessRequests.filter((x) => x.id !== r.id), r];
    this.send({ type: "access_request", access_request: r });
  }

  memoryEntry(m: MemoryEntry): void {
    this.world.memory = [m, ...(this.world.memory ?? []).filter((x) => x.id !== m.id)];
    this.send({ type: "memory", memory: m });
  }

  // mergeRefusal, when set, is the daemon's refusal of the next merge (409).
  mergeRefusal = "";

  // goalOp serves Merge, Push and the removal of a goal's exited agents (ADR 0054, 0055).
  private goalOp(route: Route, w: World, goalId: string, op: string): Promise<void> {
    const json = (body: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    if (op === "merge") {
      if (this.mergeRefusal) {
        return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: this.mergeRefusal }) });
      }
      const pv = w.previews?.[goalId];
      const body = route.request().postDataJSON() as { message: string; target_head: string; source_head: string };
      const m: GoalMerge = { id: (w.merges?.length ?? 0) + 1, goal_id: goalId, target: pv?.target ?? "main",
        source_head: body.source_head, target_before: body.target_head, commit: "abcdef0123456789abcdef0123456789abcdef01",
        message: body.message, created_at: iso(40) };
      w.merges = [m, ...(w.merges ?? [])];
      if (pv) w.previews = { ...w.previews, [goalId]: { ...pv, ready: false, reason: "already merged into main as abcdef01", merged: m } };
      this.send({ type: "goal_merge", goal_merge: m });
      return route.fulfill(json(m));
    }
    if (op === "push") {
      const m = (w.merges ?? []).find((x) => x.goal_id === goalId);
      if (!m) return route.fulfill({ status: 409, contentType: "application/json", body: '{"error":"merge refused: merge it first"}' });
      const pushed = { ...m, pushed_at: iso(41), push_remote: "origin" };
      w.merges = (w.merges ?? []).map((x) => (x.id === m.id ? pushed : x));
      this.send({ type: "goal_merge", goal_merge: pushed });
      return route.fulfill(json(pushed));
    }
    const ids = new Set([goalId, ...w.tasks.filter((t) => t.parent_task_id === goalId).map((t) => t.id)]);
    const gone = w.agents.filter((a) => a.state === "exited" && a.task_id !== undefined && ids.has(a.task_id));
    for (const a of gone) this.removeAgent(a.id);
    return route.fulfill(json({ removed: gone.map((a) => ({ agent_id: a.id, name: a.name, branch: "kept" })), kept: [] }));
  }

  message(id: number, from: string | undefined, to: string): void {
    const m: Message = { id, from, to, body: "mock message", created_at: new Date().toISOString() };
    this.world.messages.push(m);
    this.send({ type: "message", message: m });
  }
}
