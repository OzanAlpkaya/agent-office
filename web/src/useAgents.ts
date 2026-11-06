import { useEffect, useState } from "react";
import { type Agent, parseEventMessage, wsURL } from "./api";

export type Connection = "connecting" | "open" | "closed";

// useAgents keeps the agent list in sync with /api/events: a snapshot on
// connect, then one message per change. It reconnects with backoff, and every
// reconnect starts with a fresh snapshot, so missed updates cannot accumulate.
export function useAgents(): { agents: Agent[]; connection: Connection } {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [connection, setConnection] = useState<Connection>("connecting");

  useEffect(() => {
    let ws: WebSocket | undefined;
    let retry = 0;
    let timer: number | undefined;
    let disposed = false;

    const connect = () => {
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
        if (msg.type === "snapshot") {
          setAgents(msg.agents);
        } else {
          setAgents((prev) => {
            const i = prev.findIndex((a) => a.id === msg.agent.id);
            if (i === -1) return [...prev, msg.agent];
            const next = prev.slice();
            next[i] = msg.agent;
            return next;
          });
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

  return { agents, connection };
}
