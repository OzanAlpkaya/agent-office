// The office layout editor in the project settings (Phase 18, ADR 0057):
// the default office or a layout of the project's own, as YAML, started from
// a preset. The daemon checks the YAML as you type; the preview is drawn
// from the same floor plan code as the office.
import { type JSX, useEffect, useMemo, useState } from "react";
import { type LayoutCheck, type LayoutPreset, type OfficeLayout, type Project, api } from "../api";
import { customPlan } from "./custom";
import { type FloorKind, type FloorPlan, deskFootprint, floorPlan } from "./floor";

const floorFill: Record<FloorKind, string> = {
  carpet: "#3a2e4a", tile: "#2c3442", kitchen: "#3d3528", wood: "#4a3626", blue: "#25324d", lab: "#2b3a3a", rug: "#4a2a33",
};

const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// layoutWarnings are what is valid but worth saying: fewer desks than agents may run.
export function layoutWarnings(l: OfficeLayout | undefined, maxAgents: number | undefined): string[] {
  if (!l || maxAgents === undefined) return [];
  const own = 1 + (l.rooms.some((r) => r.id === "planning") ? 1 : 0) + (l.rooms.some((r) => r.id === "qa") ? 1 : 0) + 1;
  const need = Math.max(0, maxAgents - own); // the lead, the stations and the assistant have their own places
  return l.desks < need
    ? [`Up to ${maxAgents} agents may run, and ${l.desks} desks may be too few: agents beyond them work without a desk ` +
      "(they are listed in the office bar until one frees up)."]
    : [];
}

export default function LayoutSettings({ project }: { project: Project }): JSX.Element {
  const stored = project.settings.office_layout ?? "";
  const [yaml, setYaml] = useState(stored);
  const [mode, setMode] = useState<"default" | "custom">(stored.trim() ? "custom" : "default");
  const [presets, setPresets] = useState<LayoutPreset[]>([]);
  const [check, setCheck] = useState<LayoutCheck>();
  const [state, setState] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState("");

  useEffect(() => {
    api.officeLayouts().then(setPresets, (e: unknown) => setError(errText(e)));
  }, []);
  // Check as you type, a moment after the last key.
  useEffect(() => {
    if (mode !== "custom") return;
    let live = true;
    const t = window.setTimeout(() => {
      api.checkOfficeLayout(yaml).then((c) => { if (live) setCheck(c); }, (e: unknown) => { if (live) setError(errText(e)); });
    }, 250);
    return () => { live = false; window.clearTimeout(t); };
  }, [yaml, mode]);

  const layout = mode === "custom" ? check?.layout : undefined;
  const problems = mode === "custom" ? check?.problems ?? [] : [];
  const warnings = layoutWarnings(layout, project.effective.max_agents);
  const plan = useMemo(() => (layout ? customPlan(layout) : floorPlan(4, 2)), [layout]);
  const dirty = mode === "default" ? stored.trim() !== "" : yaml !== stored;
  const chosen = presets.find((p) => p.yaml === yaml)?.name;

  const save = (): void => {
    setState("saving");
    setError("");
    const settings = { ...project.settings };
    if (mode === "custom") settings.office_layout = yaml;
    else delete settings.office_layout;
    api.updateProject(project.id, { settings }).then(() => setState("saved"), (e: unknown) => { setError(errText(e)); setState("idle"); });
  };

  return (
    <div className="layout-settings stack" data-testid="layout-settings">
      <div className="row">
        <div className="segmented" role="group" aria-label="Office layout">
          <button aria-pressed={mode === "default"} onClick={() => { setMode("default"); setState("idle"); }}>Default office</button>
          <button aria-pressed={mode === "custom"} onClick={() => {
            setMode("custom");
            setState("idle");
            if (!yaml.trim() && presets[0]) setYaml(presets[0].yaml);
          }}>Own layout</button>
        </div>
        {project.office?.problems && project.office.problems.length > 0 && (
          <span className="small hint" role="note">The stored layout is not valid; the office shows the default.</span>
        )}
      </div>
      {mode === "default" ? (
        <p className="small muted">Every room, and as many desks as agents at work: the workroom grows with the team.</p>
      ) : (
        <div className="layout-presets" role="group" aria-label="Presets">
          <span className="small muted">Start from:</span>
          {presets.map((p) => (
            <button key={p.name} className="sm" aria-pressed={chosen === p.name} title={p.description}
              onClick={() => { setYaml(p.yaml); setState("idle"); }}>{p.title}</button>
          ))}
        </div>
      )}
      <div className="layout-editor">
        {mode === "custom" && (
          <label className="setting">
            <span>Layout (YAML): rooms on a grid of 16-pixel tiles, the workroom's desks, and the doors between rooms</span>
            <textarea value={yaml} spellCheck={false} aria-label="Office layout YAML" maxLength={8192}
              onChange={(e) => { setYaml(e.target.value); setState("idle"); }} />
          </label>
        )}
        <div className="stack">
          <div className="layout-preview" data-testid="layout-preview" data-valid={mode === "default" || layout ? "true" : "false"}>
            {mode === "custom" && !layout
              ? <p className="muted small">{check ? "Fix the layout to see it." : "Checking…"}</p>
              : <PlanPreview plan={plan} />}
          </div>
          {mode === "custom" && layout && (
            <p className="small muted" data-testid="layout-summary">
              {layout.rooms.length} rooms · {layout.desks} desks · {layout.columns} × {layout.rows} tiles
              {(["planning", "qa", "review", "kitchen"] as const).filter((r) => !layout.rooms.some((x) => x.id === r)).length > 0 &&
                ` · without ${(["planning", "qa", "review", "kitchen"] as const).filter((r) => !layout.rooms.some((x) => x.id === r)).join(", ")}`}
            </p>
          )}
          {problems.length > 0 && <ul className="layout-problems" role="alert" data-testid="layout-problems">{problems.map((p) => <li key={p}>{p}</li>)}</ul>}
          {warnings.length > 0 && <ul className="layout-warnings" role="note">{warnings.map((w) => <li key={w}>{w}</li>)}</ul>}
        </div>
      </div>
      <div className="row">
        <button className="primary" disabled={!dirty || state === "saving" || (mode === "custom" && !layout)} onClick={save}>
          {state === "saving" ? "Saving…" : "Save layout"}
        </button>
        {state === "saved" && !dirty && <span className="small muted" role="status">Saved: the office uses it now.</span>}
        {state === "saved" && dirty && <span className="small muted" role="status">Saved.</span>}
        {error && <span className="error small" role="alert">{error}</span>}
      </div>
    </div>
  );
}

// PlanPreview draws a floor plan as a schematic: floors, back walls, walls,
// doorways, desks and the rooms' names.
export function PlanPreview({ plan: p }: { plan: FloorPlan }): JSX.Element {
  const desk = (x: number, y: number, i: string): JSX.Element => (
    <rect key={i} x={x + deskFootprint.x} y={y + deskFootprint.y + 12} width={deskFootprint.w} height={14} rx={2} fill="#8a6a45" />
  );
  return (
    <svg viewBox={`0 0 ${p.w} ${p.h}`} role="img" aria-label={`Floor plan: ${p.rooms.map((r) => r.name).join(", ")}`}>
      {p.rooms.map((r) => <rect key={r.id} x={r.rect.x} y={r.rect.y} width={r.rect.w} height={r.rect.h} fill={floorFill[r.floor]} />)}
      {p.faces.map((f, i) => <rect key={`f${i}`} x={f.x} y={f.y} width={f.w} height={f.h} fill="#2a2f3d" />)}
      {p.doorways.map((d, i) => <rect key={`d${i}`} x={d.x} y={d.y} width={d.w} height={d.h} fill="#5b6378" opacity={0.5} />)}
      {p.walls.map((w, i) => <rect key={`w${i}`} x={w.x} y={w.y} width={w.w} height={w.h} fill="#c9ccd6" />)}
      {desk(p.lead.x, p.lead.y, "lead")}
      {desk(p.userDesk.x, p.userDesk.y, "user")}
      {p.desks.map((d, i) => desk(d.x, d.y, `desk${i}`))}
      {p.table && <rect x={p.table.x} y={p.table.y} width={p.table.w} height={p.table.h} rx={2} fill="#7a5c3c" />}
      {Object.values(p.stations).map((st) => (
        <rect key={st.kind} x={st.furniture.x} y={st.furniture.y} width={st.furniture.w} height={st.furniture.h} rx={2} fill="#6d7fa6" />
      ))}
      <rect x={p.door.x} y={p.door.y} width={22} height={34} fill="#7a5233" />
      {p.rooms.map((r) => (
        <text key={`t${r.id}`} x={r.label.x} y={r.label.y + 10} fontSize={10} fill="#e7e9ef" fontFamily="system-ui, sans-serif">{r.name}</text>
      ))}
    </svg>
  );
}
