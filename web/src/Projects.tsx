// Projects panel (ADR 0046, 0048): add a repository, change a project's
// settings, retry or remove it. Settings left empty inherit the daemon's
// flags, shown as placeholders.
import { type JSX, useState } from "react";
import { type AccessDecider, type Caps, type DaemonStatus, type Project, type ProjectSettings, api } from "./api";
import { capNote, needsStop } from "./caps";
import { formatMoney } from "./usage";

// Failure is the last action's error, shown where that action was asked for.
interface Failure {
  at: string; // "add", "save:<id>", "item:<id>" (retry, remove)
  message: string;
}

// SettingsDraft is the form: text fields, "" = inherit.
export interface SettingsDraft {
  pack: string;
  model: string;
  maxAgents: string;
  maxSubTasks: string; // open sub-tasks per goal (ADR 0062)
  github: "" | "on" | "off";
  budgetSessionUSD: string;
  budgetGoalUSD: string;
  egress: string;
  workerMemory: string; // e.g. 4g (ADR 0051)
  workerPids: string; // e.g. 1024 (Phase 17)
  mergeTarget: string; // e.g. main (ADR 0055)
  accessDecider: "" | AccessDecider; // ADR 0053
  leadHosts: string; // comma-separated
}

const usd = (micro: number | undefined): string => (micro === undefined ? "" : String(micro / 1e6));

export function draftOf(s: ProjectSettings): SettingsDraft {
  return {
    pack: s.pack ?? "", model: s.model ?? "", maxAgents: s.max_agents === undefined ? "" : String(s.max_agents),
    maxSubTasks: s.max_sub_tasks === undefined ? "" : String(s.max_sub_tasks),
    github: s.github === undefined ? "" : s.github ? "on" : "off",
    budgetSessionUSD: usd(s.budget_session_micro_usd), budgetGoalUSD: usd(s.budget_goal_micro_usd),
    egress: (s.egress_allow ?? []).join(", "),
    workerMemory: s.worker_memory ?? "", accessDecider: s.access_decider ?? "",
    workerPids: s.worker_pids ? String(s.worker_pids) : "", mergeTarget: s.merge_target ?? "",
    leadHosts: (s.lead_access_hosts ?? []).join(", "),
  };
}

// microUSD turns dollars typed by the user into integer micro-USD (ADR 0026).
function microUSD(field: string, v: string): number | undefined {
  const t = v.trim();
  if (t === "") return undefined;
  const n = Number(t);
  if (!Number.isFinite(n) || n < 0 || n > 1e6) throw new Error(`${field}: a dollar amount between 0 and 1000000`);
  return Math.round(n * 1e6);
}

// settingsOf turns the form into settings over base: the fields the form
// shows are replaced (empty: inherit), the others are kept.
export function settingsOf(d: SettingsDraft, base: ProjectSettings = {}): ProjectSettings {
  const s: ProjectSettings = { ...base };
  const set = <K extends keyof ProjectSettings>(k: K, v: ProjectSettings[K] | undefined): void => {
    if (v === undefined) delete s[k];
    else s[k] = v;
  };
  set("pack", d.pack.trim() || undefined);
  set("model", d.model.trim() || undefined);
  const max = d.maxAgents.trim();
  if (max !== "" && !/^\d+$/.test(max)) throw new Error("Max agents: a whole number");
  set("max_agents", max === "" ? undefined : Number(max));
  const subs = d.maxSubTasks.trim();
  if (subs !== "" && (!/^\d+$/.test(subs) || Number(subs) < 1 || Number(subs) > 100)) {
    throw new Error("Open sub-tasks per goal: a whole number between 1 and 100");
  }
  set("max_sub_tasks", subs === "" ? undefined : Number(subs));
  set("github", d.github === "" ? undefined : d.github === "on");
  set("budget_session_micro_usd", microUSD("Session budget", d.budgetSessionUSD));
  set("budget_goal_micro_usd", microUSD("Goal budget", d.budgetGoalUSD));
  const egress = d.egress.split(",").map((h) => h.trim()).filter((h) => h !== "");
  set("egress_allow", egress.length > 0 ? egress : undefined);
  const mem = d.workerMemory.trim();
  if (mem !== "" && !/^[1-9][0-9]{0,5}[mg]$/.test(mem)) throw new Error("Worker memory: a size such as 512m or 4g");
  set("worker_memory", mem || undefined);
  const pids = d.workerPids.trim();
  if (pids !== "" && (!/^\d+$/.test(pids) || Number(pids) < 64 || Number(pids) > 65536)) {
    throw new Error("Worker processes: a whole number between 64 and 65536");
  }
  set("worker_pids", pids === "" ? undefined : Number(pids));
  set("merge_target", d.mergeTarget.trim() || undefined);
  set("access_decider", d.accessDecider || undefined);
  const hosts = d.leadHosts.split(",").map((h) => h.trim()).filter((h) => h !== "");
  set("lead_access_hosts", hosts.length > 0 ? hosts : undefined);
  return s;
}

function inherited(d: ProjectSettings | undefined): Record<keyof SettingsDraft, string> {
  const money = (m: number | undefined): string => (m ? formatMoney(m) : "off");
  return {
    pack: d?.pack ?? "", model: d?.model || "CLI default", maxAgents: String(d?.max_agents ?? ""),
    maxSubTasks: String(d?.max_sub_tasks ?? 20),
    github: d?.github ? "on" : "off", budgetSessionUSD: money(d?.budget_session_micro_usd),
    budgetGoalUSD: money(d?.budget_goal_micro_usd), egress: (d?.egress_allow ?? []).join(", ") || "none",
    workerMemory: d?.worker_memory || "pack's or --sandbox-memory", accessDecider: d?.access_decider ?? "user",
    workerPids: d?.worker_pids ? String(d.worker_pids) : "pack's or --sandbox-pids", mergeTarget: d?.merge_target || "main",
    leadHosts: (d?.lead_access_hosts ?? []).join(", ") || "none",
  };
}

function SettingsFields({ draft, onChange, defaults, sandboxed, caps }: {
  draft: SettingsDraft; onChange: (d: SettingsDraft) => void; defaults?: ProjectSettings; sandboxed: boolean;
  caps?: Caps; // an open project's caps now (ADR 0061)
}): JSX.Element {
  const inh = inherited(defaults);
  // The inherited value is shown in full under the field (a placeholder was cut off, Phase 18 review).
  const field = (k: Exclude<keyof SettingsDraft, "github" | "accessDecider">, label: string, help: string): JSX.Element => (
    <label className="setting">
      <span>{label}</span>
      <input value={draft[k]} placeholder="default" title={help} aria-label={label}
        onChange={(e) => onChange({ ...draft, [k]: e.target.value })} />
      <span className="field-hint" data-testid={`hint-${k}`}>Default: {inh[k] || "none"}</span>
    </label>
  );
  return (
    <div className="settings-grid">
      {sandboxed && field("pack", "Stack pack", "go, node, one of yours, or none (agent-office pack list)")}
      {field("model", "Model", "default model of its agents, e.g. haiku; a role's own model wins")}
      <div className="setting-group">
        {field("maxAgents", "Max agents", "its agents at the same time; never more than the daemon's cap")}
        <span className="field-hint" data-testid="cap-note">
          {caps ? capNote(caps) : "Never more than the daemon's cap. The Tech Lead, Architect and QA count too."}
        </span>
      </div>
      {field("maxSubTasks", "Open sub-tasks per goal", "sub-tasks to do, in progress or blocked; reported, done and superseded ones do not count")}
      <label className="setting">
        <span>GitHub</span>
        <select value={draft.github} aria-label="GitHub" onChange={(e) => onChange({ ...draft, github: e.target.value as SettingsDraft["github"] })}>
          <option value="">default ({inh.github})</option>
          <option value="on">on</option>
          <option value="off">off</option>
        </select>
      </label>
      {field("budgetSessionUSD", "Session budget ($)", "refuse API requests once a session's estimated cost reaches this; 0 = off")}
      {field("budgetGoalUSD", "Goal budget ($)", "refuse API requests once a goal's estimated cost reaches this; 0 = off")}
      {sandboxed && field("egress", "Extra egress", "host names its sandboxed agents may reach, comma-separated")}
      {sandboxed && field("workerMemory", "Worker memory", "each sandboxed worker's memory limit, e.g. 4g")}
      {sandboxed && field("workerPids", "Worker processes", "each sandboxed worker's process and thread limit, e.g. 1024")}
      {field("mergeTarget", "Merge target", "the branch a goal's Merge button squash-merges into")}
      {sandboxed && (
        <label className="setting">
          <span>Access decisions</span>
          <select value={draft.accessDecider} aria-label="Access decisions" title="who decides the agents' network access requests"
            onChange={(e) => onChange({ ...draft, accessDecider: e.target.value as SettingsDraft["accessDecider"] })}>
            <option value="">default ({inh.accessDecider})</option>
            <option value="user">you</option>
            <option value="lead">the Tech Lead, with a recorded reason</option>
            <option value="lead_policy">the Tech Lead, for the hosts below; you for the rest</option>
          </select>
        </label>
      )}
      {sandboxed && draft.accessDecider === "lead_policy" &&
        field("leadHosts", "Tech Lead may approve", "hosts the Tech Lead may approve, comma-separated; .example.com for subdomains")}
    </div>
  );
}

const emptyDraft: SettingsDraft = {
  pack: "", model: "", maxAgents: "", maxSubTasks: "", github: "", budgetSessionUSD: "", budgetGoalUSD: "", egress: "", workerMemory: "",
  workerPids: "", mergeTarget: "", accessDecider: "", leadHosts: "",
};

// ProjectForm changes one project's name and settings. Settings left empty
// inherit the daemon's flags; fields the form does not show are kept.
export function ProjectForm({ project: p, status, sandboxed, caps, running = 0, onSaved }: {
  project: Project; status?: DaemonStatus; sandboxed: boolean; caps?: Caps; running?: number; onSaved?: () => void;
}): JSX.Element {
  const [edit, setEdit] = useState<SettingsDraft>(() => draftOf(p.settings));
  const [name, setName] = useState(p.name);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  return (
    <form className="project-settings" onSubmit={(e) => {
      e.preventDefault();
      setError("");
      setSaved(false);
      let settings: ProjectSettings;
      try {
        settings = settingsOf(edit, p.settings);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return;
      }
      api.updateProject(p.id, { name: name.trim(), settings }).then(() => { setSaved(true); onSaved?.(); },
        (err: unknown) => setError(err instanceof Error ? err.message : String(err)));
    }}>
      <label className="setting"><span>Name</span>
        <input value={name} aria-label="Name" onChange={(e) => setName(e.target.value)} />
      </label>
      <SettingsFields draft={edit} onChange={setEdit} defaults={status?.defaults} sandboxed={sandboxed} caps={caps} />
      {(() => {
        let next: ProjectSettings | undefined;
        try {
          next = settingsOf(edit, p.settings);
        } catch {
          next = undefined;
        }
        const stop = next ? needsStop(p.settings, next) : [];
        return running > 0 && stop.length > 0 ? (
          <p className="hint small" data-testid="needs-stop">
            {stop.join(", ")} {stop.length === 1 ? "changes" : "change"} what the agents run with: saving needs this project's agents stopped.
          </p>
        ) : null;
      })()}
      <div className="row">
        <button className="primary" type="submit">Save</button>
        {saved && <span className="small muted" role="status">Saved.</span>}
        {error && <p className="error small" role="alert">{error}</p>}
      </div>
    </form>
  );
}

// AddProjectForm adds a repository as a project (the Projects tab, and
// "+ New project" in the project picker).
export function AddProjectForm({ status, onAdded, autoFocus = false }: {
  status?: DaemonStatus; onAdded: (p: Project) => void; autoFocus?: boolean;
}): JSX.Element {
  const [repo, setRepo] = useState("");
  const [name, setName] = useState("");
  const [draft, setDraft] = useState<SettingsDraft>(emptyDraft);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const sandboxed = status?.sandbox !== "host";
  return (
    <form className="project-add" onSubmit={(e) => {
      e.preventDefault();
      setError("");
      let settings: ProjectSettings;
      try {
        settings = settingsOf(draft);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return;
      }
      setBusy(true);
      api.addProject(repo.trim(), name.trim(), settings).then((p) => {
        setBusy(false);
        setRepo("");
        setName("");
        setDraft(emptyDraft);
        onAdded(p);
      }, (err: unknown) => { setBusy(false); setError(err instanceof Error ? err.message : String(err)); });
    }}>
      <label className="setting"><span>Repository</span>
        <input value={repo} required autoFocus={autoFocus} placeholder="/home/you/projects/app" aria-label="Repository path"
          onChange={(e) => setRepo(e.target.value)} />
        <span className="field-hint">A git repository on this machine; agents work on branches of it.</span>
      </label>
      <label className="setting"><span>Name</span>
        <input value={name} placeholder="the directory's name" aria-label="Project name" onChange={(e) => setName(e.target.value)} />
      </label>
      <p className="small muted">Settings are optional: empty fields use the daemon's.</p>
      <SettingsFields draft={draft} onChange={setDraft} defaults={status?.defaults} sandboxed={sandboxed} />
      <div className="row">
        <button className="primary" type="submit" disabled={repo.trim() === "" || busy}>{busy ? "Adding…" : "Add project"}</button>
        {error && <p className="error small" role="alert">{error}</p>}
      </div>
    </form>
  );
}

// ProjectsPanel lists every project: open one, retry one that could not
// open, remove one, or open its settings; and adds a project.
export function ProjectsPanel({ projects, current, status, running, onSelect, onSettings }: {
  projects: Project[]; current?: string; status?: DaemonStatus;
  running: number; // agents that have not exited, in every project: live, unlike the status
  onSelect: (id: string) => void;
  onSettings?: (id: string) => void; // the project's own settings (the gear by the picker)
}): JSX.Element {
  const [failure, setFailure] = useState<Failure>();
  const act = async <T,>(at: string, fn: () => Promise<T>): Promise<T | undefined> => {
    setFailure(undefined);
    try {
      return await fn();
    } catch (e) {
      setFailure({ at, message: e instanceof Error ? e.message : String(e) });
      return undefined;
    }
  };
  const errorAt = (at: string): JSX.Element | null =>
    failure?.at === at ? <p className="error small" role="alert">{failure.message}</p> : null;
  const [removing, setRemoving] = useState<string>();
  return (
    <section className="side-panel projects-panel" aria-label="Projects">
      <h2>Projects</h2>
      <p className="muted small">
        Each project is one repository with its own team, board, inbox, memory and office floor.
        {status && ` At most ${status.max_agents} agents run across all projects (${running} now).`}
      </p>
      {status && <DaemonCap status={status} />}
      <ul className="project-list">
        {projects.map((p) => (
          <li key={p.id} className={`project-item ${p.id === current ? "current" : ""}`}>
            <div className="project-head">
              <strong>{p.name}</strong>
              {p.default && <span className="badge small">default</span>}
              <span className={`badge small state-${p.state}`}>{p.state === "open" ? "open" : "could not open"}</span>
              <span className="mono small muted" title={p.repo_path}>{p.repo_path}</span>
            </div>
            <div className="small muted">
              pack {p.effective.pack === "none" || !p.effective.pack ? "none" : p.effective.pack} · model{" "}
              {p.effective.model || "CLI default"} · max {p.effective.max_agents} agents · GitHub{" "}
              {p.effective.github ? "on" : "off"}
            </div>
            {p.error && <p className="error small">{p.error}</p>}
            <div className="actions">
              {p.id !== current && p.state === "open" && <button onClick={() => onSelect(p.id)}>Open</button>}
              {p.state === "error" && <button onClick={() => void act(`item:${p.id}`, () => api.retryProject(p.id))}>Retry</button>}
              {onSettings && p.state === "open" && <button onClick={() => onSettings(p.id)}>Settings</button>}
              {removing === p.id ? (
                <>
                  <span className="small">Remove {p.name}? Its history, branches and worktrees are kept.</span>
                  <button className="danger" onClick={() => void act(`item:${p.id}`, async () => {
                    await api.removeProject(p.id);
                    setRemoving(undefined);
                    return true;
                  })}>Remove</button>
                  <button onClick={() => setRemoving(undefined)}>Cancel</button>
                </>
              ) : (
                <button onClick={() => setRemoving(p.id)}>Remove…</button>
              )}
            </div>
            {errorAt(`item:${p.id}`)}
          </li>
        ))}
      </ul>
      <h3>Add a project</h3>
      <AddProjectForm status={status} onAdded={(p) => onSelect(p.id)} />
    </section>
  );
}

// DaemonCap changes the daemon-wide agent cap without a restart (ADR 0061):
// saved, it wins over --max-agents; "Use --max-agents" clears it.
function DaemonCap({ status }: { status: DaemonStatus }): JSX.Element {
  const [value, setValue] = useState(String(status.max_agents));
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const save = (n: number | null): void => {
    setError("");
    setSaved(false);
    api.setDaemonMaxAgents(n).then((st) => { setValue(String(st.max_agents)); setSaved(true); },
      (err: unknown) => setError(err instanceof Error ? err.message : String(err)));
  };
  return (
    <form className="daemon-cap row" aria-label="Daemon-wide agent cap" onSubmit={(e) => {
      e.preventDefault();
      const n = Number(value.trim());
      if (!/^\d+$/.test(value.trim()) || n < 1 || n > 32) {
        setError("Max agents (all projects): a whole number between 1 and 32");
        return;
      }
      save(n);
    }}>
      <label className="setting"><span>Max agents (all projects)</span>
        <input value={value} inputMode="numeric" aria-label="Max agents (all projects)" onChange={(e) => setValue(e.target.value)} />
        <span className="field-hint">
          {status.max_agents_saved
            ? `Set here; --max-agents is ${status.max_agents_flag ?? "?"}. Applies at once; running agents keep running.`
            : "From --max-agents. A value saved here wins over the flag and holds after a restart."}
        </span>
      </label>
      <button type="submit">Save</button>
      {status.max_agents_saved && <button type="button" className="link" onClick={() => save(null)}>Use --max-agents</button>}
      {saved && <span className="small muted" role="status">Saved.</span>}
      {error && <p className="error small" role="alert">{error}</p>}
    </form>
  );
}
