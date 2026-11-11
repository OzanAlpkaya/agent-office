// Projects panel (ADR 0046, 0048): add a repository, change a project's
// settings, retry or remove it. Settings left empty inherit the daemon's
// flags, shown as placeholders.
import { type JSX, useState } from "react";
import { type DaemonStatus, type Project, type ProjectSettings, api } from "./api";
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
  github: "" | "on" | "off";
  budgetSessionUSD: string;
  budgetGoalUSD: string;
  egress: string;
}

const usd = (micro: number | undefined): string => (micro === undefined ? "" : String(micro / 1e6));

export function draftOf(s: ProjectSettings): SettingsDraft {
  return {
    pack: s.pack ?? "", model: s.model ?? "", maxAgents: s.max_agents === undefined ? "" : String(s.max_agents),
    github: s.github === undefined ? "" : s.github ? "on" : "off",
    budgetSessionUSD: usd(s.budget_session_micro_usd), budgetGoalUSD: usd(s.budget_goal_micro_usd),
    egress: (s.egress_allow ?? []).join(", "),
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
  set("github", d.github === "" ? undefined : d.github === "on");
  set("budget_session_micro_usd", microUSD("Session budget", d.budgetSessionUSD));
  set("budget_goal_micro_usd", microUSD("Goal budget", d.budgetGoalUSD));
  const egress = d.egress.split(",").map((h) => h.trim()).filter((h) => h !== "");
  set("egress_allow", egress.length > 0 ? egress : undefined);
  return s;
}

function inherited(d: ProjectSettings | undefined): Record<keyof SettingsDraft, string> {
  const money = (m: number | undefined): string => (m ? formatMoney(m) : "off");
  return {
    pack: d?.pack ?? "", model: d?.model || "CLI default", maxAgents: String(d?.max_agents ?? ""),
    github: d?.github ? "on" : "off", budgetSessionUSD: money(d?.budget_session_micro_usd),
    budgetGoalUSD: money(d?.budget_goal_micro_usd), egress: (d?.egress_allow ?? []).join(", ") || "none",
  };
}

function SettingsFields({ draft, onChange, defaults, sandboxed }: {
  draft: SettingsDraft; onChange: (d: SettingsDraft) => void; defaults?: ProjectSettings; sandboxed: boolean;
}): JSX.Element {
  const inh = inherited(defaults);
  const field = (k: Exclude<keyof SettingsDraft, "github">, label: string, help: string): JSX.Element => (
    <label className="setting">
      <span>{label}</span>
      <input value={draft[k]} placeholder={`daemon: ${inh[k] || "none"}`} title={help} aria-label={label}
        onChange={(e) => onChange({ ...draft, [k]: e.target.value })} />
    </label>
  );
  return (
    <div className="settings-grid">
      {sandboxed && field("pack", "Stack pack", "go, node, one of yours, or none (agent-office pack list)")}
      {field("model", "Model", "default model of its agents, e.g. haiku; a role's own model wins")}
      {field("maxAgents", "Max agents", "its agents at the same time; never more than the daemon's cap")}
      <label className="setting">
        <span>GitHub</span>
        <select value={draft.github} aria-label="GitHub" onChange={(e) => onChange({ ...draft, github: e.target.value as SettingsDraft["github"] })}>
          <option value="">daemon: {inh.github}</option>
          <option value="on">on</option>
          <option value="off">off</option>
        </select>
      </label>
      {field("budgetSessionUSD", "Session budget ($)", "refuse API requests once a session's estimated cost reaches this; 0 = off")}
      {field("budgetGoalUSD", "Goal budget ($)", "refuse API requests once a goal's estimated cost reaches this; 0 = off")}
      {sandboxed && field("egress", "Extra egress", "host names its sandboxed agents may reach, comma-separated")}
    </div>
  );
}

const emptyDraft: SettingsDraft = { pack: "", model: "", maxAgents: "", github: "", budgetSessionUSD: "", budgetGoalUSD: "", egress: "" };

export function ProjectsPanel({ projects, current, status, running, onSelect }: {
  projects: Project[]; current?: string; status?: DaemonStatus;
  running: number; // agents that have not exited, in every project: live, unlike the status
  onSelect: (id: string) => void;
}): JSX.Element {
  const [failure, setFailure] = useState<Failure>();
  // act runs one action of the panel; its error is shown next to the control that asked for it.
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
  const [repo, setRepo] = useState("");
  const [name, setName] = useState("");
  const [draft, setDraft] = useState<SettingsDraft>(emptyDraft);
  const [editing, setEditing] = useState<string>();
  const [edit, setEdit] = useState<SettingsDraft>(emptyDraft);
  const [editName, setEditName] = useState("");
  const [removing, setRemoving] = useState<string>();
  const sandboxed = status?.sandbox !== "host";

  const add = async (): Promise<void> => {
    const p = await act("add", () => api.addProject(repo.trim(), name.trim(), settingsOf(draft)));
    if (p) {
      setRepo("");
      setName("");
      setDraft(emptyDraft);
      onSelect(p.id);
    }
  };
  return (
    <section className="side-panel projects-panel" aria-label="Projects">
      <h2>Projects</h2>
      <p className="muted small">
        Each project is one repository with its own team, board, inbox, memory and office floor.
        {status && ` At most ${status.max_agents} agents run across all projects (${running} now).`}
      </p>
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
              <button aria-expanded={editing === p.id} onClick={() => {
                setEditing(editing === p.id ? undefined : p.id);
                setEdit(draftOf(p.settings));
                setEditName(p.name);
              }}>Settings</button>
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
            {editing === p.id && (
              <form className="project-settings" onSubmit={(e) => {
                e.preventDefault();
                void act(`save:${p.id}`, async () => {
                  const next = await api.updateProject(p.id, { name: editName.trim(), settings: settingsOf(edit, p.settings) });
                  setEditing(undefined);
                  return next;
                });
              }}>
                <label className="setting"><span>Name</span>
                  <input value={editName} aria-label="Name" onChange={(e) => setEditName(e.target.value)} />
                </label>
                <SettingsFields draft={edit} onChange={setEdit} defaults={status?.defaults} sandboxed={sandboxed} />
                <p className="muted small">New settings restart nothing: they are refused while the project's agents run.</p>
                <div className="row">
                  <button className="primary" type="submit">Save</button>
                  {errorAt(`save:${p.id}`)}
                </div>
              </form>
            )}
          </li>
        ))}
      </ul>
      <form className="project-add" onSubmit={(e) => {
        e.preventDefault();
        void add();
      }}>
        <h3>Add a project</h3>
        <label className="setting"><span>Repository</span>
          <input value={repo} required placeholder="/home/you/projects/app" aria-label="Repository path"
            onChange={(e) => setRepo(e.target.value)} />
        </label>
        <label className="setting"><span>Name</span>
          <input value={name} placeholder="the directory's name" aria-label="Project name" onChange={(e) => setName(e.target.value)} />
        </label>
        <SettingsFields draft={draft} onChange={setDraft} defaults={status?.defaults} sandboxed={sandboxed} />
        <div className="row">
          <button className="primary" type="submit" disabled={repo.trim() === ""}>Add project</button>
          {errorAt("add")}
        </div>
      </form>
    </section>
  );
}
