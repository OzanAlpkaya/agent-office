// Root chooses the project the UI shows (ADR 0048) and watches the others
// for the Approvals badge. Switching remounts the App, so nothing of one
// project's view leaks into another's.
import { type JSX, useEffect, useMemo, useState } from "react";
import { type AuthSession, type DaemonStatus, api } from "./api";
import { App, type MainTab } from "./App";
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
  const { projects, loaded, connection, presence, profile } = useProjects();
  const [status, setStatus] = useState<DaemonStatus>();
  const [chosen, setChosen] = useState<string | undefined>(loadChoice);
  const [openAgent, setOpenAgent] = useState<string>();
  const [openTab, setOpenTab] = useState<MainTab>();

  // The daemon's status on every (re)connect: a reconnect may be a restarted daemon.
  useEffect(() => {
    if (connection === "open") void api.status().then(setStatus, () => setStatus(undefined));
  }, [connection, projects.length]);

  // The daemon's default (serve --repo's) comes with the project list, so no project opens before it is known.
  const current = pickProject(projects, chosen, projects.find((p) => p.default)?.id);
  const worlds = useWatchers(projects, current?.id);
  const others = useMemo(() => elsewhere(projects, current?.id, worlds), [projects, current?.id, worlds]);

  const select = (id: string, agentId?: string, tab?: MainTab): void => {
    setChosen(id);
    saveChoice(id);
    setOpenAgent(agentId);
    setOpenTab(tab);
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
          <ProjectsPanel projects={projects} current={current?.id} status={status}
            running={others.reduce((n, e) => n + e.running, 0)} onSelect={(id) => select(id)} />
        </main>
      </div>
    );
  }
  return (
    <App key={current.id} session={session} onSignedOut={onSignedOut} project={current} projects={projects}
      daemon={status} elsewhere={others} initialAgent={openAgent} initialTab={openTab} onSelectProject={select} presence={presence}
      profile={profile} />
  );
}
