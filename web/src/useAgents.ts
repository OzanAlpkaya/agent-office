import { useEffect, useState } from "react";
import { type Agent, type SandboxEvent, type Task, api, parseEventMessage, wsURL } from "./api";

export type Connection = "connecting" | "open" | "closed";

export interface World {
  agents: Agent[];
  tasks: Task[];
  events: Record<string, SandboxEvent[]>; // sandbox events per agent id, newest first
  connection: Connection;
}

// Events kept per agent in the UI; the API returns at most this many too.
const maxEvents = 50;

const eventKey = (e: SandboxEvent): string => `${e.at}|${e.kind}|${e.detail}`;

// mergeEvents combines fetched and live events without duplicates, newest first.
function mergeEvents(a: SandboxEvent[], b: SandboxEvent[]): SandboxEvent[] {
  const seen = new Set<string>();
  return [...a, ...b]
    .filter((e) => !seen.has(eventKey(e)) && Boolean(seen.add(eventKey(e))))
    .sort((x, y) => Date.parse(y.at) - Date.parse(x.at))
    .slice(0, maxEvents);
}

function upsert<T extends { id: string }>(list: T[], item: T): T[] {
  const i = list.findIndex((x) => x.id === item.id);
  if (i === -1) return [...list, item];
  const next = list.slice();
  next[i] = item;
  return next;
}

// useWorld keeps agents and tasks in sync with /api/events: a snapshot on
// connect, then one message per change. It reconnects with backoff, and every
// reconnect starts with a fresh snapshot, so missed updates cannot accumulate.
export function useWorld(): World {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [events, setEvents] = useState<Record<string, SandboxEvent[]>>({});
  const [connection, setConnection] = useState<Connection>("connecting");

  useEffect(() => {
    let ws: WebSocket | undefined;
    let retry = 0;
    let timer: number | undefined;
    let disposed = false;

    const connect = (): void => {
      setConnection("connecting");
      ws = new WebSocket(wsURL("/api/events"));
      ws.onopen = () => {
        retry = 0;
        setConnection("open");
      };
      ws.onmessage = (e) => {
        const msg = parseEventMessage(String(e.data));
        if (!msg) {
          console.warn("agent-office: ignoring malformed event message");
          return;
        }
        switch (msg.type) {
          case "snapshot":
            setAgents(msg.agents);
            setTasks(msg.tasks);
            setEvents({});
            // Recorded events, so a reload still shows earlier refusals. Live
            // events that arrive meanwhile are merged, not overwritten.
            for (const a of msg.agents.filter((x) => x.sandbox === "podman")) {
              api.agentEvents(a.id).then(
                (evs) => setEvents((prev) => ({ ...prev, [a.id]: mergeEvents(prev[a.id] ?? [], evs) })),
                () => console.warn(`agent-office: could not load sandbox events for ${a.id}`),
              );
            }
            break;
          case "agent":
            setAgents((prev) => upsert(prev, msg.agent));
            break;
          case "agent_removed":
            setAgents((prev) => prev.filter((a) => a.id !== msg.id));
            setEvents((prev) => {
              const { [msg.id]: _removed, ...rest } = prev;
              return rest;
            });
            break;
          case "sandbox_event":
            setEvents((prev) => ({ ...prev, [msg.agent_id]: mergeEvents(prev[msg.agent_id] ?? [], [msg.event]) }));
            break;
          case "task":
            setTasks((prev) => upsert(prev, msg.task));
            break;
          case "task_deleted":
            setTasks((prev) => prev.filter((t) => t.id !== msg.id));
            break;
        }
      };
      ws.onclose = () => {
        setConnection("closed");
        if (disposed) return;
        const delay = Math.min(5000, 500 * 2 ** retry++);
        timer = window.setTimeout(connect, delay);
      };
    };
    connect();
    return () => {
      disposed = true;
      window.clearTimeout(timer);
      ws?.close();
    };
  }, []);

  return { agents, tasks, events, connection };
}
