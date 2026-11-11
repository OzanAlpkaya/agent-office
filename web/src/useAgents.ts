import { useEffect, useState } from "react";
import {
  type AccessRequest, type Agent, type InboxItem, type MemoryEntry, type Message, type PullRequest, type QARun,
  type GoalMerge, type ProjectApi, type SandboxEvent, type SessionUsage, type Spec, type Task, api as daemonApi, parseEventMessage,
  signalUnauthorized, statusSignedOut, wsURL,
} from "./api";
import { totalTokens } from "./usage";

export type Connection = "connecting" | "open" | "closed";

export interface World {
  agents: Agent[];
  tasks: Task[];
  events: Record<string, SandboxEvent[]>; // sandbox events per agent id, newest first
  merges: GoalMerge[]; // goal merges and pushes (ADR 0055), newest first
  messages: Record<string, Message[]>; // messages sent or received per agent id, newest first
  accessRequests: AccessRequest[]; // pending first, then the most recent
  pullRequests: PullRequest[]; // in creation order (ADR 0020)
  usage: Record<string, SessionUsage>; // token usage per session id (ADR 0026)
  inbox: InboxItem[]; // messages to the user, newest first (ADR 0030)
  specs: Spec[]; // every version of every goal's plan (ADR 0031)
  qaRuns: QARun[]; // newest first (ADR 0033)
  memory: MemoryEntry[]; // newest first (ADR 0034)
  connection: Connection;
}

// byNewest upserts an item with a numeric id into a list kept newest first.
function byNewest<T extends { id: number }>(list: T[], item: T): T[] {
  return [item, ...list.filter((x) => x.id !== item.id)].sort((a, b) => b.id - a.id);
}

// newerUsage keeps whichever copy of a session's usage has seen more: live
// updates and the fetched report can arrive in any order, and every counter
// only grows.
export function newerUsage(a: SessionUsage | undefined, b: SessionUsage): SessionUsage {
  if (!a) return b;
  const key = (s: SessionUsage): number[] =>
    [s.messages, totalTokens(s.tokens), s.turns, s.tool_calls, s.ended_at ? 1 : 0];
  const ka = key(a), kb = key(b);
  for (let i = 0; i < ka.length; i++) {
    if (ka[i] !== kb[i]) return kb[i] > ka[i] ? b : a;
  }
  return b;
}

// Messages kept per agent in the UI; the API returns at most this many too.
const maxMessages = 100;

// mergeMessages combines fetched and live messages by id (a later copy, e.g.
// with delivered_at set, wins), newest first.
function mergeMessages(a: Message[], b: Message[]): Message[] {
  const byId = new Map<number, Message>();
  for (const m of [...a, ...b]) byId.set(m.id, m);
  return [...byId.values()].sort((x, y) => y.id - x.id).slice(0, maxMessages);
}

function sortAccess(list: AccessRequest[]): AccessRequest[] {
  return list.slice().sort((x, y) =>
    Number(y.status === "pending") - Number(x.status === "pending") || y.id - x.id);
}

// Events kept per agent in the UI; the API returns at most this many too.
const maxEvents = 50;

const eventKey = (e: SandboxEvent): string => `${e.at}|${e.kind}|${e.detail}`;

// mergeById combines merge records, the later list winning per id, newest first.
export function mergeById(a: readonly GoalMerge[], b: readonly GoalMerge[]): GoalMerge[] {
  const byId = new Map<number, GoalMerge>();
  for (const m of [...a, ...b]) byId.set(m.id, m);
  return [...byId.values()].sort((x, y) => y.id - x.id);
}

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

// useWorld keeps one project's agents and tasks in sync with its events
// stream (ADR 0046): a snapshot on
// connect, then one message per change. It reconnects with backoff, and every
// reconnect starts with a fresh snapshot, so missed updates cannot accumulate.
export function useWorld(api: ProjectApi): World {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [events, setEvents] = useState<Record<string, SandboxEvent[]>>({});
  const [messages, setMessages] = useState<Record<string, Message[]>>({});
  const [accessRequests, setAccessRequests] = useState<AccessRequest[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [usage, setUsage] = useState<Record<string, SessionUsage>>({});
  const [inbox, setInbox] = useState<InboxItem[]>([]);
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [qaRuns, setQARuns] = useState<QARun[]>([]);
  const [memory, setMemory] = useState<MemoryEntry[]>([]);
  const [merges, setMerges] = useState<GoalMerge[]>([]);
  const [connection, setConnection] = useState<Connection>("connecting");

  useEffect(() => {
    let ws: WebSocket | undefined;
    let retry = 0;
    let timer: number | undefined;
    let disposed = false;

    const connect = (): void => {
      setConnection("connecting");
      ws = new WebSocket(wsURL(api.eventsPath));
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
            setMessages({});
            setAccessRequests(sortAccess(msg.accessRequests));
            setPullRequests(msg.pullRequests);
            setInbox(msg.inbox.slice().sort((a, b) => b.id - a.id));
            setSpecs(msg.specs);
            setQARuns(msg.qaRuns.slice().sort((a, b) => b.id - a.id));
            setMemory(msg.memory.slice().sort((a, b) => b.id - a.id));
            // Goal merges (ADR 0055); live ones arriving meanwhile are kept.
            void api.goalMerges().then(
              (ms) => setMerges((prev) => mergeById(ms, prev)),
              () => console.warn("agent-office: could not load goal merges"),
            );
            // Usage history; live updates arriving meanwhile are merged, not overwritten.
            void api.usage().then(
              (rep) => setUsage((prev) => {
                // Start from prev: a live update that arrived during the fetch is kept.
                const next: Record<string, SessionUsage> = { ...prev };
                for (const s of rep.sessions) next[s.session_id] = newerUsage(prev[s.session_id], s);
                return next;
              }),
              () => console.warn("agent-office: could not load usage"),
            );
            for (const a of msg.agents) {
              // Fire-and-forget like the events below; a failure only loses history.
              void api.agentMessages(a.id).then(
                (ms) => setMessages((prev) => ({ ...prev, [a.id]: mergeMessages(prev[a.id] ?? [], ms) })),
                () => console.warn(`agent-office: could not load messages for ${a.id}`),
              );
            }
            // Recorded events, so a reload still shows earlier refusals. Live
            // events that arrive meanwhile are merged, not overwritten.
            for (const a of msg.agents.filter((x) => x.sandbox === "podman")) {
              // Fire-and-forget: both outcomes are handled; a failure only loses history.
              void api.agentEvents(a.id).then(
                (evs) => setEvents((prev) => ({ ...prev, [a.id]: mergeEvents(prev[a.id] ?? [], evs) })),
                () => console.warn(`agent-office: could not load sandbox events for ${a.id}`),
              );
            }
            break;
          case "agent":
            setAgents((prev) => upsert(prev, msg.agent));
            break;
          case "goal_merge":
            setMerges((prev) => mergeById(prev, [msg.merge]));
            break;
          case "agent_removed":
            setAgents((prev) => prev.filter((a) => a.id !== msg.id));
            setEvents((prev) => {
              const { [msg.id]: _removed, ...rest } = prev;
              return rest;
            });
            setMessages((prev) => {
              const { [msg.id]: _removed, ...rest } = prev;
              return rest;
            });
            break;
          case "message": {
            const m = msg.message;
            setMessages((prev) => {
              const next = { ...prev, [m.to]: mergeMessages(prev[m.to] ?? [], [m]) };
              if (m.from) next[m.from] = mergeMessages(prev[m.from] ?? [], [m]);
              return next;
            });
            break;
          }
          case "access_request": {
            const r = msg.request;
            setAccessRequests((prev) => sortAccess([...prev.filter((x) => x.id !== r.id), r]));
            break;
          }
          case "pull_request": {
            const pr = msg.pullRequest;
            setPullRequests((prev) => {
              const i = prev.findIndex((x) => x.id === pr.id);
              if (i === -1) return [...prev, pr];
              const next = prev.slice();
              next[i] = pr;
              return next;
            });
            break;
          }
          case "sandbox_event":
            setEvents((prev) => ({ ...prev, [msg.agent_id]: mergeEvents(prev[msg.agent_id] ?? [], [msg.event]) }));
            break;
          case "usage": {
            const u = msg.usage;
            setUsage((prev) => ({ ...prev, [u.session_id]: newerUsage(prev[u.session_id], u) }));
            break;
          }
          case "task":
            setTasks((prev) => upsert(prev, msg.task));
            break;
          case "task_deleted":
            setTasks((prev) => prev.filter((t) => t.id !== msg.id));
            break;
          case "inbox":
            setInbox((prev) => byNewest(prev, msg.item));
            break;
          case "spec":
            setSpecs((prev) => byNewest(prev, msg.spec));
            break;
          case "qa_run":
            setQARuns((prev) => byNewest(prev, msg.run));
            break;
          case "memory":
            setMemory((prev) => byNewest(prev, msg.entry));
            break;
          case "memory_deleted":
            setMemory((prev) => prev.filter((m) => String(m.id) !== msg.id));
            break;
        }
      };
      ws.onclose = (e) => {
        setConnection("closed");
        if (disposed) return;
        // Signed out (4401), or a refused handshake (1006): ask whether this
        // browser is still paired; the app shows its pairing screen if not.
        if (e.code === statusSignedOut) signalUnauthorized();
        else if (e.code === 1006) {
          void daemonApi.authSession().then((s) => { if (!s.authenticated) signalUnauthorized(); }, () => undefined);
        }
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
  }, [api]);

  return { agents, tasks, events, messages, accessRequests, pullRequests, usage, inbox, specs, qaRuns, memory, merges, connection };
}
