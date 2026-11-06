import { type JSX, useEffect, useState } from "react";
import { type Agent, type AgentState, api } from "./api";
import { TerminalView } from "./TerminalView";
import { useAgents } from "./useAgents";

const stateLabel: Record<AgentState, string> = {
  starting: "Starting",
  idle: "Idle",
  working: "Working",
  waiting_permission: "Waiting for permission",
  exited: "Exited",
};

export function App(): JSX.Element {
  const { agents, connection } = useAgents();
  const [selected, setSelected] = useState<string>();
  const [version, setVersion] = useState("");
  const [error, setError] = useState<string>();

  useEffect(() => {
    // Fire-and-forget: the version is cosmetic; failures leave it empty.
    void api.health().then((h) => setVersion(h.version), () => setVersion(""));
  }, []);

  // Follow the newest agent unless the user picked one that still exists.
  useEffect(() => {
    if (!selected || !agents.some((a) => a.id === selected)) {
      setSelected(agents.at(-1)?.id);
    }
  }, [agents, selected]);

  // run executes an API call from a click handler and shows its error. It
  // never rejects, so callers can discard the promise with `void`.
  const run = async (fn: () => Promise<unknown>): Promise<void> => {
    setError(undefined);
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const running = agents.some((a) => a.state !== "exited");
  const current = agents.find((a) => a.id === selected);

  return (
    <div className="app">
      <header>
        <h1>agent-office</h1>
        <span className="muted">{version && `v${version}`}</span>
        <span className={`conn conn-${connection}`}>{connection === "open" ? "connected" : connection}</span>
      </header>
      <main>
        <aside>
          <button className="primary" disabled={running} onClick={() => void run(api.startAgent)}>
            Start agent
          </button>
          {running && <p className="muted small">Phase 1 runs one agent at a time.</p>}
          {error && <p className="error">{error}</p>}
          <ul className="agents">
            {agents.map((a) => (
              <AgentCard
                key={a.id}
                agent={a}
                selected={a.id === selected}
                onSelect={() => setSelected(a.id)}
                onStop={() => void run(() => api.stopAgent(a.id))}
                onRemove={() => void run(() => api.removeAgent(a.id))}
              />
            ))}
          </ul>
          {agents.length === 0 && <p className="muted">No agents yet.</p>}
        </aside>
        <section className="main-pane">
          {current ? <TerminalView key={current.id} agentId={current.id} /> : <div className="empty">Start an agent to see its terminal.</div>}
        </section>
      </main>
    </div>
  );
}

interface CardProps {
  agent: Agent;
  selected: boolean;
  onSelect: () => void;
  onStop: () => void;
  onRemove: () => void;
}

function AgentCard({ agent: a, selected, onSelect, onStop, onRemove }: CardProps): JSX.Element {
  return (
    <li className={`card ${selected ? "selected" : ""}`} onClick={onSelect}>
      <div className="card-head">
        <span className="mono">{a.id}</span>
        <span className={`badge state-${a.state}`}>{stateLabel[a.state]}</span>
      </div>
      {a.activity && <div className="small">Tool: <span className="mono">{a.activity}</span></div>}
      {a.state === "starting" && (
        <div className="small hint">
          Still starting? Claude Code may be waiting for an answer in the terminal, e.g. whether to
          trust this folder. Answer it there; agent-office never accepts it for you.
        </div>
      )}
      <div className="small muted">
        {a.model ? `model ${a.model}` : "default model"} · user settings {a.inherit_user_settings ? "on" : "off"}
      </div>
      <div className="small muted mono" title={a.repo}>{a.repo}</div>
      {a.state === "exited" && <div className="small muted">exit code {a.exit_code ?? "?"}</div>}
      <div className="card-actions" onClick={(e) => e.stopPropagation()}>
        {a.state !== "exited" ? <button onClick={onStop}>Stop</button> : <button onClick={onRemove}>Remove</button>}
      </div>
    </li>
  );
}
