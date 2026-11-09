// The Office tab (ADR 0024): a lazy-loaded Pixi scene of the team, with HTML
// nameplates over it. It reads the same world as the rest of the UI and has
// one action: open an agent.
import { type JSX, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { AccessRequest, Agent, Message, Task, TaskStatus } from "../api";
import { type Assignment, emptyAssignment, inCell, planOf, slotOrigin, whiteboardAt } from "./layout";
import { buildOffice, freshMessages } from "./model";
import { type Pose, poseLabel } from "./pose";
import { whiteboardSize } from "./art";
import { OfficeScene, type Transform, type Whereabouts } from "./scene";

export interface OfficeProps {
  agents: Agent[];
  tasks: Task[];
  messages: Record<string, Message[]>;
  accessRequests: AccessRequest[];
  onOpenAgent: (id: string) => void;
}

const poseOrder: Pose[] = ["typing", "reviewing", "waiting", "idle", "booting"];

// Where a character is, in the nameplate's tooltip (ADR 0045).
const whereLabel: Record<Whereabouts, string> = {
  desk: "", walking: "Walking", whiteboard: "Reporting at the whiteboard", coffee: "At the coffee machine", review: "In the review room",
};

const statusLabel: Record<TaskStatus, string> = {
  todo: "to do", in_progress: "in progress", review: "in review", done: "done", blocked: "blocked",
};

// Who sits where, kept for the page's lifetime rather than the component's,
// so leaving the tab and coming back does not move anyone. A reload starts
// over (in start order).
let keptAssignment: Assignment = emptyAssignment;

function reducedMotion(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export default function Office({ agents, tasks, messages, accessRequests, onOpenAgent }: OfficeProps): JSX.Element {
  const pane = useRef<HTMLDivElement>(null);
  const canvasHost = useRef<HTMLDivElement>(null);
  const [scene, setScene] = useState<OfficeScene>();
  const [failure, setFailure] = useState<string>();
  const [xf, setXf] = useState<Transform>();
  const [hovered, setHovered] = useState<string>();
  const [places, setPlaces] = useState<Record<string, Whereabouts>>({});
  const root = useRef<HTMLDivElement>(null);
  const openedAt = useRef(Date.now());
  const seen = useRef(new Set<number>());

  // assignDesks is idempotent, so recomputing with the stored assignment
  // (e.g. in StrictMode) gives the same seats.
  const model = useMemo(() => {
    const m = buildOffice(keptAssignment, agents, tasks, accessRequests);
    keptAssignment = m.assignment;
    return m;
  }, [agents, tasks, accessRequests]);

  // Latest callback for the scene, which is created once.
  const open = useRef(onOpenAgent);
  open.current = onOpenAgent;

  useEffect(() => {
    const host = canvasHost.current;
    if (!host) return;
    let disposed = false;
    let created: OfficeScene | undefined;
    OfficeScene.create(host, {
      reducedMotion: reducedMotion(),
      onOpen: (id) => open.current(id),
      onHover: (id) => setHovered(id),
      onPlaces: (p) => setPlaces(p),
    }).then(
      (s) => {
        if (disposed) {
          s.destroy();
          return;
        }
        created = s;
        setScene(s);
      },
      (e: unknown) => {
        if (!disposed) setFailure(e instanceof Error ? e.message : String(e));
      },
    );
    return () => {
      disposed = true;
      created?.destroy();
    };
  }, []);

  // Layout effects: the scene and the nameplates change in the same paint.
  useLayoutEffect(() => {
    if (!scene) return;
    const t = scene.update(model);
    if (t) setXf(t);
  }, [scene, model]);

  useLayoutEffect(() => {
    const el = pane.current;
    if (!scene || !el) return;
    const fit = (): void => setXf(scene.resize(el.clientWidth, el.clientHeight));
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    return () => ro.disconnect();
  }, [scene]);

  // The scene's frame statistics, for diagnostics and the UI tests; written
  // straight to the DOM once a second, outside React.
  useEffect(() => {
    if (!scene) return;
    const t = window.setInterval(() => root.current?.setAttribute("data-stats", JSON.stringify(scene.stats())), 1000);
    return () => window.clearInterval(t);
  }, [scene]);

  useEffect(() => {
    if (!scene) return;
    for (const m of freshMessages(seen.current, messages, openedAt.current)) scene.sendEnvelope(m.from, m.to);
  }, [scene, messages]);

  const counts = poseOrder
    .map((p) => [p, model.seats.filter((s) => s.pose === p).length] as const)
    .filter(([, n]) => n > 0);

  return (
    <div className="office" ref={root} data-renderer={scene?.renderer ?? ""} data-scale={xf?.scale ?? ""}
      data-plan={xf ? (planOf(xf.grid).stacked ? "stacked" : "wide") : ""}>
      <div className="office-bar">
        {counts.length === 0
          ? <span className="muted">
              {model.exited > 0 ? "Nobody is at work. " : "No agents yet. "}
              Start one from the board or the sidebar; it gets a desk here.
            </span>
          : counts.map(([p, n]) => (
              <span key={p} className={`office-count pose-${p}`}><span className="dot" /> {n} {poseLabel[p].toLowerCase()}</span>
            ))}
        {model.exited > 0 && (
          <span className="muted office-left" title="Exited agents leave the office; the agent list and the switcher still have them">
            · {model.exited} left the office
          </span>
        )}
      </div>
      <div className="office-pane" ref={pane}>
        <div className="office-canvas" ref={canvasHost} />
        {failure && (
          <div className="office-failure">
            The office could not start a renderer (WebGL or canvas): {failure}. The agent list and the board still work.
          </div>
        )}
        {xf && model.seats.map((s) => {
          const o = slotOrigin(s.slot, xf.grid);
          const left = xf.x + (o.x + inCell.nameplate.x) * xf.scale;
          const top = xf.y + (o.y + inCell.nameplate.y) * xf.scale;
          const a = s.agent;
          const place = places[a.id] ?? "desk";
          const tip = [
            `${a.name} · ${a.role} · ${a.id}`, poseLabel[s.pose], whereLabel[place], a.activity ? `tool: ${a.activity}` : "", s.taskTitle ?? "",
            "Click to open its terminal",
          ].filter(Boolean).join("\n");
          return (
            <button key={a.id} type="button"
              className={`nameplate pose-${s.pose} ${hovered === a.id ? "hovered" : ""}`}
              style={{ left, top, maxWidth: Math.max(48, 60 * xf.scale) }}
              data-agent={a.id} data-pose={s.pose} data-state={a.state} data-place={place}
              title={tip}
              onMouseEnter={() => scene?.hover(a.id)} onMouseLeave={() => scene?.hover(undefined)}
              onFocus={() => scene?.hover(a.id)} onBlur={() => scene?.hover(undefined)}
              onClick={() => onOpenAgent(a.id)}>
              <span className="dot" />
              <span className="nameplate-text">
                <span className="nameplate-name">
                  {a.name || a.id.slice(0, 6)}
                  {a.kind !== "worker" && <span className="nameplate-role"> {a.role}</span>}
                </span>
                {s.taskTitle && <span className="nameplate-task">{s.taskTitle}</span>}
              </span>
            </button>
          );
        })}
        {xf && planOf(xf.grid).rooms.map((r) => (
          <div key={r.id} className={`room-label room-${r.id}`} data-room={r.id} title={r.purpose}
            style={{ left: xf.x + r.label.x * xf.scale, top: xf.y + r.label.y * xf.scale }}>
            {r.name}
          </div>
        ))}
        {xf && model.goal && (
          <div className="whiteboard-label" role="note"
            style={{
              left: xf.x + whiteboardAt(xf.grid).x * xf.scale,
              top: xf.y + whiteboardAt(xf.grid).y * xf.scale,
              width: whiteboardSize.w * xf.scale,
              height: (whiteboardSize.h - 4) * xf.scale,
            }}
            title={[`Goal: ${model.goal.title}`, ...model.goal.notes.map((n) => `• ${n.title} (${statusLabel[n.status]})`)].join("\n")}
            aria-label={`Lead's whiteboard: ${model.goal.notes.length} sub-tasks of ${model.goal.title}`}
            data-notes={model.goal.notes.length}
          />
        )}
      </div>
    </div>
  );
}
