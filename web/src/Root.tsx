// Root chooses the project the UI shows (ADR 0048) and watches the others
// for the Approvals badge. Switching remounts the App, so nothing of one
// project's view leaks into another's.
import { type JSX, useEffect, useMemo, useState } from "react";
import { type AuthSession, type DaemonStatus, api } from "./api";
import { App } from "./App";
import { ProjectsPanel } from "./Projects";
import { elsewhere, pickProject } from "./projects";
import { useProjects, useWatchers } from "./useProjects";

// The chosen project is remembered per browser: a convenience, so blocked
// storage only means the daemon's default.
const projectKey = "agent-office.project";
function loadChoice(): string | undefined {
  try {
    return window.localStorage.getItem(projectKey) ?? undefined;
  } catch {
    return undefined;
  }
}
function saveChoice(id: string): void {
  try {
    window.localStorage.setItem(projectKey, id);
  } catch {
    // storage blocked: the choice lasts until reload
  }
}

export function Root({ session, onSignedOut }: { session: AuthSession; onSignedOut: () => void }): JSX.Element {
  const { projects, loaded, connection } = useProjects();
  const [status, setStatus] = useState<DaemonStatus>();
  const [chosen, setChosen] = useState<string | undefined>(loadChoice);
  const [openAgent, setOpenAgent] = useState<string>();
  const [error, setError] = useState<string>();

  // The daemon's status on every (re)connect: a reconnect may be a restarted daemon.
  useEffect(() => {
    if (connection === "open") void api.status().then(setStatus, () => setStatus(undefined));
  }, [connection, projects.length]);

  const current = pickProject(projects, chosen, status?.default_project);
  const worlds = useWatchers(projects, current?.id);
  const others = useMemo(() => elsewhere(projects, current?.id, worlds), [projects, current?.id, worlds]);

  const select = (id: string, agentId?: string): void => {
    setChosen(id);
    saveChoice(id);
    setOpenAgent(agentId);
  };
  const run = async <T,>(fn: () => Promise<T>): Promise<T | undefined> => {
    setError(undefined);
    try {
      return await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return undefined;
    }
  };

  if (!loaded) return <div className="empty">Connecting to agent-office…</div>;
  if (!current || current.state !== "open") {
    return (
      <div className="app no-project">
        <header>
          <h1>agent-office</h1>
          <span className="muted small">{status?.version && `v${status.version}`}</span>
        </header>
        <main className="single">
          {current ? (
            <div className="banner warn">The project {current.name} could not be opened: {current.error}</div>
          ) : (
            <p className="lead-text">No project yet. Add a git repository to start a team on it, or start the daemon with{" "}
              <span className="mono">--repo</span>.</p>
          )}
          {error && <p className="error">{error}</p>}
          <ProjectsPanel projects={projects} current={current?.id} status={status} run={run} onSelect={(id) => select(id)} />
        </main>
      </div>
    );
  }
  return (
    <App key={current.id} session={session} onSignedOut={onSignedOut} project={current} projects={projects}
      daemon={status} elsewhere={others} initialAgent={openAgent} onSelectProject={select} />
  );
}
