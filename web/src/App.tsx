import { type JSX, Suspense, lazy, useEffect, useState } from "react";
import {
  type AccessRequest, type Agent, type AgentState, ApiError, type ApproveResult, type ImportInfo, type Kind, type Message,
  type Project, type Role, type RoleInfo, type SandboxEvent, type Spec, type Status, type Task, type TaskStatus, api,
} from "./api";
import { Board } from "./Board";
import { InboxPanel, MemoryPanel } from "./Team";
import { unread } from "./workflow";
import { TerminalView } from "./TerminalView";
import { useWorld } from "./useAgents";
import { type UsageRow, formatMoney, formatTokens, sessionList, totalTokens, usageBy } from "./usage";

// The office pulls in PixiJS; load it only when its tab opens (ADR 0024).
const Office = lazy(() => import("./office/Office"));
const Usage = lazy(() => import("./Usage"));

const stateLabel: Record<AgentState, string> = {
  starting: "Starting",
  idle: "Idle",
  working: "Working",
  waiting_permission: "Waiting for permission",
  exited: "Exited",
};

const exitLabel: Record<string, string> = {
  process_exit: "process exited",
  stopped: "stopped",
  daemon_shutdown: "daemon shut down",
  daemon_restart: "daemon restarted",
  start_failed: "start failed",
  reported_done: "reported done",
  review_submitted: "review submitted",
  follow_up: "ended for a follow-up",
  goal_done: "goal marked done",
  plan_approved: "plan approved",
};

// What each kind of agent does (ADR 0029); the role name is shown next to it.
const kindHelp: Record<Kind, string> = {
  architect: "Agrees the plan with you before work starts; reads the repository only",
  lead: "Tech Lead: executes the goal's plan, assigns sub-tasks, stays until the goal is done",
  worker: "Works on one task on its own branch",
  qa: "Merges the reported branches, tests them and reports bugs",
  reviewer: "Reviews one pull request; never edits, pushes or merges",
  assistant: "Briefs you across goals and proposes project memory; no code",
};

const importOutcome: Record<string, string> = {
  no_commits: "no commits yet",
  created: "branch created",
  fast_forward: "fast-forwarded",
  up_to_date: "up to date",
  diverged: "history rewritten",
};

const importTrigger: Record<string, string> = {
  exit: "on exit",
  manual: "import now",
  restart: "after restart",
  remove: "before removal",
  save_work: "save work",
  report_done: "report_done",
};

const eventLabel: Record<string, string> = {
  egress_denied: "Network blocked",
  egress_failed: "Network error",
  api_rejected: "API request refused",
  api_auth: "Unexpected API credentials",
  api_error: "API error",
  import_diverged: "History rewritten",
  commit_flagged: "Commit flagged",
  import_failed: "Import failed",
  api_budget: "Budget spent",
  api_meter: "Usage not readable",
};

const timeOf = (iso: string): string => new Date(iso).toLocaleTimeString();

// How long an agent may stay in "starting" before the UI flags it.
const stuckAfterMs = 20_000;

type View = { kind: "board" } | { kind: "office" } | { kind: "usage" } | { kind: "agent"; id: string };

export function App(): JSX.Element {
  const { agents, tasks, events, messages, accessRequests, pullRequests, usage, inbox, specs, qaRuns, memory, connection } =
    useWorld();
  const [roles, setRoles] = useState<RoleInfo[]>([]);
  const [panel, setPanel] = useState<"inbox" | "memory">();
  const [view, setView] = useState<View>({ kind: "board" });
  const [project, setProject] = useState<Project>();
  const [version, setVersion] = useState("");
  const [status, setStatus] = useState<Status>();
  const [showPack, setShowPack] = useState(false);
  const [error, setError] = useState<string>();
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    // Fire-and-forget: header details are cosmetic; failures leave them empty.
    void api.health().then((h) => setVersion(h.version), () => setVersion(""));
    void api.project().then(setProject, () => setProject(undefined));
    const t = window.setInterval(() => setNowMs(Date.now()), 5000);
    return () => window.clearInterval(t);
  }, []);

  // The sandbox mode and warnings belong to the daemon run: reload them on
  // every (re)connect, since a reconnect may mean a restarted daemon.
  useEffect(() => {
    if (connection === "open") void api.status().then(setStatus, () => setStatus(undefined));
    // Roles belong to the daemon run as well (ADR 0029); a failure only hides the role picker.
    if (connection === "open") void api.roles().then(setRoles, () => setRoles([]));
  }, [connection]);

  // Fall back to the board if the open agent disappears.
  useEffect(() => {
    if (view.kind === "agent" && !agents.some((a) => a.id === view.id)) setView({ kind: "board" });
  }, [agents, view]);

  // run executes an API call from a click handler and shows its error. It
  // never rejects, so callers can discard the promise with `void`.
  const run = async <T,>(fn: () => Promise<T>): Promise<T | undefined> => {
    setError(undefined);
    try {
      return await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return undefined;
    }
  };

  const startAgent = async (taskId?: string, role: Role = "worker"): Promise<void> => {
    const a = await run(() => api.startAgent(taskId, role));
    if (a) setView({ kind: "agent", id: a.id });
  };

  const removeAgent = (id: string): Promise<unknown> =>
    run(() =>
      api.removeAgent(id).catch((e: unknown) => {
        // 409 means the worktree has uncommitted changes and was kept; the
        // card then offers "Discard changes". Not an error worth a banner.
        if (e instanceof ApiError && e.status === 409) return undefined;
        throw e;
      }),
    );

  const running = agents.filter((a) => a.state !== "exited").length;
  const leadRunning = agents.some((a) => a.kind === "lead" && a.state !== "exited");
  const architectRunning = agents.some((a) => a.kind === "architect" && a.state !== "exited");
  const unreadCount = unread(inbox);
  const proposed = memory.filter((m) => m.status === "proposed").length;
  const ok = async (fn: () => Promise<unknown>): Promise<boolean> => (await run(fn)) !== undefined;
  const togglePanel = (p: "inbox" | "memory"): void => setPanel((cur) => (cur === p ? undefined : p));

  // approveSpec approves a plan (ADR 0031); a Tech Lead that could not start is shown as an error.
  const approveSpec = async (s: Spec): Promise<ApproveResult> => {
    const res = await api.approveSpec(s.id);
    if (res.lead_error) setError(`Plan approved; the Tech Lead did not start: ${res.lead_error}`);
    return res;
  };

  // reviewPlan opens the board at the goal's card, whose plan is open while it waits for approval.
  const reviewPlan = (goalId: string): void => {
    setView({ kind: "board" });
    window.setTimeout(() => {
      const card = document.getElementById(`task-${goalId}`);
      card?.scrollIntoView({ block: "center" });
      card?.focus({ preventScroll: true });
    }, 0);
  };
  const pending = accessRequests.filter((r) => r.status === "pending");
  const current = view.kind === "agent" ? agents.find((a) => a.id === view.id) : undefined;
  const taskTitle = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const sessions = sessionList(usage);
  const usageByAgent = usageBy(sessions, (s) => s.agent_id);
  const usageByTask = usageBy(sessions, (s) => s.task_id);
  const usageByGoal = usageBy(sessions, (s) => s.goal_id);

  return (
    <div className="app">
      <header>
        <h1>agent-office</h1>
        {project && <span className="mono small" title={project.repo_path}>{project.name}</span>}
        <span className="muted small">{version && `v${version}`}</span>
        {status?.sandbox === "podman" && (
          <span className="badge sandboxed" title={status.image ? `worker image ${status.image}` : undefined}>
            sandboxed · podman
          </span>
        )}
        {status?.sandbox === "podman" && (
          <button className="badge pack" onClick={() => setShowPack((v) => !v)} aria-expanded={showPack}
            title="What sandboxed agents get: image, network, secrets, skills, MCP servers">
            {status.pack ? `pack · ${status.pack.name}` : "no stack pack"}
          </button>
        )}
        {status?.sandbox === "host" && <span className="badge unsandboxed">unsandboxed · host mode</span>}
        {status?.github && (
          <span className="badge github" title={`Pushes to ${status.github.remote}; PRs into ${status.github.base}; ` +
            `${status.github.rounds} automatic follow-up round(s). agent-office never merges.`}>
            GitHub · {status.github.repo} · review: {status.github.reviewer}
          </span>
        )}
        <span className="header-actions">
          <button className={`badge inbox-button ${unreadCount ? "has-unread" : ""}`} aria-expanded={panel === "inbox"}
            onClick={() => togglePanel("inbox")} title="Messages to you from the team (ADR 0030)">
            Inbox{unreadCount > 0 && ` · ${unreadCount}`}
          </button>
          <button className="badge" aria-expanded={panel === "memory"} onClick={() => togglePanel("memory")}
            title="Notes every agent starts with (ADR 0034)">
            Memory{proposed > 0 && ` · ${proposed} proposed`}
          </button>
          <button className="badge" title="Ask the assistant for a briefing (starts it if needed)"
            onClick={() => void run(async () => { await api.askBriefing(); setPanel("inbox"); return true; })}>
            Briefing
          </button>
        </span>
        <span className={`conn conn-${connection}`}>{connection === "open" ? "connected" : connection}</span>
      </header>
      {status?.sandbox === "host" && (
        <div className="banner warn">
          Host mode: agents run directly on this machine, with your files, SSH keys and network, and ask before
          using tools. Restart the daemon with <span className="mono">--sandbox podman</span> to isolate them.
        </div>
      )}
      {showPack && status?.sandbox === "podman" && <PackPanel status={status} />}
      {panel === "inbox" && (
        <InboxPanel items={inbox} tasks={tasks} specs={specs} onOpenAgent={(id) => setView({ kind: "agent", id })}
          onRead={(i) => void run(() => api.readInbox(i.id))}
          onReply={(i, body) => ok(() => api.replyInbox(i.id, body))}
          onReviewPlan={reviewPlan}
          onApprovePlan={(s, i) => void run(async () => {
            const res = await approveSpec(s);
            if (!i.read_at) await api.readInbox(i.id);
            return res;
          })} />
      )}
      {panel === "memory" && (
        <MemoryPanel entries={memory}
          onAdd={(k, t, b) => ok(() => api.addMemory(k, t, b))}
          onEdit={(m, patch) => ok(() => api.editMemory(m.id, patch))}
          onAccept={(m) => void run(() => api.acceptMemory(m.id))}
          onDelete={(m) => void run(async () => { await api.deleteMemory(m.id); return true; })} />
      )}
      {status?.warnings.map((w) => <div key={w} className="banner warn">{w}</div>)}
      {pending.length > 0 && (
        <Approvals requests={pending} agents={agents} taskTitle={taskTitle}
          onApprove={(r) => void run(() => api.approveAccess(r.id))}
          onDeny={(r) => void run(() => api.denyAccess(r.id))}
        />
      )}
      <main>
        <aside>
          <button className="primary" onClick={() => void startAgent()}>Start agent (no task)</button>
          <p className="muted small">{running} running</p>
          {error && <p className="error">{error}</p>}
          <ul className="agents">
            {agents.map((a) => (
              <AgentCard key={a.id} agent={a} taskTitle={taskTitle(a.task_id)} nowMs={nowMs}
                selected={current?.id === a.id}
                onSelect={() => setView({ kind: "agent", id: a.id })}
                onStop={() => void run(() => api.stopAgent(a.id))}
                onRemove={() => void removeAgent(a.id)}
                onDiscard={() => void run(() => api.discardAgent(a.id))}
                onImport={() => void run(() => api.importAgent(a.id))}
                onSaveWork={() => void run(() => api.saveWork(a.id))}
                events={events[a.id] ?? []}
                messages={messages[a.id] ?? []}
                usage={usageByAgent.get(a.id)}
              />
            ))}
          </ul>
          {agents.length === 0 && <p className="muted">No agents yet.</p>}
        </aside>
        <section className="main-pane">
          <nav className="tabs">
            <button className={view.kind === "board" ? "active" : ""} onClick={() => setView({ kind: "board" })}>
              Board
            </button>
            <button className={view.kind === "office" ? "active" : ""} onClick={() => setView({ kind: "office" })}>
              Office
            </button>
            <button className={view.kind === "usage" ? "active" : ""} onClick={() => setView({ kind: "usage" })}>
              Usage
            </button>
            {agents.filter((a) => a.has_terminal).map((a) => (
              <button key={a.id} className={current?.id === a.id ? "active" : ""}
                onClick={() => setView({ kind: "agent", id: a.id })}>
                <span className={`dot state-${a.state}`} /> {taskTitle(a.task_id) ?? a.id.slice(0, 6)}
              </button>
            ))}
          </nav>
          {view.kind === "office" ? (
            <Suspense fallback={<div className="empty">Loading the office…</div>}>
              <Office agents={agents} tasks={tasks} messages={messages} accessRequests={accessRequests}
                onOpenAgent={(id) => setView({ kind: "agent", id })} />
            </Suspense>
          ) : view.kind === "usage" ? (
            <Suspense fallback={<div className="empty">Loading usage…</div>}>
              <Usage usage={usage} agents={agents} tasks={tasks} status={status}
                onOpenAgent={(id) => setView({ kind: "agent", id })} />
            </Suspense>
          ) : view.kind === "board" || !current ? (
            <Board tasks={tasks} agents={agents} canStart={true} leadRunning={leadRunning} usageByTask={usageByTask}
              usageByGoal={usageByGoal}
              onStartLead={(t) => void startAgent(t.id, "lead")}
              onCreate={async (title, description) => (await run(() => api.createTask(title, description))) !== undefined}
              onStatus={(t: Task, status: TaskStatus) => void run(() => api.updateTask(t.id, { status }))}
              onStart={(t) => void startAgent(t.id)}
              onDelete={(t) => void run(() => api.deleteTask(t.id))}
              onOpenAgent={(id) => setView({ kind: "agent", id })}
              pullRequests={pullRequests} github={status?.github !== undefined}
              onOpenPR={(t) => void run(() => api.openPullRequest(t.id))}
              onRequestReview={(pr) => void run(() => api.requestReview(pr.id))}
              onSendToWorker={(pr, r) => void run(() => api.sendToWorker(pr.id, r.id))}
              onAcceptFlagged={(pr) => void run(() => api.acceptFlagged(pr.id, pr.flagged.map((f) => f.commit)))}
              roles={roles} specs={specs} qaRuns={qaRuns} inbox={inbox} architectRunning={architectRunning}
              onStartRole={(t, role) => void startAgent(t.id, role)}
              onSaveSpec={(s, plan) => ok(() => api.editSpec(s.id, plan))}
              onApproveSpec={(s) => void run(() => approveSpec(s))}
              onResolve={(t) => void run(async () => { await api.resolveConflict(t.id); return true; })}
              onReadInbox={(i) => void run(() => api.readInbox(i.id))}
              onReplyInbox={(i, body) => ok(() => api.replyInbox(i.id, body))}
            />
          ) : current.has_terminal ? (
            <TerminalView key={current.id} agentId={current.id} />
          ) : (
            <div className="empty">This agent ran before the daemon restarted; its terminal is no longer available.</div>
          )}
        </section>
      </main>
    </div>
  );
}

interface CardProps {
  agent: Agent;
  taskTitle?: string;
  nowMs: number;
  selected: boolean;
  onSelect: () => void;
  onStop: () => void;
  onRemove: () => void;
  onDiscard: () => void;
  onImport: () => void;
  onSaveWork: () => void;
  events: SandboxEvent[];
  messages: Message[];
  usage?: UsageRow; // all of the agent's sessions (ADR 0026)
}

function AgentCard({
  agent: a, taskTitle, nowMs, selected, onSelect, onStop, onRemove, onDiscard, onImport, onSaveWork, events, messages, usage,
}: CardProps): JSX.Element {
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const stuck = a.state === "starting" && nowMs - Date.parse(a.started_at) > stuckAfterMs;
  const sandboxed = a.sandbox === "podman";
  const repoLabel = sandboxed ? "Agent repository" : "Worktree";
  return (
    <li className={`card ${selected ? "selected" : ""}`} onClick={onSelect}>
      <div className="card-head">
        <span className="mono">{a.id}</span>
        <span className={`badge state-${a.state}`}>{stateLabel[a.state]}</span>
      </div>
      <div className="row">
        <span className={`chip role-${a.kind}`} title={kindHelp[a.kind]}>{a.role}</span>
        {sandboxed
          ? <span className="chip sandboxed" title="Rootless Podman worker; permission prompts off inside it">sandboxed</span>
          : <span className="chip unsandboxed" title="Runs on the host with permission prompts on">unsandboxed</span>}
      </div>
      {taskTitle && <div className="small">{taskTitle}</div>}
      {a.activity && <div className="small">Tool: <span className="mono">{a.activity}</span></div>}
      {usage && <UsageLine usage={usage} />}
      {a.state === "starting" && (
        <div className={`small hint ${stuck ? "stuck" : ""}`}>
          {stuck ? "⚠ Still starting after 20 s. " : "Still starting? "}
          Claude Code may be waiting for an answer in the terminal, e.g. whether to trust this folder.
          Answer it there; agent-office never accepts it for you.
        </div>
      )}
      <div className="small muted mono"
        title={sandboxed
          ? `Agent repository: ${a.worktree_path}\nDo not cd into or open it; use the branch in your repository.`
          : a.worktree_path}>
        {a.branch}
      </div>
      <div className="small muted">
        {a.model ? `model ${a.model}` : "default model"} · user settings {a.inherit_user_settings ? "on" : "off"}
      </div>
      {a.state === "exited" && (
        <div className="small muted">
          {exitLabel[a.exit_reason ?? ""] ?? a.exit_reason ?? "exited"}
          {a.exit_code !== undefined && ` · exit code ${a.exit_code}`}
        </div>
      )}
      {a.last_import && <ImportLine imp={a.last_import} branch={a.branch} />}
      {a.worktree_status === "kept" && (
        <div className="small hint">
          {repoLabel} has uncommitted changes and was kept.
          {sandboxed && " Save work commits them to the branch; discard deletes them."}
        </div>
      )}
      {events.length > 0 && <EventList events={events} />}
      {messages.length > 0 && <MessageList agentId={a.id} messages={messages} />}
      <div className="card-actions row" onClick={(e) => e.stopPropagation()}>
        {a.state !== "exited" && <button onClick={onStop}>Stop</button>}
        {sandboxed && a.worktree_status !== "removed" && (
          <button onClick={onImport} title="Bring the agent's commits into your repository now">Import now</button>
        )}
        {sandboxed && a.state === "exited" && a.worktree_status === "kept" && !confirmDiscard && (
          <button onClick={onSaveWork} title="Commit uncommitted changes as one WIP commit and import it">Save work</button>
        )}
        {a.state === "exited" && a.worktree_status !== "kept" && <button onClick={onRemove}>Remove</button>}
        {a.state === "exited" && a.worktree_status === "kept" && !confirmDiscard && (
          <>
            <button onClick={onRemove}>Retry remove</button>
            <button className="danger" onClick={() => setConfirmDiscard(true)}>Discard changes…</button>
          </>
        )}
        {confirmDiscard && (
          <>
            <span className="small">Delete uncommitted work in this {sandboxed ? "agent repository" : "worktree"}?</span>
            <button className="danger" onClick={() => { setConfirmDiscard(false); onDiscard(); }}>Yes, discard</button>
            <button onClick={() => setConfirmDiscard(false)}>Cancel</button>
          </>
        )}
      </div>
    </li>
  );
}

// ImportLine summarizes the last import of a sandboxed agent's branch.
function ImportLine({ imp, branch }: { imp: ImportInfo; branch: string }): JSX.Element {
  const flagged = imp.flagged ?? [];
  return (
    <>
      <div className="small muted" title={imp.head}>
        Import {importTrigger[imp.trigger] ?? imp.trigger}: {importOutcome[imp.outcome] ?? imp.outcome}
        {imp.commits > 0 && ` · ${imp.commits} commit${imp.commits === 1 ? "" : "s"}`} · {timeOf(imp.at)}
      </div>
      {imp.outcome === "diverged" && imp.branch && (
        <div className="small hint">
          The agent rewrote its history. It was imported as <span className="mono">{imp.branch}</span>;{" "}
          <span className="mono">{branch}</span> was left unchanged.
        </div>
      )}
      {flagged.length > 0 && (
        <div className="small hint">
          ⚠ {flagged.length} imported commit{flagged.length === 1 ? "" : "s"} without your identity or the agent
          trailer: {flagged.map((f) => `${f.commit.slice(0, 8)} (${f.problem})`).join(", ")}
        </div>
      )}
    </>
  );
}

// EventList shows an agent's sandbox events, newest first. A refusal here
// means the agent tried something the sandbox does not allow.
function EventList({ events }: { events: SandboxEvent[] }): JSX.Element {
  return (
    <details className="events small" onClick={(e) => e.stopPropagation()}>
      <summary className="hint">
        {events.length === 1 ? "1 sandbox event" : `${events.length} sandbox events`}
        {` · latest: ${eventLabel[events[0].kind] ?? events[0].kind}`}
      </summary>
      <ul>
        {events.map((e) => (
          <li key={`${e.at}|${e.kind}|${e.detail}`}>
            <span className="muted">{timeOf(e.at)}</span> {eventLabel[e.kind] ?? e.kind}
            {e.detail && <>: <span className="mono">{e.detail}</span></>}
          </li>
        ))}
      </ul>
    </details>
  );
}

// Approvals is the human approval queue for access requests (ADR 0019).
// Nothing is granted until a button here is pressed; the reason is the
// agent's own text.
function Approvals({ requests, agents, taskTitle, onApprove, onDeny }: {
  requests: AccessRequest[];
  agents: Agent[];
  taskTitle: (id?: string) => string | undefined;
  onApprove: (r: AccessRequest) => void;
  onDeny: (r: AccessRequest) => void;
}): JSX.Element {
  return (
    <section className="approvals">
      <h2>Waiting for your approval</h2>
      <ul>
        {requests.map((r) => {
          const a = agents.find((x) => x.id === r.agent_id);
          return (
            <li key={r.id}>
              <div>
                Agent <span className="mono">{r.agent_id.slice(0, 6)}</span>
                {a && <> ({a.role}{taskTitle(a.task_id) ? `, “${taskTitle(a.task_id)}”` : ""})</>} asks for{" "}
                {r.kind === "domain" ? <>network access to <span className="mono strong">{r.value}</span></> : `${r.kind} ${r.value}`}
              </div>
              <div className="small muted">Agent&apos;s reason: <span className="quote">{r.reason}</span></div>
              <div className="row">
                <button onClick={() => onApprove(r)} title="Adds the host to this agent's allowlist until it exits">
                  Allow for this agent
                </button>
                <button className="danger" onClick={() => onDeny(r)}>Deny</button>
                <span className="small muted">Requested {timeOf(r.created_at)} · applies to this agent only</span>
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const deliveryLabel: Record<string, string> = { stop_hook: "at end of turn", pty: "typed while idle" };

// MessageList shows messages an agent sent or received, newest first. Bodies
// are agent-written text.
function MessageList({ agentId, messages }: { agentId: string; messages: Message[] }): JSX.Element {
  const waiting = messages.filter((m) => m.to === agentId && !m.delivered_at).length;
  return (
    <details className="events small" onClick={(e) => e.stopPropagation()}>
      <summary className="muted">
        {messages.length === 1 ? "1 message" : `${messages.length} messages`}
        {waiting > 0 && ` · ${waiting} waiting for delivery`}
      </summary>
      <ul>
        {messages.map((m) => (
          <li key={m.id}>
            <span className="muted">
              {timeOf(m.created_at)} {m.to === agentId
                ? `from ${m.from_user ? "you" : m.from ? m.from.slice(0, 6) : "agent-office"}`
                : `to ${m.to.slice(0, 6)}`}
              {" · "}
              {m.delivered_at ? `delivered ${deliveryLabel[m.delivery ?? ""] ?? ""}` : "not delivered yet"}
            </span>
            <div className="quote">{m.body}</div>
          </li>
        ))}
      </ul>
    </details>
  );
}

// PackPanel shows what the stack pack gives sandboxed agents (ADR 0023).
function PackPanel({ status }: { status: Status }) {
  const p = status.pack;
  const list = (xs: string[] | undefined) => (xs && xs.length > 0 ? xs.join(", ") : "none");
  return (
    <div className="pack-panel">
      {p ? (
        <p>
          <span className="strong">Stack pack {p.name}</span>{" "}
          <span className="muted">({p.source === "builtin" ? "built in" : p.source})</span>: {p.description}
        </p>
      ) : (
        <p>
          No stack pack: agents get the base image (no language toolchain) and the default allowlist. Start the
          daemon with <span className="mono">--pack &lt;name&gt;</span>; <span className="mono">agent-office pack list</span>{" "}
          shows the packs.
        </p>
      )}
      <dl>
        <dt>Image</dt>
        <dd className="mono">{status.image}</dd>
        <dt>Network</dt>
        <dd className="mono">{list(status.egress)}</dd>
        {p && (
          <>
            <dt>Secrets</dt>
            <dd>
              {p.secrets.length === 0
                ? "none"
                : p.secrets.map((s) => (
                    <span key={s.name} className={`secret ${s.present ? "present" : "missing"}`}
                      title={s.problem ?? s.description ?? undefined}>
                      <span className="mono">{s.name}</span>{" "}
                      {s.present ? "set" : s.problem ? "file not usable" : s.required ? "missing (required)" : "not set"}
                    </span>
                  ))}
            </dd>
            <dt>Skills</dt>
            <dd className="mono">{list(p.skills)}</dd>
            <dt>MCP servers</dt>
            <dd className="mono">{list(p.mcp_servers)}</dd>
          </>
        )}
      </dl>
      {p && p.secrets.length > 0 && (
        <p className="muted small">
          Workers and reviewers get the secrets in their environment and could leak them through an allowed host:
          give agents test or read-only credentials only.
        </p>
      )}
    </div>
  );
}

// UsageLine is an agent's or task's tokens and estimated cost (ADR 0026).
export function UsageLine({ usage: u }: { usage: UsageRow }): JSX.Element {
  return (
    <div className="small muted usage-line" title="estimated from token counts; see the Usage tab">
      {formatTokens(totalTokens(u.tokens))} tokens · {formatMoney(u.costMicroUSD)}
      {u.unpricedTokens > 0 && <span className="hint"> + unpriced</span>}
    </div>
  );
}
