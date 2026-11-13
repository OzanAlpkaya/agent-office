// Settings (Phase 18, split in its review): the shown project's own settings
// behind the gear by the project picker (general, office layout, sandbox,
// memory), and the app's behind Settings in the rail (projects, devices,
// the user's profile).
import { type JSX, type ReactNode, Suspense, lazy, useLayoutEffect, useRef, useState } from "react";
import type { AccessRequest, Caps, DaemonStatus, MemoryDraft, MemoryEntry, Project, ProjectApi, Status } from "./api";
import { DevicesPanel } from "./Devices";
import { BranchCleanup } from "./Goals";
import { GrantsPanel } from "./Grants";
import type { Profile } from "./office/avatar";
import { ProjectForm, ProjectsPanel } from "./Projects";
import { MemoryPanel } from "./Team";

// Both draw with the office's code; loaded with their tab.
const LayoutSettings = lazy(() => import("./office/LayoutSettings"));
const AvatarCreator = lazy(() => import("./office/AvatarCreator"));

export type ProjectTab = "general" | "office" | "sandbox" | "memory";
export type AppTab = "projects" | "devices" | "profile";

const projectTabLabel: Record<ProjectTab, string> = { general: "General", office: "Office", sandbox: "Sandbox", memory: "Memory" };
const appTabLabel: Record<AppTab, string> = { projects: "Projects", devices: "Devices", profile: "Profile" };

function Tabs<T extends string>({ label, tabs, names, current, onTab, counts = {} }: {
  label: string; tabs: T[]; names: Record<T, string>; current: T; onTab: (t: T) => void; counts?: Partial<Record<T, number>>;
}): JSX.Element {
  // A strip that overflows fades at its right edge, so it shows that it scrolls.
  const ref = useRef<HTMLDivElement>(null);
  const [fits, setFits] = useState(true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const check = (): void => setFits(el.scrollWidth <= el.clientWidth + 1);
    check();
    const ro = new ResizeObserver(check);
    ro.observe(el);
    return () => ro.disconnect();
  }, [tabs.length]);
  return (
    <div className={`tab-list ${fits ? "fits" : "overflows"}`} role="tablist" aria-label={label} ref={ref}>
      {tabs.map((t) => (
        <button key={t} role="tab" aria-selected={current === t} onClick={() => onTab(t)} data-tab={t}>
          {names[t]}
          {(counts[t] ?? 0) > 0 && <span className="count tone-warn">{counts[t]}</span>}
        </button>
      ))}
    </div>
  );
}

function Section({ title, lede, id, children }: { title: string; lede?: string; id?: string; children: ReactNode }): JSX.Element {
  return (
    <section className="section-card" id={id} aria-label={title}>
      <h2>{title}</h2>
      {lede && <p className="lede">{lede}</p>}
      {children}
    </section>
  );
}

export interface ProjectSettingsProps {
  tab: ProjectTab;
  onTab: (t: ProjectTab) => void;
  project: Project;
  status?: Status; // this project's
  daemon?: DaemonStatus;
  api: ProjectApi;
  accessRequests: AccessRequest[];
  memory: MemoryEntry[];
  run: <T>(fn: () => Promise<T>) => Promise<T | undefined>;
  caps?: Caps; // live changes (ADR 0061); the status has the first value
  running: number; // the project's agents that have not exited
  onDaemonCap?: () => void; // Phase 20: open Settings → App at the daemon-wide cap
}

// ProjectSettings is one project's settings, titled with its name.
export function ProjectSettings(p: ProjectSettingsProps): JSX.Element {
  const sandboxed = p.status?.sandbox === "podman";
  const tabs: ProjectTab[] = ["general", "office", ...(sandboxed ? ["sandbox" as const] : []), "memory"];
  const tab = tabs.includes(p.tab) ? p.tab : "general";
  const proposed = p.memory.filter((m) => m.status === "proposed").length;
  const ok = async (fn: () => Promise<unknown>): Promise<boolean> => (await p.run(fn)) !== undefined;
  return (
    <div className="settings-page project-settings-page">
      <div className="page-head">
        <h1>{p.project.name}</h1>
        <p className="muted small">Project settings · <span className="mono">{p.project.repo_path}</span></p>
      </div>
      <Tabs label="Project settings" tabs={tabs} names={projectTabLabel} current={tab} onTab={p.onTab} counts={{ memory: proposed }} />
      <div className="settings-body" role="tabpanel" aria-label={projectTabLabel[tab]}>
        {tab === "general" && (
          <>
            <Section title="General"
              lede="Empty fields use the daemon's flags. Models, caps, budgets, worker limits, access decisions and the merge target apply to the next agent start, also while agents run. The stack pack, network, secrets, GitHub, reviewer and role CLIs need the project's agents stopped.">
              <ProjectForm project={p.project} status={p.daemon} sandboxed={sandboxed} caps={p.caps ?? p.status?.caps}
                running={p.running} onDaemonCap={p.onDaemonCap} />
            </Section>
            <Section title="Branch clean-up" id="branch-cleanup"
              lede="Delete old agent-office branches whose work is in the merge target. Branches that are not merged are listed apart and deleted only after a second confirmation; every deleted head is recorded.">
              <BranchCleanup />
            </Section>
            {p.status?.github && (
              <Section title="GitHub">
                <p className="small">
                  Pushes to <span className="mono">{p.status.github.remote}</span>, pull requests into{" "}
                  <span className="mono">{p.status.github.base}</span> on <span className="mono">{p.status.github.repo}</span>; review
                  by {p.status.github.reviewer}, {p.status.github.rounds} automatic follow-up round(s). agent-office never merges a pull request.
                </p>
              </Section>
            )}
          </>
        )}
        {tab === "office" && (
          <Section title="Office layout" id="office-layout"
            lede="Which rooms the office has, where they sit and how many desks the workroom has. Pick a preset, change its YAML, and check the preview.">
            <Suspense fallback={<p className="muted">Loading the layout editor…</p>}>
              <LayoutSettings project={p.project} />
            </Suspense>
          </Section>
        )}
        {tab === "sandbox" && p.status && (
          <>
            <Section title="Sandbox"><PackPanel status={p.status} /></Section>
            <Section title="Access grants" lede="Hosts allowed beyond the allowlist, for one agent or the whole project. Revoking one takes effect at once.">
              <GrantsPanel api={p.api} requests={p.accessRequests} />
            </Section>
          </>
        )}
        {tab === "memory" && (
          <MemoryPanel entries={p.memory}
            onAdd={(d: MemoryDraft) => ok(() => p.api.addMemory(d))}
            onEdit={(m, patch) => ok(() => p.api.editMemory(m.id, patch))}
            onAccept={(m) => void p.run(() => p.api.acceptMemory(m.id))}
            onDelete={(m) => void p.run(async () => { await p.api.deleteMemory(m.id); return true; })} />
        )}
      </div>
    </div>
  );
}

export interface AppSettingsProps {
  tab: AppTab;
  onTab: (t: AppTab) => void;
  project: Project; // the shown one
  projects: Project[];
  daemon?: DaemonStatus;
  remoteHosts: string[];
  version: string;
  deviceName?: string; // this browser's name as paired
  running: number; // agents running in every project
  remote?: boolean; // this page came through tailscale serve: the daemon-wide cap is this machine's only
  profile?: Profile;
  run: <T>(fn: () => Promise<T>) => Promise<T | undefined>;
  onSignedOut: () => void;
  onSelectProject: (id: string) => void;
  onProjectSettings: (id: string) => void;
  capFocusAt?: number; // Phase 20: focus the daemon-wide cap
}

// AppSettings is the app's: every project, the paired devices, the user's profile.
export function AppSettings(p: AppSettingsProps): JSX.Element {
  const tabs: AppTab[] = ["projects", "devices", "profile"];
  return (
    <div className="settings-page app-settings-page">
      <div className="page-head">
        <h1>Settings</h1>
        <p className="muted small">
          agent-office {p.version ? `v${p.version}` : ""}{p.deviceName ? ` · this device: ${p.deviceName}` : ""}
        </p>
      </div>
      <Tabs label="Settings" tabs={tabs} names={appTabLabel} current={p.tab} onTab={p.onTab} />
      <div className="settings-body" role="tabpanel" aria-label={appTabLabel[p.tab]}>
        {p.tab === "projects" && (
          <ProjectsPanel projects={p.projects} current={p.project.id} status={p.daemon} running={p.running} remote={p.remote}
            onSelect={p.onSelectProject} onSettings={p.onProjectSettings} capFocusAt={p.capFocusAt} />
        )}
        {p.tab === "devices" && <DevicesPanel remoteHosts={p.remoteHosts} run={p.run} onSignedOut={p.onSignedOut} />}
        {p.tab === "profile" && (
          <Section title="Profile" lede="Your character in the office: it sits at your desk on every project's floor while a device has agent-office open.">
            <Suspense fallback={<p className="muted">Loading…</p>}>
              <AvatarCreator profile={p.profile} />
            </Suspense>
          </Section>
        )}
      </div>
    </div>
  );
}

// PackPanel shows what the stack pack gives sandboxed agents (ADR 0023).
function PackPanel({ status }: { status: Status }): JSX.Element {
  const p = status.pack;
  const list = (xs: string[] | undefined): string => (xs && xs.length > 0 ? xs.join(", ") : "none");
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
        <dt>Worker limits</dt>
        <dd className="mono">{status.worker_memory ?? "default"} memory · {status.worker_pids ?? "default"} processes</dd>
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
