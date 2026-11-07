// A mocked agent-office daemon for browser tests (ADR 0025): every /api
// request is answered here, and /api/events plays a snapshot followed by
// whatever updates a test pushes. Fixtures are typed with the UI's own API
// types, so a change to them shows up here.
import type { Page, WebSocketRoute } from "@playwright/test";
import type { AccessRequest, Agent, AgentState, Message, Role, Status, Task, TaskStatus } from "../src/api";

const t0 = Date.parse("2026-11-08T10:00:00Z");
const iso = (minutes: number): string => new Date(t0 + minutes * 60_000).toISOString();

export function agent(id: string, role: Role, state: AgentState, over: Partial<Agent> = {}): Agent {
  return {
    id, role, state, model: "haiku", inherit_user_settings: false, sandbox: "podman",
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
    agent("a1ead0000001", "lead", "idle", { task_id: goal.id, started_at: iso(0) }),
    agent("b2c3d4000001", "worker", "working", { task_id: "t00000000001", activity: "Bash", started_at: iso(2) }),
    agent("c3d4e5000002", "worker", "working", { task_id: "t00000000002", activity: "Edit", started_at: iso(3) }),
    agent("d4e5f6000003", "worker", "waiting_permission", { task_id: "t00000000005", sandbox: "host", started_at: iso(4) }),
    agent("e5f6a7000004", "reviewer", "working", { task_id: "t00000000003", activity: "Read", started_at: iso(5) }),
    agent("f6a7b8000005", "worker", "idle", { task_id: "t00000000006", started_at: iso(6) }),
    agent("a7b8c9000006", "worker", "starting", { task_id: "t00000000007", started_at: iso(7) }),
    agent("b8c9d0000007", "worker", "working", { task_id: "t00000000004", activity: "Grep", started_at: iso(8) }),
    agent("c9d0e1000008", "reviewer", "idle", { started_at: iso(9) }),
    agent("d0e1f2000009", "worker", "exited", { task_id: "t00000000004", exit_reason: "reported_done", exit_code: 0, started_at: iso(1) }),
  ];
  for (let i = agents.length; i < n; i++) {
    agents.push(agent(`e${String(i).padStart(11, "0")}`, "worker", i % 2 ? "working" : "idle", { started_at: iso(10 + i) }));
  }
  const accessRequests: AccessRequest[] = [{
    id: 1, agent_id: "b8c9d0000007", kind: "domain", value: "pkg.go.dev", reason: "read the docs of a module",
    status: "pending", created_at: iso(9),
  }];
  return { agents: agents.slice(0, n), tasks, accessRequests, messages: [] };
}

export const status: Status = {
  sandbox: "podman", image: "localhost/agent-office-pack-go:02150a97ddc8", egress: ["proxy.golang.org"],
  pack: { name: "go", description: "Go 1.27", source: "builtin", egress: ["proxy.golang.org"], secrets: [], skills: ["go-sandbox"], mcp_servers: [] },
  warnings: [],
};

// MockDaemon serves one world to the page.
export class MockDaemon {
  private events?: WebSocketRoute;
  readonly terminals: string[] = []; // agent ids whose terminal was opened

  constructor(readonly world: World) {}

  async install(page: Page): Promise<void> {
    const json = (body: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      const p = url.pathname;
      if (p === "/api/health") return route.fulfill(json({ status: "ok", version: "phase-7-test" }));
      if (p === "/api/project") return route.fulfill(json({ id: "p1", name: "linkshelf", repo_path: "/home/dev/linkshelf", slug: "linkshelf" }));
      if (p === "/api/status") return route.fulfill(json(status));
      const m = /^\/api\/agents\/([^/]+)\/(events|messages)$/.exec(p);
      if (m) {
        return route.fulfill(json(m[2] === "messages" ? this.world.messages.filter((x) => x.to === m[1] || x.from === m[1]) : []));
      }
      return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: `mock: no route for ${p}` }) });
    });
    await page.routeWebSocket("**/api/events", (ws) => {
      this.events = ws;
      ws.send(JSON.stringify({
        type: "snapshot", agents: this.world.agents, tasks: this.world.tasks,
        access_requests: this.world.accessRequests, pull_requests: [],
      }));
    });
    await page.routeWebSocket(/\/api\/agents\/[^/]+\/terminal$/, (ws) => {
      const id = /\/api\/agents\/([^/]+)\/terminal$/.exec(ws.url())?.[1] ?? "";
      this.terminals.push(id);
      ws.send(Buffer.from(`\x1b[32mmock terminal of ${id}\x1b[0m\r\n> `));
      ws.onMessage(() => undefined);
    });
  }

  private send(msg: object): void {
    if (!this.events) throw new Error("mock daemon: the page has not opened /api/events");
    this.events.send(JSON.stringify(msg));
  }

  updateAgent(id: string, patch: Partial<Agent>): void {
    const i = this.world.agents.findIndex((a) => a.id === id);
    this.world.agents[i] = { ...this.world.agents[i], ...patch };
    this.send({ type: "agent", agent: this.world.agents[i] });
  }

  addAgent(a: Agent): void {
    this.world.agents.push(a);
    this.send({ type: "agent", agent: a });
  }

  removeAgent(id: string): void {
    this.world.agents = this.world.agents.filter((a) => a.id !== id);
    this.send({ type: "agent_removed", id });
  }

  message(id: number, from: string | undefined, to: string): void {
    const m: Message = { id, from, to, body: "mock message", created_at: new Date().toISOString() };
    this.world.messages.push(m);
    this.send({ type: "message", message: m });
  }
}
