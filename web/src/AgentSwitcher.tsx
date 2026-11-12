// AgentSwitcher finds any agent by name, id, role, task, goal or branch and
// opens it (ADR 0044). Agents that are not exited are grouped by goal; exited
// agents come last, so they stay reachable. A dialog over the page; a
// full-screen sheet on a phone.
import { type JSX, type KeyboardEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Agent, AgentState, Task } from "./api";
import { searchAgents, shortId } from "./agents";

const stateText: Record<AgentState, string> = {
  starting: "starting", idle: "idle", working: "working", waiting_permission: "waiting for you",
  waiting_background: "waiting on background task", exited: "exited",
};

export function AgentSwitcher({ agents, tasks, current, waiting, onPick, onClose }: {
  agents: readonly Agent[];
  tasks: readonly Task[];
  current?: string;
  waiting: ReadonlySet<string>; // agents with an approval waiting for the user
  onPick: (id: string) => void;
  onClose: () => void;
}): JSX.Element {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const groups = useMemo(() => searchAgents(agents, tasks, query), [agents, tasks, query]);
  const flat = groups.flatMap((g) => g.agents);
  const title = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;

  // Focus before the first paint, so keys typed right after opening reach the search.
  useLayoutEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    input.current?.focus();
    return () => before?.focus({ preventScroll: true });
  }, []);
  useEffect(() => setActive(0), [query]);
  useEffect(() => {
    list.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => Math.min(flat.length - 1, i + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(0, i - 1));
    } else if (e.key === "Enter" && flat[active]) {
      e.preventDefault();
      onPick(flat[active].id);
    }
  };

  let index = -1;
  return (
    <div className="switcher-layer" onKeyDown={onKey}>
      <div className="drawer-backdrop" onClick={onClose} />
      <div className="switcher" role="dialog" aria-modal="true" aria-label="Find an agent">
        <div className="switcher-head">
          <input ref={input} type="search" value={query} onChange={(e) => setQuery(e.target.value)}
            placeholder="Name, id, role, task or branch" aria-label="Search agents" aria-controls="switcher-list"
            aria-activedescendant={flat[active] ? `switch-${flat[active].id}` : undefined}
            autoComplete="off" spellCheck={false} />
          <button className="link" onClick={onClose} aria-label="Close">✕</button>
        </div>
        <ul className="switcher-list" id="switcher-list" role="listbox" ref={list} aria-label="Agents">
          {groups.map((g) => (
            <li key={g.key} role="presentation">
              <div className="switcher-group" role="presentation">
                {g.key === "exited" || g.key === "none" ? g.title : <>Goal: {g.title}</>}
                <span className="muted"> · {g.agents.length}</span>
              </div>
              <ul role="presentation">
                {g.agents.map((a) => {
                  index++;
                  const i = index;
                  const task = title(a.task_id);
                  return (
                    <li key={a.id} id={`switch-${a.id}`} role="option" aria-selected={i === active} data-index={i}
                      data-agent={a.id}
                      className={`switcher-item ${i === active ? "active" : ""} ${a.id === current ? "current" : ""}`}
                      onMouseMove={() => setActive(i)} onClick={() => onPick(a.id)}>
                      <span className={`dot state-${a.state}`} />
                      <span className="switcher-name">{a.name || shortId(a.id)}</span>
                      <span className="mono muted small">{shortId(a.id)}</span>
                      <span className="switcher-role">
                        {a.kind !== "worker" && <span className={`chip role-${a.kind}`}>{a.role}</span>}
                      </span>
                      <span className="switcher-task small muted">{task ?? ""}</span>
                      <span className={`small state-${a.state}`}>
                        {waiting.has(a.id) ? "approval waiting" : stateText[a.state]}
                      </span>
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
          {flat.length === 0 && <li className="muted switcher-empty">{agents.length ? "No agent matches." : "No agents yet."}</li>}
        </ul>
        <div className="switcher-foot small muted">↑ ↓ to move · Enter to open · Esc to close · <kbd>/</kbd> opens this</div>
      </div>
    </div>
  );
}
