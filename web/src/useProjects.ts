import { useEffect, useState } from "react";
import { type Project, api, parseEventMessage, parseProjectsMessage, projectApi, signalUnauthorized, statusSignedOut, wsURL } from "./api";
import type { Connection } from "./useAgents";
import { type LightWorld, applyProjects, emptyLight, reduceLight } from "./projects";
import { type Presence, visibilityMessage } from "./presence";

const backoff = (retry: number): number => Math.min(5000, 500 * 2 ** retry);

// useProjects keeps the project list in sync with the daemon's /api/events
// (ADR 0046). loaded turns true with the first snapshot. The page tells the
// daemon on the same socket whether it is visible, and gets the user's
// presence back (Phase 18): the devices with a visible page.
export function useProjects(): { projects: Project[]; loaded: boolean; connection: Connection; presence?: Presence } {
  const [projects, setProjects] = useState<Project[]>([]);
  const [presence, setPresence] = useState<Presence>();
  const [loaded, setLoaded] = useState(false);
  const [connection, setConnection] = useState<Connection>("connecting");
  useEffect(() => {
    let ws: WebSocket | undefined;
    let retry = 0;
    let timer: number | undefined;
    let disposed = false;
    const connect = (): void => {
      setConnection("connecting");
      const sock = new WebSocket(wsURL("/api/events"));
      ws = sock;
      sock.onopen = () => {
        retry = 0;
        setConnection("open");
        sock.send(visibilityMessage(document.visibilityState === "visible"));
      };
      sock.onmessage = (e) => {
        const m = parseProjectsMessage(String(e.data));
        if (!m) {
          console.warn("agent-office: ignoring malformed project message");
          return;
        }
        if (m.type === "presence") {
          setPresence(m.presence);
          return;
        }
        setProjects((prev) => applyProjects(prev, m));
        if (m.type === "projects") setLoaded(true);
      };
      sock.onclose = (e) => {
        setConnection("closed");
        setPresence(undefined);
        if (disposed) return;
        if (e.code === statusSignedOut) signalUnauthorized();
        else if (e.code === 1006) {
          void api.authSession().then((s) => { if (!s.authenticated) signalUnauthorized(); }, () => undefined);
        }
        timer = window.setTimeout(connect, backoff(retry++));
      };
    };
    const onVisibility = (): void => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(visibilityMessage(document.visibilityState === "visible"));
    };
    document.addEventListener("visibilitychange", onVisibility);
    connect();
    return () => {
      disposed = true;
      document.removeEventListener("visibilitychange", onVisibility);
      window.clearTimeout(timer);
      ws?.close();
    };
  }, []);
  return { projects, loaded, connection, presence };
}

// useWatchers follows every open project but the shown one, lightly: only
// what decides "waiting on the user" (ADR 0048). One events stream each.
export function useWatchers(projects: readonly Project[], shown: string | undefined): Record<string, LightWorld> {
  const [worlds, setWorlds] = useState<Record<string, LightWorld>>({});
  const ids = projects.filter((p) => p.id !== shown && p.state === "open").map((p) => p.id).sort().join(",");
  useEffect(() => {
    const list = ids === "" ? [] : ids.split(",");
    setWorlds((prev) => Object.fromEntries(list.map((id) => [id, prev[id] ?? emptyLight])));
    const stops = list.map((id) => {
      let ws: WebSocket | undefined;
      let retry = 0;
      let timer: number | undefined;
      let disposed = false;
      const connect = (): void => {
        ws = new WebSocket(wsURL(projectApi(id).eventsPath));
        ws.onopen = () => { retry = 0; };
        ws.onmessage = (e) => {
          const m = parseEventMessage(String(e.data));
          if (m) setWorlds((prev) => ({ ...prev, [id]: reduceLight(prev[id] ?? emptyLight, m) }));
        };
        ws.onclose = (e) => {
          if (disposed) return;
          if (e.code === statusSignedOut) {
            signalUnauthorized(); // signed out: the Gate takes over; no retries
            return;
          }
          timer = window.setTimeout(connect, backoff(retry++));
        };
      };
      connect();
      return () => {
        disposed = true;
        window.clearTimeout(timer);
        ws?.close();
      };
    });
    return () => stops.forEach((s) => s());
  }, [ids]);
  return worlds;
}
