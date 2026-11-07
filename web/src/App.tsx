import { type JSX, useEffect, useState } from "react";
import { type Agent, type AgentState, ApiError, type Project, type Task, type TaskStatus, api } from "./api";
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

// How long an agent may stay in "starting" before the UI flags it.
const stuckAfterMs = 20_000;

type View = { kind: "board" } | { kind: "agent"; id: string };

export function App(): JSX.Element {
  const { agents, tasks, connection } = useWorld();
  const [view, setView] = useState<View>({ kind: "board" });
  const [project, setProject] = useState<Project>();
  const [version, setVersion] = useState("");
  const [error, setError] = useState<string>();
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    // Fire-and-forget: header details are cosmetic; failures leave them empty.
    void api.health().then((h) => setVersion(h.version), () => setVersion(""));
    void api.project().then(setProject, () => setProject(undefined));
    const t = window.setInterval(() => setNowMs(Date.now()), 5000);
    return () => window.clearInterval(t);
  }, []);

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
        <span className={`conn conn-${connection}`}>{connection === "open" ? "connected" : connection}</span>
      </header>
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
}

function AgentCard({ agent: a, taskTitle, nowMs, selected, onSelect, onStop, onRemove, onDiscard }: CardProps): JSX.Element {
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const stuck = a.state === "starting" && nowMs - Date.parse(a.started_at) > stuckAfterMs;
  return (
    <li className={`card ${selected ? "selected" : ""}`} onClick={onSelect}>
      <div className="card-head">
        <span className="mono">{a.id}</span>
        <span className={`badge state-${a.state}`}>{stateLabel[a.state]}</span>
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
      {a.worktree_status === "kept" && (
        <div className="small hint">Worktree has uncommitted changes and was kept.</div>
      )}
      <div className="card-actions row" onClick={(e) => e.stopPropagation()}>
        {a.state !== "exited" && <button onClick={onStop}>Stop</button>}
        {a.state === "exited" && a.worktree_status !== "kept" && <button onClick={onRemove}>Remove</button>}
        {a.state === "exited" && a.worktree_status === "kept" && !confirmDiscard && (
          <>
            <button onClick={onRemove}>Retry remove</button>
            <button className="danger" onClick={() => setConfirmDiscard(true)}>Discard changes…</button>
          </>
        )}
        {confirmDiscard && (
          <>
            <span className="small">Delete uncommitted work in this worktree?</span>
            <button className="danger" onClick={() => { setConfirmDiscard(false); onDiscard(); }}>Yes, discard</button>
            <button onClick={() => setConfirmDiscard(false)}>Cancel</button>
          </>
        )}
      </div>
    </li>
  );
}
