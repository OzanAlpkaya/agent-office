import { type JSX, useEffect, useState } from "react";
import {
  type Agent, type AgentState, ApiError, type ImportInfo, type Project, type SandboxEvent, type Status, type Task,
  type TaskStatus, api,
} from "./api";
import { Board } from "./Board";
import { TerminalView } from "./TerminalView";
import { useWorld } from "./useAgents";

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
};

const timeOf = (iso: string): string => new Date(iso).toLocaleTimeString();

// How long an agent may stay in "starting" before the UI flags it.
const stuckAfterMs = 20_000;

type View = { kind: "board" } | { kind: "agent"; id: string };

export function App(): JSX.Element {
  const { agents, tasks, events, connection } = useWorld();
  const [view, setView] = useState<View>({ kind: "board" });
  const [project, setProject] = useState<Project>();
  const [version, setVersion] = useState("");
  const [status, setStatus] = useState<Status>();
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

  const startAgent = async (taskId?: string): Promise<void> => {
    const a = await run(() => api.startAgent(taskId));
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
  const current = view.kind === "agent" ? agents.find((a) => a.id === view.id) : undefined;
  const taskTitle = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;

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
        {status?.sandbox === "host" && <span className="badge unsandboxed">unsandboxed · host mode</span>}
        <span className={`conn conn-${connection}`}>{connection === "open" ? "connected" : connection}</span>
      </header>
      {status?.sandbox === "host" && (
        <div className="banner warn">
          Host mode: agents run directly on this machine, with your files, SSH keys and network, and ask before
          using tools. Restart the daemon with <span className="mono">--sandbox podman</span> to isolate them.
        </div>
      )}
      {status?.warnings.map((w) => <div key={w} className="banner warn">{w}</div>)}
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
            {agents.filter((a) => a.has_terminal).map((a) => (
              <button key={a.id} className={current?.id === a.id ? "active" : ""}
                onClick={() => setView({ kind: "agent", id: a.id })}>
                <span className={`dot state-${a.state}`} /> {taskTitle(a.task_id) ?? a.id.slice(0, 6)}
              </button>
            ))}
          </nav>
          {view.kind === "board" || !current ? (
            <Board tasks={tasks} agents={agents} canStart={true}
              onCreate={async (title, description) => (await run(() => api.createTask(title, description))) !== undefined}
              onStatus={(t: Task, status: TaskStatus) => void run(() => api.updateTask(t.id, { status }))}
              onStart={(t) => void startAgent(t.id)}
              onDelete={(t) => void run(() => api.deleteTask(t.id))}
              onOpenAgent={(id) => setView({ kind: "agent", id })}
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
}

function AgentCard({
  agent: a, taskTitle, nowMs, selected, onSelect, onStop, onRemove, onDiscard, onImport, onSaveWork, events,
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
        {sandboxed
          ? <span className="chip sandboxed" title="Rootless Podman worker; permission prompts off inside it">sandboxed</span>
          : <span className="chip unsandboxed" title="Runs on the host with permission prompts on">unsandboxed</span>}
      </div>
      {taskTitle && <div className="small">{taskTitle}</div>}
      {a.activity && <div className="small">Tool: <span className="mono">{a.activity}</span></div>}
      {a.state === "starting" && (
        <div className={`small hint ${stuck ? "stuck" : ""}`}>
          {stuck ? "⚠ Still starting after 20 s. " : "Still starting? "}
          Claude Code may be waiting for an answer in the terminal, e.g. whether to trust this folder.
          Answer it there; agent-office never accepts it for you.
        </div>
      )}
      <div className="small muted mono" title={a.worktree_path}>{a.branch}</div>
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
