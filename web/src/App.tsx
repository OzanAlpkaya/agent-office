import { type JSX, Suspense, lazy, useEffect, useLayoutEffect, useMemo, useState } from "react";
import {
  type AccessRequest, type Agent, type AgentState, ApiError, type ApproveResult, type AuthSession, type ImportInfo, type Kind, type Message,
  type DaemonStatus, type Project, type Role, type RoleInfo, type SandboxEvent, type Spec, type Status, type Task, type TaskStatus,
  api as daemonApi, projectApi,
} from "./api";
import { ProjectApiContext } from "./ProjectContext";
import { ProjectsPanel } from "./Projects";
import { type Elsewhere, waitingTotal } from "./projects";
import { AgentName, AgentsContext } from "./AgentName";
import { AgentSwitcher } from "./AgentSwitcher";
import { type Tabs, closeTab, keepTabs, labelOf, moveTab, neighbourTab, openTab, parseTabs } from "./agents";
import { Board } from "./Board";
import { Drawer } from "./Drawer";
import { DevicesPanel } from "./Devices";
import { narrowQuery, useMedia } from "./useMedia";
import { InboxPanel, MemoryPanel } from "./Team";
import { unread } from "./workflow";
import { type Waiting, waitingOnUser } from "./waiting";
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
  resume_failed: "resume failed: Claude Code could not load the previous conversation",
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

// The open terminal tabs are remembered per browser and project (ADR 0044,
// 0048): a convenience, so storage that is missing or blocked only means
// starting with none.
const tabsKey = (projectId: string): string => `agent-office.tabs.${projectId}`;
function loadTabs(projectId: string): Tabs {
  try {
    return parseTabs(window.localStorage.getItem(tabsKey(projectId)));
  } catch {
    return parseTabs(null); // storage blocked (private window, policy)
  }
}
function saveTabs(projectId: string, t: Tabs): void {
  try {
    window.localStorage.setItem(tabsKey(projectId), JSON.stringify(t));
  } catch {
    // storage blocked or full: the tabs are only lost on reload
  }
}

// With more agent tabs than this, only the open tab shows its task title.
const crowdedTabs = 3;

// typingTarget says whether a key press belongs to a text field or a terminal.
function typingTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  return t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName) || t.closest(".xterm") !== null;
}

// MainTab is a tab of the main pane that every project has.
export type MainTab = "board" | "office" | "usage";

// "agents" is the phone layout's tab for the agent list (the sidebar on wider screens, ADR 0040).
type View = { kind: "board" } | { kind: "office" } | { kind: "usage" } | { kind: "agents" } | { kind: "agent"; id: string };

type Panel = "inbox" | "memory" | "devices" | "projects";

export interface AppProps {
  session: AuthSession;
  onSignedOut: () => void;
  project: Project; // the project this App shows (ADR 0046); the Root remounts the App for another
  projects: Project[];
  daemon?: DaemonStatus;
  elsewhere: Elsewhere[]; // the other projects' waits, for the Approvals badge (ADR 0048)
  initialAgent?: string; // open this agent once it is known (a switch from another project's approval)
  initialTab?: MainTab; // the tab the other project showed, kept across a switch (e.g. the office's floors)
  onSelectProject: (id: string, agentId?: string, tab?: MainTab) => void;
}

export function App({
  session, onSignedOut, project, projects, daemon, elsewhere, initialAgent, initialTab, onSelectProject,
}: AppProps): JSX.Element {
  // The project's routes, plus the daemon's (health, devices, projects) under one name.
  const papi = useMemo(() => projectApi(project.id), [project.id]);
  const api = useMemo(() => ({ ...daemonApi, ...papi }), [papi]);
  const { agents, tasks, events, messages, accessRequests, pullRequests, usage, inbox, specs, qaRuns, memory, connection } =
    useWorld(papi);
  const [roles, setRoles] = useState<RoleInfo[]>([]);
  const [panel, setPanel] = useState<Panel>();
  const narrow = useMedia(narrowQuery);
  const [view, setView] = useState<View>({ kind: initialTab ?? "board" });
  // A switch to another project keeps the main tab (Board, Office, Usage) the user is on.
  const tab: MainTab = view.kind === "office" || view.kind === "usage" ? view.kind : "board";
  const [tabs, setTabs] = useState<Tabs>(() => loadTabs(project.id));
  const [switcher, setSwitcher] = useState(false);
  const [approvalsOpen, setApprovalsOpen] = useState(false);
  const [dragged, setDragged] = useState<string>();
  const [version, setVersion] = useState("");
  const [status, setStatus] = useState<Status>();
  const [showPack, setShowPack] = useState(false);
  const [error, setError] = useState<string>();
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    // Fire-and-forget: header details are cosmetic; failures leave them empty.
    void api.health().then((h) => setVersion(h.version), () => setVersion(""));
    const t = window.setInterval(() => setNowMs(Date.now()), 5000);
    return () => window.clearInterval(t);
  }, []);

  // The sandbox mode and warnings belong to the daemon run: reload them on
  // every (re)connect, since a reconnect may mean a restarted daemon.
  useEffect(() => {
    if (connection === "open") void api.status().then(setStatus, () => setStatus(undefined));
    // Roles belong to the daemon run as well (ADR 0029); a failure only hides the role picker.
    if (connection === "open") void api.roles().then(setRoles, () => setRoles([]));
  }, [connection, api]);

  // Fall back to the board if the open agent disappears, or the agents tab on a wide screen.
  useEffect(() => {
    if (view.kind === "agent" && !agents.some((a) => a.id === view.id)) setView({ kind: "board" });
    if (view.kind === "agents" && !narrow) setView({ kind: "board" });
  }, [agents, view, narrow]);

  // Tabs of removed agents close, once the daemon has sent its agents.
  useEffect(() => {
    if (connection === "open" && agents.length > 0) setTabs((t) => keepTabs(t, new Set(agents.map((a) => a.id))));
  }, [agents, connection]);
  useEffect(() => saveTabs(project.id, tabs), [project.id, tabs]);

  // The open agent's tab is scrolled into view when the strip overflows.
  const openId = view.kind === "agent" ? view.id : undefined;
  useEffect(() => {
    if (!openId) return;
    document.querySelector(`.agent-tab button[data-agent="${openId}"]`)?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [openId, tabs]);

  // "/" opens the agent switcher, except while typing or in a terminal. A
  // layout effect, so the shortcut works from the first painted frame.
  useLayoutEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || typingTarget(e.target)) return;
      e.preventDefault();
      setSwitcher(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Everything waiting on the user, from one list: the badge, the drawer, the
  // tab title, the cards, the switcher and the Office all count it (ADR 0044).
  // Memoised: the Office redraws when this list changes, so it must not be new on every render.
  const waiting = useMemo(() => waitingOnUser(agents, accessRequests, inbox, specs), [agents, accessRequests, inbox, specs]);
  // The badge and the title count every project (ADR 0048); the drawer lists this project's first.
  const waitingCount = waitingTotal(waiting.length, elsewhere);
  // The tab title shows the count, so it is seen from other browser tabs too.
  useEffect(() => {
    const name = projects.length > 1 ? ` · ${project.name}` : ""; // with several projects, which one this tab shows
    document.title = waitingCount > 0 ? `(${waitingCount}) agent-office${name}` : `agent-office${name}`;
  }, [waitingCount, project.name, projects.length]);

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

  // openAgent shows an agent's terminal and gives it a tab (ADR 0044).
  const openAgent = (id: string): void => {
    setView({ kind: "agent", id });
    setTabs((t) => openTab(t, id));
  };

  const closeAgentTab = (id: string): void => {
    if (view.kind === "agent" && view.id === id) {
      const next = neighbourTab(tabs, id);
      if (next) openAgent(next);
      else setView({ kind: "board" });
    }
    setTabs((t) => closeTab(t, id));
  };

  // An approval of this project picked in another project's drawer opens its agent here.
  const [pendingAgent, setPendingAgent] = useState(initialAgent);
  useEffect(() => {
    if (pendingAgent && agents.some((a) => a.id === pendingAgent)) {
      setPendingAgent(undefined);
      openAgent(pendingAgent);
    }
  }); // every render: openAgent is recreated each time, and the check is cheap

  const startAgent = async (taskId?: string, role: Role = "worker"): Promise<void> => {
    const a = await run(() => api.startAgent(taskId, role));
    if (a) openAgent(a.id);
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
  const togglePanel = (p: Panel): void => setPanel((cur) => (cur === p ? undefined : p));

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
  const current = view.kind === "agent" ? agents.find((a) => a.id === view.id) : undefined;
  const waitingAgents = new Set(waiting.map((w) => w.agent.id));
  const label = (id: string): string => labelOf(agents, id);
  const byId = new Map(agents.map((a) => [a.id, a]));
  const tabAgents = tabs.order.map((id) => byId.get(id)).filter((a): a is Agent => a !== undefined);
  // A phone has room for the open agent only; the switcher reaches the others.
  const shownTabs = narrow ? tabAgents.filter((a) => a.id === current?.id) : tabAgents;
  const activeAgents = agents.filter((a) => a.state !== "exited");
  const exitedAgents = agents.filter((a) => a.state === "exited");
  const moveTabBy = (id: string, delta: number): void => setTabs((t) => moveTab(t, id, t.order.indexOf(id) + delta));
  const taskTitle = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const sessions = sessionList(usage);
  const usageByAgent = usageBy(sessions, (s) => s.agent_id);
  const usageByTask = usageBy(sessions, (s) => s.task_id);
  const usageByGoal = usageBy(sessions, (s) => s.goal_id);

  return (
    <ProjectApiContext.Provider value={papi}>
    <AgentsContext.Provider value={agents}>
      <div className="app">
        <header>
          <h1>agent-office</h1>
          <ProjectSwitcher project={project} projects={projects} elsewhere={elsewhere} onSelect={(id) => onSelectProject(id, undefined, tab)}
            onManage={() => togglePanel("projects")} managing={panel === "projects"} />
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
            <button className={`badge approvals-button ${waitingCount ? "has-pending" : ""}`} aria-expanded={approvalsOpen}
              aria-label={waitingCount ? `Approvals: ${waitingCount} waiting` : "Approvals: none waiting"}
              onClick={() => setApprovalsOpen((v) => !v)}
              title="Agents waiting on you: access requests, permission prompts, questions and plans to approve (ADR 0019, 0044)">
              Approvals{waitingCount > 0 && <span className="count">{waitingCount}</span>}
            </button>
            <button className={`badge inbox-button ${unreadCount ? "has-unread" : ""}`} aria-expanded={panel === "inbox"}
              onClick={() => togglePanel("inbox")} title="Messages to you from the team (ADR 0030)">
              Inbox{unreadCount > 0 && ` · ${unreadCount}`}
            </button>
            <button className="badge" aria-expanded={panel === "memory"} onClick={() => togglePanel("memory")}
              title="Notes every agent starts with (ADR 0034)">
              Memory{proposed > 0 && ` · ${proposed} proposed`}
            </button>
            <button className="badge" aria-expanded={panel === "devices"} onClick={() => togglePanel("devices")}
              title={`Paired browsers and phones${session.device ? `; this one: ${session.device.name}` : ""} (ADR 0039)`}>
              Devices
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
          <InboxPanel items={inbox} tasks={tasks} specs={specs} onOpenAgent={openAgent}
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
            onAdd={(d) => ok(() => api.addMemory(d))}
            onEdit={(m, patch) => ok(() => api.editMemory(m.id, patch))}
            onAccept={(m) => void run(() => api.acceptMemory(m.id))}
            onDelete={(m) => void run(async () => { await api.deleteMemory(m.id); return true; })} />
        )}
        {panel === "projects" && (
          <ProjectsPanel projects={projects} current={project.id} status={daemon}
            running={elsewhere.reduce((n, e) => n + e.running, running)}
            onSelect={(id) => onSelectProject(id, undefined, tab)} />
        )}
        {panel === "devices" && (
          <DevicesPanel remoteHosts={status?.remote_hosts ?? []} run={run} onSignedOut={onSignedOut} />
        )}
        {status?.warnings.map((w) => <div key={w} className="banner warn">{w}</div>)}
        {approvalsOpen && (
          <Drawer label="Approvals" onClose={() => setApprovalsOpen(false)}
            title={<>Waiting for you{waitingCount > 0 && <span className="muted"> · {waitingCount}</span>}</>}>
            <Approvals waiting={waiting} elsewhere={waitingCount - waiting.length} taskTitle={taskTitle} onOpenAgent={(id) => {
              setApprovalsOpen(false);
              openAgent(id);
            }}
              onOpenInbox={() => {
                setApprovalsOpen(false);
                setPanel("inbox");
              }}
              onReviewPlan={(goalId) => {
                setApprovalsOpen(false);
                reviewPlan(goalId);
              }}
              onApprove={(r) => void run(() => api.approveAccess(r.id))}
              onDeny={(r) => void run(() => api.denyAccess(r.id))}
            />
            <OtherProjects elsewhere={elsewhere} run={run} onSwitch={(id, agentId) => {
              setApprovalsOpen(false);
              onSelectProject(id, agentId);
            }} />
          </Drawer>
        )}
        {switcher && (
          <AgentSwitcher agents={agents} tasks={tasks} current={current?.id} waiting={waitingAgents}
            onClose={() => setSwitcher(false)}
            onPick={(id) => {
              setSwitcher(false);
              openAgent(id);
            }} />
        )}
        <main>
          {(!narrow || view.kind === "agents") && <aside className={narrow ? "agents-pane" : undefined}>
            <button className="primary" onClick={() => void startAgent()}>Start agent (no task)</button>
            <p className="muted small">{running} running</p>
            {error && <p className="error">{error}</p>}
            {[activeAgents, exitedAgents].map((list, i) => {
              const cards = (
                <ul className="agents">
                  {list.map((a) => (
                    <AgentCard key={a.id} agent={a} taskTitle={taskTitle(a.task_id)} nowMs={nowMs}
                      selected={current?.id === a.id} label={label}
                      waiting={waiting.find((w) => w.agent.id === a.id)}
                      onApprovals={() => setApprovalsOpen(true)}
                      onSelect={() => openAgent(a.id)}
                      onStop={() => void run(() => api.stopAgent(a.id))}
                      onRemove={() => void removeAgent(a.id)}
                      onDiscard={() => void run(() => api.discardAgent(a.id))}
                      onImport={() => void run(() => api.importAgent(a.id))}
                      onSaveWork={() => void run(() => api.saveWork(a.id))}
                      onResume={() => void run(() => api.resumeAgent(a.id))}
                      events={events[a.id] ?? []}
                      messages={messages[a.id] ?? []}
                      usage={usageByAgent.get(a.id)}
                    />
                  ))}
                </ul>
              );
              if (i === 0) return <div key="active">{cards}</div>;
              if (list.length === 0) return null;
              // Exited agents stay reachable here, in the switcher and in Usage (ADR 0044).
              return (
                <details key="exited" className="exited-agents" open={list.some((a) => a.id === current?.id) || undefined}>
                  <summary className="muted small">Exited · {list.length}</summary>
                  {cards}
                </details>
              );
            })}
            {agents.length === 0 && <p className="muted">No agents yet.</p>}
            {narrow && <AgentsTabNav onBoard={() => setView({ kind: "board" })} />}
          </aside>}
          {(!narrow || view.kind !== "agents") && <section className="main-pane">
            <nav className="tabs">
              {narrow && (
                <button onClick={() => setView({ kind: "agents" })} title="Agents and their actions">
                  Agents · {running}
                </button>
              )}
              <button className={view.kind === "board" ? "active" : ""} onClick={() => setView({ kind: "board" })}>
                Board
              </button>
              <button className={view.kind === "office" ? "active" : ""} onClick={() => setView({ kind: "office" })}>
                Office
              </button>
              <button className={view.kind === "usage" ? "active" : ""} onClick={() => setView({ kind: "usage" })}>
                Usage
              </button>
              <span className={`agent-tabs ${shownTabs.length > crowdedTabs ? "crowded" : ""}`} role="tablist" aria-label="Open agents">
                {shownTabs.map((a) => (
                  <span key={a.id} role="presentation"
                    className={`agent-tab ${current?.id === a.id ? "active" : ""} ${dragged === a.id ? "dragging" : ""}`}
                    draggable={!narrow}
                    onDragStart={(e) => {
                      setDragged(a.id);
                      e.dataTransfer.effectAllowed = "move";
                      e.dataTransfer.setData("text/plain", a.id);
                    }}
                    onDragEnd={() => setDragged(undefined)}
                    onDragOver={(e) => {
                      if (dragged && dragged !== a.id) e.preventDefault();
                    }}
                    onDrop={(e) => {
                      e.preventDefault();
                      if (dragged) setTabs((t) => moveTab(t, dragged, t.order.indexOf(a.id)));
                      setDragged(undefined);
                    }}>
                    <button role="tab" aria-selected={current?.id === a.id} data-agent={a.id}
                      title={`${a.name} · ${a.role} · ${a.id}${taskTitle(a.task_id) ? `\n${taskTitle(a.task_id)}` : ""}` +
                        "\nDrag or Alt+←/→ to move; middle-click to close"}
                      onClick={() => openAgent(a.id)}
                      onAuxClick={(e) => {
                        if (e.button === 1) closeAgentTab(a.id);
                      }}
                      onKeyDown={(e) => {
                        if (!e.altKey || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
                        e.preventDefault();
                        moveTabBy(a.id, e.key === "ArrowLeft" ? -1 : 1);
                      }}>
                      <span className={`dot state-${a.state}`} />
                      <span className="agent-tab-name">{a.name || a.id.slice(0, 6)}</span>
                      {taskTitle(a.task_id) && <span className="agent-tab-task">{taskTitle(a.task_id)}</span>}
                    </button>
                    <button className="agent-tab-close" aria-label={`Close ${a.name || a.id.slice(0, 6)}`}
                      onClick={() => closeAgentTab(a.id)}>×</button>
                  </span>
                ))}
              </span>
              <button className="switcher-button" onClick={() => setSwitcher(true)} aria-haspopup="dialog"
              aria-label={`Find an agent (${running} running)`}
                title="Find an agent by name, id, role, task or branch ( / )">
                <span aria-hidden="true">⌕</span><span className="switcher-label"> Agents</span>
                <span className="count">{running}</span>
              </button>
            </nav>
            {view.kind === "office" ? (
              <Suspense fallback={<div className="empty">Loading the office…</div>}>
                <Office agents={agents} tasks={tasks} messages={messages} waiting={waiting}
                  onOpenAgent={openAgent}
                  floors={[{ id: project.id, name: project.name, waiting: waiting.length, running, current: true },
                    ...elsewhere.filter((e) => e.project.state === "open").map((e) => ({ id: e.project.id, name: e.project.name,
                      waiting: e.waiting.length, running: e.running, current: false }))]
                    .sort((a, b) => projects.findIndex((p) => p.id === a.id) - projects.findIndex((p) => p.id === b.id))}
                  onFloor={(id) => onSelectProject(id, undefined, "office")} />
              </Suspense>
            ) : view.kind === "usage" ? (
              <Suspense fallback={<div className="empty">Loading usage…</div>}>
                <Usage usage={usage} agents={agents} tasks={tasks} status={status}
                  onOpenAgent={openAgent} />
              </Suspense>
            ) : view.kind === "board" || !current ? (
              <Board tasks={tasks} agents={agents} canStart={true} leadRunning={leadRunning} usageByTask={usageByTask}
                usageByGoal={usageByGoal}
                onStartLead={(t) => void startAgent(t.id, "lead")}
                onCreate={async (title, description) => (await run(() => api.createTask(title, description))) !== undefined}
                onStatus={(t: Task, status: TaskStatus) => void run(() => api.updateTask(t.id, { status }))}
                onStart={(t) => void startAgent(t.id)}
                onDelete={(t) => void run(() => api.deleteTask(t.id))}
                onOpenAgent={openAgent}
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
            ) : (
              // Live while it runs, else its saved sessions (ADR 0036).
              <TerminalView key={`${current.id}-${current.session_id ?? ""}`} agent={current}
                onResume={() => void run(() => api.resumeAgent(current.id))} />
            )}
          </section>}
        </main>
      </div>
    </AgentsContext.Provider>
    </ProjectApiContext.Provider>
  );
}

// AgentsTabNav leads back from the phone layout's agent list.
function AgentsTabNav({ onBoard }: { onBoard: () => void }): JSX.Element {
  return <button className="link" onClick={onBoard}>← Board</button>;
}

interface CardProps {
  agent: Agent;
  taskTitle?: string;
  nowMs: number;
  selected: boolean;
  label: (id: string) => string; // another agent's name
  waiting?: Waiting; // what of this agent waits on the user
  onApprovals: () => void;
  onSelect: () => void;
  onStop: () => void;
  onRemove: () => void;
  onDiscard: () => void;
  onImport: () => void;
  onSaveWork: () => void;
  onResume: () => void;
  events: SandboxEvent[];
  messages: Message[];
  usage?: UsageRow; // all of the agent's sessions (ADR 0026)
}

function AgentCard({
  agent: a, taskTitle, nowMs, selected, label, waiting, onApprovals, onSelect, onStop, onRemove, onDiscard, onImport, onSaveWork,
  onResume, events, messages, usage,
}: CardProps): JSX.Element {
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const stuck = a.state === "starting" && nowMs - Date.parse(a.started_at) > stuckAfterMs;
  const sandboxed = a.sandbox === "podman";
  const repoLabel = sandboxed ? "Agent repository" : "Worktree";
  return (
    <li className={`card ${selected ? "selected" : ""}`} onClick={onSelect} data-agent={a.id}>
      <div className="card-head">
        <AgentName agent={a} />
        <span className={`badge state-${a.state}`}>{stateLabel[a.state]}</span>
      </div>
      {waiting && waiting.requests.length > 0 && (
        <button className="approval-link" onClick={(e) => {
          e.stopPropagation();
          onApprovals();
        }}>
          {waiting.requests.length === 1 ? "1 request waits for your approval"
            : `${waiting.requests.length} requests wait for your approval`}
        </button>
      )}
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
        {a.cli === "codex" && (
          <><span className="badge small cli-codex" title="Runs on the Codex CLI with your ChatGPT plan; the login stays on this machine (ADR 0049)">
            codex</span>{" "}</>
        )}
        {a.model ? `model ${a.model}` : "default model"} ·{" "}
        {sandboxed
          ? <span title={`Sandboxed agents get a clean ${a.cli === "codex" ? "Codex" : "Claude Code"} config; your own settings never apply.`}>
            sandbox config</span>
          : `user settings ${a.inherit_user_settings ? "on" : "off"}`}
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
      {messages.length > 0 && <MessageList agentId={a.id} messages={messages} label={label} />}
      <div className="card-actions row" onClick={(e) => e.stopPropagation()}>
        {a.state !== "exited" && <button onClick={onStop}>Stop</button>}
        {a.resumable && !confirmDiscard && (
          <button onClick={onResume} title="Continue this agent's conversation in its workspace (claude --resume, in agent-office)">
            Resume
          </button>
        )}
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

// Approvals lists every agent waiting on the user (ADR 0019, 0044). Access
// requests are decided here: nothing is granted until a button is pressed,
// and the reason is the agent's own text. A permission prompt can only be
// answered in the agent's terminal, a question in the inbox and a plan on its
// goal's card, so those entries lead there.
// ProjectSwitcher chooses the project the UI shows (ADR 0048); each other
// project says how many of its agents run and wait on the user.
function ProjectSwitcher({ project, projects, elsewhere, onSelect, onManage, managing }: {
  project: Project; projects: Project[]; elsewhere: Elsewhere[]; onSelect: (id: string) => void; onManage: () => void;
  managing: boolean;
}): JSX.Element {
  const other = new Map(elsewhere.map((e) => [e.project.id, e]));
  const label = (p: Project): string => {
    if (p.id === project.id) return p.name;
    const e = other.get(p.id);
    if (p.state !== "open") return `${p.name} · could not open`;
    const parts = [e?.running ? `${e.running} running` : "", e?.waiting.length ? `${e.waiting.length} waiting` : ""].filter(Boolean);
    return parts.length > 0 ? `${p.name} · ${parts.join(" · ")}` : p.name;
  };
  return (
    <span className="project-switcher">
      <select aria-label="Project" value={project.id} title={project.repo_path} onChange={(e) => onSelect(e.target.value)}>
        {projects.map((p) => <option key={p.id} value={p.id}>{label(p)}</option>)}
      </select>
      <button className="badge" aria-expanded={managing} onClick={onManage} title="Add, change or remove projects (ADR 0046)">
        Projects
      </button>
    </span>
  );
}

// OtherProjects lists what waits on the user in the projects not shown
// (ADR 0048): access requests are decided here; everything else opens the
// agent in its project.
function OtherProjects({ elsewhere, run, onSwitch }: {
  elsewhere: Elsewhere[]; run: <T>(fn: () => Promise<T>) => Promise<T | undefined>; onSwitch: (id: string, agentId?: string) => void;
}): JSX.Element | null {
  const busy = elsewhere.filter((e) => e.waiting.length > 0);
  if (busy.length === 0) return null;
  return (
    <section className="approvals other-projects">
      {busy.map(({ project: p, waiting }) => {
        const papi = projectApi(p.id);
        return (
          <div key={p.id} className="other-project" data-project={p.id}>
            <h3>
              In {p.name} <span className="muted">· {waiting.length}</span>{" "}
              <button className="link" onClick={() => onSwitch(p.id)}>Switch to {p.name}</button>
            </h3>
            <ul>
              {waiting.map(({ agent: a, requests, permission, questions, plans }) => (
                <li key={a.id} className="waiting-entry" data-agent={a.id}>
                  <div>
                    <button className="link agent-link" onClick={() => onSwitch(p.id, a.id)}>{a.name || a.id.slice(0, 6)}</button>
                    {" "}({a.role}){" "}
                    <span className="small muted">
                      {[permission ? "a permission prompt" : "", questions.length ? `${questions.length} question(s)` : "",
                        plans.length ? `${plans.length} plan(s) to approve` : ""].filter(Boolean).join(", ")}
                    </span>
                  </div>
                  {requests.map((r) => (
                    <div key={r.id} className="waiting-item request">
                      <div>Asks for {r.kind === "domain" ? <>network access to <span className="mono strong">{r.value}</span></> : `${r.kind} ${r.value}`}</div>
                      <div className="small muted">Agent&apos;s reason: <span className="quote">{r.reason}</span></div>
                      <div className="row">
                        <button onClick={() => void run(() => papi.approveAccess(r.id))}>Allow for this agent</button>
                        <button className="danger" onClick={() => void run(() => papi.denyAccess(r.id))}>Deny</button>
                      </div>
                    </div>
                  ))}
                </li>
              ))}
            </ul>
          </div>
        );
      })}
    </section>
  );
}

function Approvals({ waiting, elsewhere = 0, taskTitle, onOpenAgent, onOpenInbox, onReviewPlan, onApprove, onDeny }: {
  waiting: Waiting[];
  elsewhere?: number; // agents waiting in other projects

  taskTitle: (id?: string) => string | undefined;
  onOpenAgent: (id: string) => void;
  onOpenInbox: () => void;
  onReviewPlan: (goalId: string) => void;
  onApprove: (r: AccessRequest) => void;
  onDeny: (r: AccessRequest) => void;
}): JSX.Element {
  if (waiting.length === 0 && elsewhere > 0) return <p className="muted approvals-empty">Nothing waits in this project.</p>;
  if (waiting.length === 0) {
    return (
      <p className="muted approvals-empty">
        Nothing is waiting. Agents ask here before using a host outside their allowlist, agents on the host show their
        permission prompts here, and questions and plans waiting for you are listed here too.
      </p>
    );
  }
  return (
    <section className="approvals">
      <ul>
        {waiting.map(({ agent: a, requests, permission, questions, plans }) => (
          <li key={a.id} className="waiting-entry" data-agent={a.id}>
            <div>
              <button className="link agent-link" onClick={() => onOpenAgent(a.id)}><AgentName agent={a} /></button>
              {" "}({a.role}{taskTitle(a.task_id) ? `, “${taskTitle(a.task_id)}”` : ""})
            </div>
            {permission && (
              <div className="waiting-item permission">
                <div>Waits for you to answer a permission prompt in its terminal.</div>
                <div className="row">
                  <button onClick={() => onOpenAgent(a.id)} title="Open its terminal; the prompt is answered there">
                    Go to terminal
                  </button>
                  <span className="small muted">Runs on the host with permission prompts on</span>
                </div>
              </div>
            )}
            {questions.map((q) => (
              <div key={`q${q.id}`} className="waiting-item question">
                <div>{q.kind === "blocked" ? "Is blocked and asks you:" : "Asks you:"} <span className="quote">{q.body}</span></div>
                <div className="row">
                  <button onClick={onOpenInbox} title="Answers go from the inbox to the agent (ADR 0030)">Answer in the inbox</button>
                  <span className="small muted">Asked {timeOf(q.created_at)}</span>
                </div>
              </div>
            ))}
            {plans.map((plan) => (
              <div key={`p${plan.id}`} className="waiting-item plan">
                <div>Its plan for “{taskTitle(plan.goal_id) ?? plan.goal_id}” waits for your approval (version {plan.version}).</div>
                <div className="row">
                  <button onClick={() => onReviewPlan(plan.goal_id)} title="The plan is open on the goal's card">Review plan</button>
                  <span className="small muted">Proposed {timeOf(plan.created_at)}</span>
                </div>
              </div>
            ))}
            {requests.map((r) => (
              <div key={r.id} className="waiting-item request">
                <div>
                  Asks for{" "}
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
              </div>
            ))}
          </li>
        ))}
      </ul>
    </section>
  );
}

const deliveryLabel: Record<string, string> = { stop_hook: "at end of turn", pty: "typed while idle" };

// MessageList shows messages an agent sent or received, newest first. Bodies
// are agent-written text.
function MessageList({ agentId, messages, label }: {
  agentId: string;
  messages: Message[];
  label: (id: string) => string;
}): JSX.Element {
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
                ? `from ${m.from_user ? "you" : m.from ? label(m.from) : "agent-office"}`
                : `to ${label(m.to)}`}
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
