// The Office tab (ADR 0024): a lazy-loaded Pixi scene of the team, with HTML
// nameplates over it. It reads the same world as the rest of the UI and has
// one action: open an agent.
import { type JSX, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Agent, Message, OfficeInfo, Task, TaskStatus } from "../api";
import { type Presence, present, presenceText } from "../presence";
import { narrowQuery, useMedia } from "../useMedia";
import { type Profile, displayName, lookOfProfile } from "./avatar";
import type { Waiting } from "../waiting";
import { type Assignment, emptyAssignment, inCell, planOf, slotOrigin, whiteboardAt } from "./layout";
import { buildOffice, freshMessages } from "./model";
import { type Pose, cssColor, poseLabel, shirtOf } from "./pose";
import { whiteboardSize } from "./art";
import { OfficeScene, type Transform, type Whereabouts } from "./scene";

export interface OfficeProps {
  agents: Agent[];
  tasks: Task[];
  messages: Record<string, Message[]>;
  waiting: Waiting[]; // waitingOnUser: what the Approvals badge counts (ADR 0044)
  onOpenAgent: (id: string) => void;
  // One floor per project (ADR 0048): the shown one is current; choosing
  // another switches the project. Omitted or one floor: no elevator panel.
  floors?: Floor[];
  onFloor?: (id: string) => void;
  // Phase 18: the project's office layout (absent: the default plan), the
  // user's presence, and where to change the layout.
  layout?: OfficeInfo;
  presence?: Presence;
  onLayoutSettings?: () => void;
  profile?: Profile; // the user's name and look (Phase 18 review)
}

// Floor is one project's floor in the elevator panel.
export interface Floor {
  id: string;
  name: string;
  waiting: number; // agents waiting on the user there (ADR 0044)
  running: number;
  current: boolean;
}

const poseOrder: Pose[] = ["typing", "reviewing", "waiting", "idle", "booting"];

// Where a character is, in the nameplate's tooltip (ADR 0045).
const whereLabel: Record<Whereabouts, string> = {
  desk: "", walking: "Walking", whiteboard: "Reporting at the whiteboard", coffee: "At the coffee machine", review: "In the review room",
  queue: "Waiting at your door",
};

const statusLabel: Record<TaskStatus, string> = {
  todo: "to do", in_progress: "in progress", review: "in review", done: "done", blocked: "blocked",
};

// Who sits where, kept per floor for the page's lifetime rather than the
// component's, so leaving the tab (or the floor) and coming back does not
// move anyone. A reload starts over (in start order).
const keptAssignments = new Map<string, Assignment>();

function reducedMotion(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export default function Office({
  agents, tasks, messages, waiting, onOpenAgent, floors = [], onFloor, layout, presence, onLayoutSettings, profile,
}: OfficeProps): JSX.Element {
  // On a phone a nameplate shows the name only; a tap shows the task, a second tap opens the agent.
  const narrow = useMedia(narrowQuery);
  const [shown, setShown] = useState<string>();
  const userLook = useMemo(() => lookOfProfile(profile), [profile]);
  const userName = displayName(profile);
  // The project's own layout when it validated; else the default plan (ADR 0057).
  const custom = layout?.source === "custom" ? layout.layout : undefined;
  const problems = layout?.problems ?? [];
  const userHere = present(presence);
  const floorId = floors.find((f) => f.current)?.id ?? "";
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
    const m = buildOffice(keptAssignments.get(floorId) ?? emptyAssignment, agents, tasks, waiting, { layout: custom });
    keptAssignments.set(floorId, m.assignment);
    return m;
  }, [agents, tasks, waiting, floorId, custom]);

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
      onTransform: (t) => setXf(t),
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

  // The user at their desk while a device has the UI open (Phase 18).
  useEffect(() => {
    scene?.setPresence(userHere, userLook);
  }, [scene, userHere, userLook]);

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
      data-plan={xf ? (planOf(xf.grid).custom ? "custom" : planOf(xf.grid).stacked ? "stacked" : "wide") : ""}
      data-user-present={userHere ? "true" : "false"}>
      {floors.length > 1 && (
        <nav className="elevator" aria-label="Floors">
          {floors.map((f, i) => (
            <button key={f.id} className={`floor ${f.current ? "current" : ""} ${f.waiting > 0 ? "has-waiting" : ""}`}
              aria-current={f.current ? "true" : undefined} data-floor={f.id}
              title={`${f.name}: ${f.running} running${f.waiting > 0 ? `, ${f.waiting} waiting on you` : ""}`}
              onClick={() => { if (!f.current) onFloor?.(f.id); }}>
              <span className="floor-number">{i + 1}</span> {f.name}
              {f.waiting > 0 && <span className="count">{f.waiting}</span>}
            </button>
          ))}
        </nav>
      )}
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
        {model.deskless.length > 0 && (
          <span className="office-deskless hint" data-testid="deskless"
            title="This office layout has fewer desks than agents at work; add desks to its workroom in the project settings">
            · {model.deskless.length} without a desk:
            {model.deskless.map((a) => (
              <button key={a.id} className="link sm" data-agent={a.id} onClick={() => onOpenAgent(a.id)}>{a.name || a.id.slice(0, 6)}</button>
            ))}
          </span>
        )}
      </div>
      {problems.length > 0 && (
        <div className="callout warn office-warning" role="alert" data-testid="layout-warning">
          The project's office layout is not valid, so the default office is shown.{" "}
          {onLayoutSettings && <button className="link" onClick={onLayoutSettings}>Fix it in the project settings</button>}
          <ul>{problems.map((p) => <li key={p}>{p}</li>)}</ul>
        </div>
      )}
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
          const expanded = !narrow || shown === a.id;
          return (
            <button key={a.id} type="button" aria-expanded={narrow ? expanded : undefined}
              className={`nameplate pose-${s.pose} ${hovered === a.id ? "hovered" : ""} ${expanded ? "" : "name-only"}`}
              style={{ left, top, maxWidth: Math.max(48, 60 * xf.scale) }}
              data-agent={a.id} data-pose={s.pose} data-state={a.state} data-place={place} data-shirt={cssColor(shirtOf(a).base)}
              title={tip}
              onMouseEnter={() => scene?.hover(a.id)} onMouseLeave={() => scene?.hover(undefined)}
              onFocus={() => scene?.hover(a.id)} onBlur={() => scene?.hover(undefined)}
              onClick={() => (expanded ? onOpenAgent(a.id) : setShown(a.id))}>
              <span className="dot" />
              <span className="nameplate-text">
                <span className="nameplate-name">
                  {a.name || a.id.slice(0, 6)}
                  {a.role !== "worker" && (
                    <span className="nameplate-role" style={{ color: cssColor(shirtOf(a).base) }}> {a.role}</span>
                  )}
                </span>
                {s.taskTitle && expanded && <span className="nameplate-task">{s.taskTitle}</span>}
                {narrow && expanded && <span className="nameplate-open">Tap to open</span>}
              </span>
            </button>
          );
        })}
        {xf && userHere && (() => {
          const u = planOf(xf.grid).userDesk;
          return (
            <div className="user-plate" data-testid="user-plate" title={presenceText(presence, userName)}
              style={{ left: xf.x + (u.x + inCell.nameplate.x) * xf.scale, top: xf.y + (u.y + inCell.nameplate.y) * xf.scale }}>
              <span className="dot" style={{ color: "var(--accent)" }} />
              <span className="nameplate-text"><span className="nameplate-name">{userName}</span></span>
            </div>
          );
        })()}
        {xf && planOf(xf.grid).rooms.map((r) => (
          <div key={r.id} className={`room-label room-${r.id}`} data-room={r.id} title={r.purpose}
            style={{ left: xf.x + r.label.x * xf.scale, top: xf.y + r.label.y * xf.scale }}>
            {r.name}
          </div>
        ))}
        {xf && (() => {
          // The sign at the user's door counts the same list as the Approvals badge.
          const sign = planOf(xf.grid).queueSign;
          return (
            <div className={`queue-sign ${model.waiting > 0 ? "has-waiting" : ""}`} data-waiting={model.waiting}
              title="Agents waiting on you queue at this door: the same count as Approvals"
              style={{ left: xf.x + sign.x * xf.scale, top: xf.y + sign.y * xf.scale }}>
              {model.waiting > 0 ? `${model.waiting} waiting` : "Nobody waiting"}
            </div>
          );
        })()}
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
