import { useEffect, useState } from "react";
import { type Agent, type Task, parseEventMessage, wsURL } from "./api";

export type Connection = "connecting" | "open" | "closed";

export interface World {
  agents: Agent[];
  tasks: Task[];
  connection: Connection;
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
            break;
          case "agent":
            setAgents((prev) => upsert(prev, msg.agent));
            break;
          case "agent_removed":
            setAgents((prev) => prev.filter((a) => a.id !== msg.id));
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

  return { agents, tasks, connection };
}
