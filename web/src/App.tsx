import { type JSX, Suspense, lazy, useEffect, useLayoutEffect, useMemo, useState } from "react";
import {
  type AccessRequest, type AccessScope, type Agent, type AgentState, ApiError, type ApproveResult, type AuthSession, type ImportInfo, type Kind, type Message,
  type DaemonStatus, type Project, type Role, type RoleInfo, type SandboxEvent, type Spec, type Status, type Task, type TaskStatus,
  api as daemonApi, projectApi,
} from "./api";
import { ProjectApiContext } from "./ProjectContext";
import { type Elsewhere, waitingTotal } from "./projects";
import { AgentName, AgentsContext } from "./AgentName";
import { AgentSwitcher } from "./AgentSwitcher";
import { type Tabs, closeTab, keepTabs, labelOf, moveTab, neighbourTab, openTab, parseTabs } from "./agents";
import { Board } from "./Board";
import { backgroundLine, splitEvents } from "./sandboxEvents";
import { Drawer } from "./Drawer";
import { Icon, type IconName } from "./icons";
import { type Presence } from "./presence";
import { type SettingsTab, Settings } from "./Settings";
import { narrowQuery, useMedia } from "./useMedia";
import { InboxPanel } from "./Team";
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
  resume_failed: "resume failed: the agent CLI could not load the previous conversation",
  stopped_by_lead: "stopped by the Tech Lead",
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
  oom_kill: "Out of memory",
  pids_limit: "Process limit reached",
  egress_background: "Claude Code background request (expected)",
  api_background: "Claude Code background API call (expected)",
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

// MainTab is a view every project has; a switch to another project keeps it.
export type MainTab = "board" | "office" | "usage" | "settings";

// "agents" is the phone layout's view of the agent list (the rail on wider screens, ADR 0040).
type View = { kind: MainTab } | { kind: "agents" } | { kind: "agent"; id: string };

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
  presence?: Presence; // the user's devices with the UI open (Phase 18)
}

export function App({
  session, onSignedOut, project, projects, daemon, elsewhere, initialAgent, initialTab, onSelectProject, presence,
}: AppProps): JSX.Element {
  // The project's routes, plus the daemon's (health, devices, projects) under one name.
  const papi = useMemo(() => projectApi(project.id), [project.id]);
  const api = useMemo(() => ({ ...daemonApi, ...papi }), [papi]);
  const { agents, tasks, events, messages, accessRequests, pullRequests, usage, inbox, specs, qaRuns, memory, merges, connection } =
    useWorld(papi);
  const [roles, setRoles] = useState<RoleInfo[]>([]);
  const narrow = useMedia(narrowQuery);
  const [view, setView] = useState<View>({ kind: initialTab ?? "board" });
  const [settingsTab, setSettingsTab] = useState<SettingsTab>("project");
  // A switch to another project keeps the main view the user is on.
  const tab: MainTab = view.kind === "office" || view.kind === "usage" || view.kind === "settings" ? view.kind : "board";
  const [tabs, setTabs] = useState<Tabs>(() => loadTabs(project.id));
  const [switcher, setSwitcher] = useState(false);
  const [approvalsOpen, setApprovalsOpen] = useState(false);
  const [inboxOpen, setInboxOpen] = useState(false);
  const [detail, setDetail] = useState<string>();
  const [dragged, setDragged] = useState<string>();
  const [version, setVersion] = useState("");
  const [status, setStatus] = useState<Status>();
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

  // Fall back to the board if the open agent disappears, or the agents view on a wide screen.
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
    setDetail(undefined);
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
        // agent's header then offers "Discard changes". Not an error worth a banner.
        if (e instanceof ApiError && e.status === 409) return undefined;
        throw e;
      }),
    );

  const running = agents.filter((a) => a.state !== "exited").length;
  const leadRunning = agents.some((a) => a.kind === "lead" && a.state !== "exited");
  const architectRunning = agents.some((a) => a.kind === "architect" && a.state !== "exited");
  const unreadCount = unread(inbox);
  const ok = async (fn: () => Promise<unknown>): Promise<boolean> => (await run(fn)) !== undefined;

  // approveSpec approves a plan (ADR 0031); a Tech Lead that could not start is shown as an error.
  const approveSpec = async (s: Spec): Promise<ApproveResult> => {
    const res = await api.approveSpec(s.id);
    if (res.lead_error) setError(`Plan approved; the Tech Lead did not start: ${res.lead_error}`);
    return res;
  };

  // reviewPlan opens the goal's card, whose plan is open while it waits for approval.
  const reviewPlan = (goalId: string): void => {
    setView({ kind: "board" });
    setDetail(goalId);
  };
  const go = (kind: MainTab | "agents"): void => {
    setView({ kind });
  };
  const openSettings = (t: SettingsTab): void => {
    setSettingsTab(t);
    setView({ kind: "settings" });
  };
  const current = view.kind === "agent" ? agents.find((a) => a.id === view.id) : undefined;
  const waitingAgents = useMemo(() => new Set(waiting.map((w) => w.agent.id)), [waiting]);
  const label = (id: string): string => labelOf(agents, id);
  const byId = new Map(agents.map((a) => [a.id, a]));
  const tabAgents = tabs.order.map((id) => byId.get(id)).filter((a): a is Agent => a !== undefined);
  const activeAgents = agents.filter((a) => a.state !== "exited");
  const exitedAgents = agents.filter((a) => a.state === "exited");
  const moveTabBy = (id: string, delta: number): void => setTabs((t) => moveTab(t, id, t.order.indexOf(id) + delta));
  const taskTitle = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const sessions = sessionList(usage);
  const usageByAgent = usageBy(sessions, (s) => s.agent_id);
  const usageByTask = usageBy(sessions, (s) => s.task_id);
  const usageByGoal = usageBy(sessions, (s) => s.goal_id);
  const runningEverywhere = elsewhere.reduce((n, e) => n + e.running, running);

  const navItems: { kind: MainTab; label: string; icon: IconName; count?: number }[] = [
    { kind: "board", label: "Board", icon: "board" },
    { kind: "office", label: "Office", icon: "office" },
    { kind: "usage", label: "Usage", icon: "usage" },
    { kind: "settings", label: "Settings", icon: "settings", count: memory.filter((m) => m.status === "proposed").length || undefined },
  ];

  const agentList = (
    <div className="agent-list">
      <div className="agent-list-head">
        <span className="eyebrow">Agents</span>
        <span className="muted small">{running} running</span>
        <span className="spacer" />
        <button className="ghost sm" onClick={() => void startAgent()} aria-label="Start agent (no task)"
          title="Start an agent without a task, e.g. to explore the repository">
          <Icon name="plus" size={16} />Start
        </button>
      </div>
      {activeAgents.length > 0 && (
        <ul className="agents">
          {activeAgents.map((a) => (
            <AgentRow key={a.id} agent={a} taskTitle={taskTitle(a.task_id)} selected={current?.id === a.id}
              waiting={waitingAgents.has(a.id)} usage={usageByAgent.get(a.id)} onSelect={() => openAgent(a.id)} />
          ))}
        </ul>
      )}
      {exitedAgents.length > 0 && (
        // Exited agents stay reachable here, in the switcher and in Usage (ADR 0044).
        <details className="exited-agents" open={exitedAgents.some((a) => a.id === current?.id) || undefined}>
          <summary className="muted small">Exited · {exitedAgents.length}</summary>
          <ul className="agents">
            {exitedAgents.map((a) => (
              <AgentRow key={a.id} agent={a} taskTitle={taskTitle(a.task_id)} selected={current?.id === a.id}
                waiting={false} usage={usageByAgent.get(a.id)} onSelect={() => openAgent(a.id)} />
            ))}
          </ul>
        </details>
      )}
      {agents.length === 0 && <p className="muted small agent-list-empty">No agents yet. Start a Tech Lead from a goal on the board.</p>}
    </div>
  );

  return (
    <ProjectApiContext.Provider value={papi}>
    <AgentsContext.Provider value={agents}>
      <div className={`app ${narrow ? "narrow" : "wide"}`}>
        <header className="topbar">
          {!narrow && (
            <div className="brand" aria-label="agent-office">
              <span className="brand-mark" aria-hidden="true" />
              <h1>agent-office</h1>
            </div>
          )}
          <ProjectSwitcher project={project} projects={projects} elsewhere={elsewhere} connection={connection}
            onSelect={(id) => onSelectProject(id, undefined, tab)} />
          {status?.sandbox === "podman" && !narrow && (
            <button className="pill sandboxed" onClick={() => openSettings("sandbox")}
              title={`Agents run in rootless Podman${status.image ? `; worker image ${status.image}` : ""}. Opens the sandbox settings.`}>
              <span className="dot" />sandboxed{status.pack ? ` · ${status.pack.name}` : ""}
            </button>
          )}
          {status?.sandbox === "host" && (
            <span className="pill unsandboxed" title="Agents run on this machine with permission prompts">
              <span className="dot" />{narrow ? "host" : "host mode"}
            </span>
          )}
          <span className="spacer" />
          <button className="topbar-button switcher-button" onClick={() => setSwitcher(true)} aria-haspopup="dialog"
            aria-label={`Find an agent (${running} running)`} title="Find an agent by name, id, role, task or branch ( / )">
            <Icon name="search" />
            {!narrow && <span className="switcher-label">Find agent</span>}
            {!narrow && <kbd>/</kbd>}
          </button>
          <button className={`topbar-button approvals-button ${waitingCount ? "has-pending" : ""}`} aria-expanded={approvalsOpen}
            aria-label={waitingCount ? `Approvals: ${waitingCount} waiting` : "Approvals: none waiting"}
            onClick={() => setApprovalsOpen((v) => !v)}
            title="Agents waiting on you: access requests, permission prompts, questions and plans to approve (ADR 0019, 0044)">
            <Icon name="approvals" />
            {!narrow && <span>Approvals</span>}
            {waitingCount > 0 && <span className="count">{waitingCount}</span>}
          </button>
          <button className={`topbar-button inbox-button ${unreadCount ? "has-unread" : ""}`} aria-expanded={inboxOpen}
            aria-label={unreadCount ? `Inbox: ${unreadCount} unread` : "Inbox"}
            onClick={() => setInboxOpen((v) => !v)} title="Messages to you from the team (ADR 0030)">
            <Icon name="inbox" />
            {!narrow && <span>Inbox</span>}
            {unreadCount > 0 && <span className="count">{unreadCount}</span>}
          </button>
        </header>
        {status?.sandbox === "host" && (
          <div className="banner warn">
            Host mode: agents run directly on this machine, with your files, SSH keys and network, and ask before
            using tools. Restart the daemon with <span className="mono">--sandbox podman</span> to isolate them.
          </div>
        )}
        {status?.warnings.map((w) => <div key={w} className="banner warn">{w}</div>)}
        {error && (
          <div className="banner danger" role="alert">
            <span className="error-text">{error}</span>
            <span className="spacer" />
            <button className="ghost sm" onClick={() => setError(undefined)} aria-label="Dismiss the error">Dismiss</button>
          </div>
        )}
        <div className="shell">
          {!narrow && (
            <nav className="rail" aria-label="Main">
              <ul className="nav-list">
                {navItems.map((n) => (
                  <li key={n.kind}>
                    <button className={`nav-item ${view.kind === n.kind ? "active" : ""}`} aria-current={view.kind === n.kind ? "page" : undefined}
                      aria-label={n.count ? `${n.label} (${n.count} memory note${n.count === 1 ? "" : "s"} proposed)` : n.label}
                      onClick={() => go(n.kind)}>
                      <Icon name={n.icon} /><span>{n.label}</span>
                      {n.count !== undefined && <span className="count tone-warn">{n.count}</span>}
                    </button>
                  </li>
                ))}
              </ul>
              {agentList}
            </nav>
          )}
          <main className="main-pane">
            {view.kind === "agents" ? (
              <div className="agents-pane">{agentList}</div>
            ) : view.kind === "office" ? (
              <Suspense fallback={<div className="empty">Loading the office…</div>}>
                <Office agents={agents} tasks={tasks} messages={messages} waiting={waiting}
                  onOpenAgent={openAgent} layout={project.office} presence={presence}
                  floors={[{ id: project.id, name: project.name, waiting: waiting.length, running, current: true },
                    ...elsewhere.filter((e) => e.project.state === "open").map((e) => ({ id: e.project.id, name: e.project.name,
                      waiting: e.waiting.length, running: e.running, current: false }))]
                    .sort((a, b) => projects.findIndex((p) => p.id === a.id) - projects.findIndex((p) => p.id === b.id))}
                  onFloor={(id) => onSelectProject(id, undefined, "office")}
                  onLayoutSettings={() => openSettings("project")} />
              </Suspense>
            ) : view.kind === "usage" ? (
              <Suspense fallback={<div className="empty">Loading usage…</div>}>
                <Usage usage={usage} agents={agents} tasks={tasks} status={status}
                  onOpenAgent={openAgent} />
              </Suspense>
            ) : view.kind === "settings" ? (
              <Settings tab={settingsTab} onTab={setSettingsTab} project={project} projects={projects} status={status} daemon={daemon}
                version={version} deviceName={session.device?.name} running={runningEverywhere} api={papi} accessRequests={accessRequests} memory={memory} run={run}
                onSignedOut={onSignedOut} onSelectProject={(id) => onSelectProject(id, undefined, "settings")} />
            ) : view.kind === "board" || !current ? (
              <Board tasks={tasks} agents={agents} canStart={true} leadRunning={leadRunning} usageByTask={usageByTask}
                usageByGoal={usageByGoal}
                onStartLead={(t) => void startAgent(t.id, "lead")}
                onCreate={async (title, description) => (await run(() => api.createTask(title, description))) !== undefined}
                onStatus={(t: Task, s: TaskStatus) => void run(() => api.updateTask(t.id, { status: s }))}
                onStart={(t) => void startAgent(t.id)}
                onDelete={(t) => void run(() => api.deleteTask(t.id))}
                onOpenAgent={openAgent}
                pullRequests={pullRequests} github={status?.github !== undefined}
                onOpenPR={(t) => void run(() => api.openPullRequest(t.id))}
                onRequestReview={(pr) => void run(() => api.requestReview(pr.id))}
                onSendToWorker={(pr, r) => void run(() => api.sendToWorker(pr.id, r.id))}
                onAcceptFlagged={(pr) => void run(() => api.acceptFlagged(pr.id, pr.flagged.map((f) => f.commit)))}
                roles={roles} specs={specs} qaRuns={qaRuns} inbox={inbox} architectRunning={architectRunning}
                merges={merges} projectId={project.id} key={project.id}
                detail={detail} onDetail={setDetail} waitingAgents={waitingAgents}
                onStartRole={(t, role) => void startAgent(t.id, role)}
                onSaveSpec={(s, plan) => ok(() => api.editSpec(s.id, plan))}
                onApproveSpec={(s) => void run(() => approveSpec(s))}
                onResolve={(t) => void run(async () => { await api.resolveConflict(t.id); return true; })}
                onReadInbox={(i) => void run(() => api.readInbox(i.id))}
                onReplyInbox={(i, body) => ok(() => api.replyInbox(i.id, body))}
              />
            ) : (
              <div className="agent-view">
                {!narrow && tabAgents.length > 0 && (
                  <div className={`agent-tabs ${tabAgents.length > crowdedTabs ? "crowded" : ""}`} role="tablist" aria-label="Open agents">
                    {tabAgents.map((a) => (
                      <span key={a.id} role="presentation"
                        className={`agent-tab ${current.id === a.id ? "active" : ""} ${dragged === a.id ? "dragging" : ""}`}
                        draggable
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
                        <button role="tab" aria-selected={current.id === a.id} data-agent={a.id}
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
                  </div>
                )}
                <AgentHeader agent={current} taskTitle={taskTitle(current.task_id)} nowMs={nowMs} label={label} narrow={narrow}
                  waiting={waiting.find((w) => w.agent.id === current.id)}
                  onApprovals={() => setApprovalsOpen(true)}
                  onBack={() => setView({ kind: narrow ? "agents" : "board" })}
                  onClose={() => closeAgentTab(current.id)}
                  onStop={() => void run(() => api.stopAgent(current.id))}
                  onRemove={() => void removeAgent(current.id)}
                  onDiscard={() => void run(() => api.discardAgent(current.id))}
                  onImport={() => void run(() => api.importAgent(current.id))}
                  onSaveWork={() => void run(() => api.saveWork(current.id))}
                  onResume={() => void run(() => api.resumeAgent(current.id))}
                  events={events[current.id] ?? []}
                  messages={messages[current.id] ?? []}
                  usage={usageByAgent.get(current.id)} />
                {/* Live while it runs, else its saved sessions (ADR 0036). */}
                <TerminalView key={`${current.id}-${current.session_id ?? ""}`} agent={current}
                  onResume={() => void run(() => api.resumeAgent(current.id))} />
              </div>
            )}
          </main>
        </div>
        {narrow && (
          <nav className="tabbar" aria-label="Main">
            {[navItems[0], navItems[1]].map((n) => (
              <button key={n.kind} className={view.kind === n.kind ? "active" : ""} aria-current={view.kind === n.kind ? "page" : undefined}
                onClick={() => go(n.kind)}>
                <Icon name={n.icon} /><span>{n.label}</span>
              </button>
            ))}
            <button className={view.kind === "agents" || view.kind === "agent" ? "active" : ""} aria-label={`Agents · ${running}`}
              title="Agents and their actions" onClick={() => go("agents")}>
              <Icon name="agents" /><span>Agents</span>{running > 0 && <span className="count">{running}</span>}
            </button>
            {[navItems[2], navItems[3]].map((n) => (
              <button key={n.kind} className={view.kind === n.kind ? "active" : ""} aria-current={view.kind === n.kind ? "page" : undefined}
                aria-label={n.count ? `${n.label} (${n.count} memory note${n.count === 1 ? "" : "s"} proposed)` : n.label}
                onClick={() => go(n.kind)}>
                <Icon name={n.icon} /><span>{n.label}</span>
                {n.count !== undefined && <span className="count tone-warn">{n.count}</span>}
              </button>
            ))}
          </nav>
        )}
        {inboxOpen && (
          <Drawer label="Inbox" onClose={() => setInboxOpen(false)} className="inbox-drawer"
            title={<>Inbox{unreadCount > 0 && <span className="count">{unreadCount}</span>}</>}
            actions={
              <button className="sm" title="Ask the assistant for a briefing (starts it if needed); it arrives here"
                onClick={() => void run(async () => { await api.askBriefing(); return true; })}>
                Briefing
              </button>
            }>
            <InboxPanel items={inbox} tasks={tasks} specs={specs} onOpenAgent={(id) => { setInboxOpen(false); openAgent(id); }}
              onRead={(i) => void run(() => api.readInbox(i.id))}
              onReply={(i, body) => ok(() => api.replyInbox(i.id, body))}
              onReviewPlan={(goalId) => { setInboxOpen(false); reviewPlan(goalId); }}
              onApprovePlan={(s, i) => void run(async () => {
                const res = await approveSpec(s);
                if (!i.read_at) await api.readInbox(i.id);
                return res;
              })} />
          </Drawer>
        )}
        {approvalsOpen && (
          <Drawer label="Approvals" onClose={() => setApprovalsOpen(false)} className="approvals-drawer"
            title={<>Waiting for you{waitingCount > 0 && <span className="count">{waitingCount}</span>}</>}>
            <Approvals waiting={waiting} elsewhere={waitingCount - waiting.length} taskTitle={taskTitle} onOpenAgent={(id) => {
              setApprovalsOpen(false);
              openAgent(id);
            }}
              onOpenInbox={() => {
                setApprovalsOpen(false);
                setInboxOpen(true);
              }}
              onReviewPlan={(goalId) => {
                setApprovalsOpen(false);
                reviewPlan(goalId);
              }}
              onApprove={(r, scope) => void run(() => api.approveAccess(r.id, scope))}
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
      </div>
    </AgentsContext.Provider>
    </ProjectApiContext.Provider>
  );
}

// AgentRow is an agent in the rail (or the phone's Agents view): state, name,
// role, task and cost. Its details and actions are above its terminal.
function AgentRow({ agent: a, taskTitle, selected, waiting, usage, onSelect }: {
  agent: Agent; taskTitle?: string; selected: boolean; waiting: boolean; usage?: UsageRow; onSelect: () => void;
}): JSX.Element {
  return (
    <li className={`card agent-row ${selected ? "selected" : ""}`} data-agent={a.id}>
      <button className="agent-row-button" onClick={onSelect} aria-current={selected ? "true" : undefined}
        title={`${a.name} · ${a.role} · ${stateLabel[a.state]}${taskTitle ? `\n${taskTitle}` : ""}`}>
        <span className={`dot state-${a.state}`} aria-hidden="true" />
        <span className="agent-row-main">
          <span className="agent-row-line">
            <span className="agent-first">{a.name || a.id.slice(0, 6)}</span>
            <span className={`agent-row-role role-${a.kind}`}>{a.role}</span>
          </span>
          <span className="agent-row-task">{taskTitle ?? (a.state === "exited" ? exitLabel[a.exit_reason ?? ""] ?? "exited" : "no task")}</span>
        </span>
        <span className="agent-row-side">
          {waiting && <span className="chip small tone-warn">needs you</span>}
          {usage && usage.costMicroUSD > 0 && <span className="agent-row-cost">{formatMoney(usage.costMicroUSD)}</span>}
        </span>
      </button>
    </li>
  );
}

interface HeaderProps {
  agent: Agent;
  taskTitle?: string;
  nowMs: number;
  label: (id: string) => string; // another agent's name
  narrow: boolean;
  waiting?: Waiting; // what of this agent waits on the user
  onApprovals: () => void;
  onBack: () => void;
  onClose: () => void;
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

// AgentHeader sits above an agent's terminal: who it is, what it does, its
// actions, and its details (branch, model, imports, events, messages).
function AgentHeader({
  agent: a, taskTitle, nowMs, label, narrow, waiting, onApprovals, onBack, onClose, onStop, onRemove, onDiscard, onImport, onSaveWork,
  onResume, events, messages, usage,
}: HeaderProps): JSX.Element {
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const stuck = a.state === "starting" && nowMs - Date.parse(a.started_at) > stuckAfterMs;
  const sandboxed = a.sandbox === "podman";
  const repoLabel = sandboxed ? "Agent repository" : "Worktree";
  return (
    <section className="agent-header card" data-agent={a.id} aria-label={`Agent ${a.name || a.id}`}>
      <div className="agent-header-top">
        {narrow && <button className="ghost icon-button" onClick={onBack} aria-label="Back to the agents"><Icon name="back" /></button>}
        <div className="agent-header-title">
          <div className="card-head row">
            <AgentName agent={a} />
            <span className={`chip role-${a.kind}`} title={kindHelp[a.kind]}>{a.role}</span>
            <span className={`badge status-chip state-${a.state}`}>{stateLabel[a.state]}</span>
            {sandboxed
              ? <span className="chip sandboxed" title="Rootless Podman worker; permission prompts off inside it">sandboxed</span>
              : <span className="chip unsandboxed" title="Runs on the host with permission prompts on">unsandboxed</span>}
            {a.cli === "codex" && (
              <span className="chip cli-codex" title="Runs on the Codex CLI with your ChatGPT plan; the login stays on this machine (ADR 0049)">codex</span>
            )}
          </div>
          <div className="small muted agent-header-sub">
            {taskTitle ?? "No task"}
            {a.activity && <> · tool <span className="mono">{a.activity}</span></>}
            {usage && <> · <UsageLine usage={usage} inline /></>}
          </div>
        </div>
        <div className="card-actions row">
          {a.state !== "exited" && <button onClick={onStop}>Stop</button>}
          {a.resumable && !confirmDiscard && (
            <button className="primary" onClick={onResume} title="Continue this agent's conversation in its workspace (claude --resume, in agent-office)">
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
          {!narrow && <button className="ghost icon-button" onClick={onClose} aria-label="Close this terminal" title="Close this terminal's tab">
            <Icon name="close" /></button>}
        </div>
      </div>
      {waiting && waiting.requests.length > 0 && (
        <button className="approval-link" onClick={onApprovals}>
          {waiting.requests.length === 1 ? "1 request waits for your approval" : `${waiting.requests.length} requests wait for your approval`}
        </button>
      )}
      {a.state === "starting" && (
        <div className={`small hint ${stuck ? "stuck" : ""}`}>
          {stuck ? "⚠ Still starting after 20 s. " : "Still starting? "}
          Claude Code may be waiting for an answer in the terminal, e.g. whether to trust this folder.
          Answer it there; agent-office never accepts it for you.
        </div>
      )}
      {a.state === "exited" && (
        <div className="small muted">
          {exitLabel[a.exit_reason ?? ""] ?? a.exit_reason ?? "exited"}
          {a.exit_code !== undefined && ` · exit code ${a.exit_code}`}
          {a.exit_signal && ` · signal ${a.exit_signal}`}
        </div>
      )}
      {a.state === "exited" && a.exit_detail && (
        <div className={`small exit-detail ${a.oom_killed ? "oom" : ""}`} data-testid="exit-detail">{a.exit_detail}</div>
      )}
      {a.worktree_status === "kept" && (
        <div className="small hint">
          {repoLabel} has uncommitted changes and was kept.
          {sandboxed && " Save work commits them to the branch; discard deletes them."}
        </div>
      )}
      <div className="agent-header-details">
        <details className="events small">
          <summary className="muted">Details · <span className="mono">{a.branch}</span></summary>
          <div className="small muted mono"
            title={sandboxed
              ? `Agent repository: ${a.worktree_path}\nDo not cd into or open it; use the branch in your repository.`
              : a.worktree_path}>
            {a.branch}
          </div>
          <div className="small muted">
            {a.model ? `model ${a.model}` : "default model"} ·{" "}
            {sandboxed
              ? <span title={`Sandboxed agents get a clean ${a.cli === "codex" ? "Codex" : "Claude Code"} config; your own settings never apply.`}>
                sandbox config</span>
              : `user settings ${a.inherit_user_settings ? "on" : "off"}`}
          </div>
          {a.last_import && <ImportLine imp={a.last_import} branch={a.branch} />}
        </details>
        {events.length > 0 && <EventList events={events} />}
        {messages.length > 0 && <MessageList agentId={a.id} messages={messages} label={label} />}
      </div>
    </section>
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
// means the agent tried something the sandbox does not allow; Claude Code's
// own background requests are refused too, but shown apart and quietly
// (Phase 17): they are expected, not something to act on.
function EventList({ events }: { events: SandboxEvent[] }): JSX.Element {
  const { problems, background } = splitEvents(events);
  const item = (e: SandboxEvent): JSX.Element => (
    <li key={`${e.at}|${e.kind}|${e.detail}`}>
      <span className="muted">{timeOf(e.at)}</span> {eventLabel[e.kind] ?? e.kind}
      {e.detail && <>: <span className="mono">{e.detail}</span></>}
    </li>
  );
  return (
    <>
      {problems.length > 0 && (
        <details className="events small">
          <summary className="hint">
            {problems.length === 1 ? "1 sandbox event" : `${problems.length} sandbox events`}
            {` · latest: ${eventLabel[problems[0].kind] ?? problems[0].kind}`}
          </summary>
          <ul>{problems.map(item)}</ul>
        </details>
      )}
      {background.length > 0 && (
        <details className="events small background-events">
          <summary className="muted" data-testid="background-events">{backgroundLine(background.length)}</summary>
          <ul>{background.map(item)}</ul>
        </details>
      )}
    </>
  );
}

// ProjectSwitcher chooses the project the UI shows (ADR 0048); each other
// project says how many of its agents run and wait on the user. The dot is
// the connection to the daemon.
function ProjectSwitcher({ project, projects, elsewhere, connection, onSelect }: {
  project: Project; projects: Project[]; elsewhere: Elsewhere[]; connection: string; onSelect: (id: string) => void;
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
      <span className={`conn conn-${connection}`} role="img" aria-label={connection === "open" ? "connected" : connection}
        title={connection === "open" ? "Connected to the daemon" : `Daemon connection: ${connection}`}>
        <span className="dot" />
      </span>
      <select aria-label="Project" value={project.id} title={project.repo_path} onChange={(e) => onSelect(e.target.value)}>
        {projects.map((p) => <option key={p.id} value={p.id}>{label(p)}</option>)}
      </select>
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
                        <button onClick={() => void run(() => papi.approveAccess(r.id, "project"))}>Allow for the project</button>
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

// Approvals lists every agent waiting on the user (ADR 0019, 0044). Access
// requests are decided here: nothing is granted until a button is pressed,
// and the reason is the agent's own text. A permission prompt can only be
// answered in the agent's terminal, a question in the inbox and a plan on its
// goal's card, so those entries lead there.
function Approvals({ waiting, elsewhere = 0, taskTitle, onOpenAgent, onOpenInbox, onReviewPlan, onApprove, onDeny }: {
  waiting: Waiting[];
  elsewhere?: number; // agents waiting in other projects

  taskTitle: (id?: string) => string | undefined;
  onOpenAgent: (id: string) => void;
  onOpenInbox: () => void;
  onReviewPlan: (goalId: string) => void;
  onApprove: (r: AccessRequest, scope?: AccessScope) => void; // no scope: the asking agent (ADR 0053)
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
              {" "}<span className="muted">({a.role}{taskTitle(a.task_id) ? `, “${taskTitle(a.task_id)}”` : ""})</span>
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
                  <button onClick={() => onReviewPlan(plan.goal_id)} title="The plan is in the goal's card">Review plan</button>
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
                {r.decider === "lead" && (
                  <div className="small hint">Its Tech Lead decides this one (the project lets it); you can decide first.</div>
                )}
                <div className="row">
                  <button onClick={() => onApprove(r, undefined)} title="Adds the host to this agent's allowlist, in later sessions too">
                    Allow for this agent
                  </button>
                  <button onClick={() => onApprove(r, "project")} title="Adds the host for every agent of this project">
                    Allow for the project
                  </button>
                  <button className="danger" onClick={() => onDeny(r)}>Deny</button>
                  <span className="small muted">Requested {timeOf(r.created_at)}</span>
                </div>
              </div>
            ))}
          </li>
        ))}
      </ul>
    </section>
  );
}

const deliveryLabel: Record<string, string> = { stop_hook: "at end of turn", pty: "typed while idle", prompt: "with its resume" };

// MessageList shows messages an agent sent or received, newest first. Bodies
// are agent-written text.
function MessageList({ agentId, messages, label }: {
  agentId: string;
  messages: Message[];
  label: (id: string) => string;
}): JSX.Element {
  const waiting = messages.filter((m) => m.to === agentId && !m.delivered_at).length;
  return (
    <details className="events small">
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

// UsageLine is an agent's or task's tokens and estimated cost (ADR 0026).
export function UsageLine({ usage: u, inline = false }: { usage: UsageRow; inline?: boolean }): JSX.Element {
  const Tag = inline ? "span" : "div";
  return (
    <Tag className="small muted usage-line" title="estimated from token counts; see the Usage tab">
      {formatTokens(totalTokens(u.tokens))} tokens · {formatMoney(u.costMicroUSD)}
      {u.unpricedTokens > 0 && <span className="hint"> + unpriced</span>}
    </Tag>
  );
}
