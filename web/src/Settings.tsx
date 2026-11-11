// Settings (Phase 18): one page with tabs for what used to open over the
// board and push it down: the shown project's settings, its office layout
// and branch clean-up; every project; the sandbox and access grants; the
// project memory; the paired devices.
import { type JSX, type ReactNode, Suspense, lazy } from "react";
import type { AccessRequest, DaemonStatus, MemoryDraft, MemoryEntry, Project, ProjectApi, Status } from "./api";
import { DevicesPanel } from "./Devices";
import { BranchCleanup } from "./Goals";
import { GrantsPanel } from "./Grants";
import { ProjectForm, ProjectsPanel } from "./Projects";
import { MemoryPanel } from "./Team";

// The layout editor draws its preview with the office's floor plan code; load it with the tab.
const LayoutSettings = lazy(() => import("./office/LayoutSettings"));

export type SettingsTab = "project" | "projects" | "sandbox" | "memory" | "devices";

const tabLabel: Record<SettingsTab, string> = {
  project: "Project", projects: "Projects", sandbox: "Sandbox", memory: "Memory", devices: "Devices",
};

export interface SettingsProps {
  tab: SettingsTab;
  onTab: (t: SettingsTab) => void;
  project: Project;
  projects: Project[];
  status?: Status; // this project's
  daemon?: DaemonStatus;
  version: string;
  deviceName?: string; // this browser's name as paired
  running: number; // agents running in every project
  api: ProjectApi;
  accessRequests: AccessRequest[];
  memory: MemoryEntry[];
  run: <T>(fn: () => Promise<T>) => Promise<T | undefined>;
  onSignedOut: () => void;
  onSelectProject: (id: string) => void;
}

export function Settings(p: SettingsProps): JSX.Element {
  const sandboxed = p.status?.sandbox === "podman";
  const tabs: SettingsTab[] = ["project", "projects", ...(sandboxed ? ["sandbox" as const] : []), "memory", "devices"];
  const tab = tabs.includes(p.tab) ? p.tab : "project";
  const proposed = p.memory.filter((m) => m.status === "proposed").length;
  const ok = async (fn: () => Promise<unknown>): Promise<boolean> => (await p.run(fn)) !== undefined;
  return (
    <div className="settings-page">
      <div className="page-head">
        <h1>Settings</h1>
        <p className="muted small">{p.project.name} · <span className="mono">{p.project.repo_path}</span></p>
      </div>
      <div className="tab-list" role="tablist" aria-label="Settings">
        {tabs.map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} onClick={() => p.onTab(t)} data-tab={t}>
            {tabLabel[t]}
            {t === "memory" && proposed > 0 && <span className="count tone-warn">{proposed}</span>}
          </button>
        ))}
      </div>
      <div className="settings-body" role="tabpanel" aria-label={tabLabel[tab]}>
        {tab === "project" && (
          <>
            <Section title="Project settings"
              lede="Empty fields use the daemon's flags. Changing what agents run with is refused while the project's agents run; the name and the office layout are not.">
              <ProjectForm project={p.project} status={p.daemon} sandboxed={sandboxed} />
            </Section>
            <Section title="Office layout" id="office-layout"
              lede="Which rooms the office has, where they sit and how many desks the workroom has. Pick a preset, change its YAML, and check the preview.">
              <Suspense fallback={<p className="muted">Loading the layout editor…</p>}>
                <LayoutSettings project={p.project} />
              </Suspense>
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
            <p className="muted small settings-version">
              agent-office {p.version ? `v${p.version}` : ""}{p.deviceName ? ` · this device: ${p.deviceName}` : ""}
            </p>
          </>
        )}
        {tab === "projects" && (
          <ProjectsPanel projects={p.projects} current={p.project.id} status={p.daemon} running={p.running} onSelect={p.onSelectProject} />
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
        {tab === "devices" && <DevicesPanel remoteHosts={p.status?.remote_hosts ?? []} run={p.run} onSignedOut={p.onSignedOut} />}
      </div>
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
