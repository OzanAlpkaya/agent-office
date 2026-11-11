// The office as data: who sits where, in which pose, and what the lead's
// whiteboard shows. The scene draws this; the HTML nameplates label it. Pure.
import type { Agent, Message, OfficeLayout, Task, TaskStatus } from "../api";
import type { Waiting } from "../waiting";
import { noteGrid } from "./art";
import { stationKinds } from "./floor";
import { customPlan } from "./custom";
import { type Assignment, type Slot, assignDesks, ownPlaces, placesOf } from "./layout";
import { type Look, type Pose, lookOf, poseOf } from "./pose";

export interface Seat {
  slot: Slot;
  agent: Agent;
  pose: Pose;
  look: Look;
  taskTitle?: string;
}

export interface Note {
  id: string;
  title: string;
  status: TaskStatus;
}

export interface OfficeModel {
  assignment: Assignment;
  desks: number; // regular desks in use, including free ones between seated agents
  seats: Seat[]; // one per agent that has not exited
  exited: number; // agents that left the office; they stay in the agent list
  goal?: { title: string; notes: Note[] }; // the lead's goal and its sub-tasks
  gone: string[]; // agents that exited, still listed by the daemon
  waiting: number; // agents waiting on the user: the Approvals count, and the queue at the user's door
  waitingSince: Record<string, number>; // ms: how long each has waited, the queue's order (as in the drawer)
  taskStatus: Record<string, TaskStatus | undefined>; // every agent's task status, for errands (ADR 0045)
  deskless: Agent[]; // with a custom layout, agents beyond its desks: not drawn, listed in the office bar (ADR 0057)
  layout?: OfficeLayout; // the project's own layout; absent: the default plan
}

// The whiteboard has room for this many notes, as many as the sub-tasks a
// lead may create (20, ADR 0017).
export const maxNotes = noteGrid.cols * noteGrid.rows;

// buildOffice takes waiting from waitingOnUser, the list the Approvals badge
// counts, so "waiting for you" and the queue at the user's door match it (ADR 0044).
export function buildOffice(
  prev: Assignment,
  agents: readonly Agent[],
  tasks: readonly Task[],
  waitingList: readonly Waiting[],
  opts: { layout?: OfficeLayout } = {},
): OfficeModel {
  // An exited agent leaves: its desk is free for the next agent (ADR 0024).
  const present = agents.filter((a) => a.state !== "exited");
  // A custom layout has its own rooms and a fixed number of desks (ADR 0057).
  const plan = opts.layout ? customPlan(opts.layout) : undefined;
  const capacity = plan?.capacity;
  const assignment = assignDesks(prev, present, plan ? placesOf(plan) : ownPlaces, capacity);
  const listed = waitingList.filter((w) => w.agent.state !== "exited");
  const waiting = new Set(listed.map((w) => w.agent.id));
  const waitingSince: Record<string, number> = {};
  for (const w of listed) waitingSince[w.agent.id] = w.since;
  const title = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const byId = new Map(present.map((a) => [a.id, a]));

  const seat = (slot: Slot, a: Agent): Seat => ({
    slot, agent: a, pose: poseOf(a, waiting.has(a.id)), look: lookOf(a.id), taskTitle: title(a.task_id),
  });
  const seats: Seat[] = [];
  const lead = assignment.lead !== undefined ? byId.get(assignment.lead) : undefined;
  if (lead) seats.push(seat({ kind: "lead" }, lead));
  for (const k of stationKinds) {
    const a = assignment[k] !== undefined ? byId.get(assignment[k] as string) : undefined;
    if (a) seats.push(seat({ kind: "station", station: k }, a));
  }
  const deskless: Agent[] = [];
  assignment.desks.forEach((id, index) => {
    const a = id !== undefined ? byId.get(id) : undefined;
    if (!a) return;
    if (capacity !== undefined && index >= capacity) deskless.push(a);
    else seats.push(seat({ kind: "desk", index }, a));
  });

  let goal: OfficeModel["goal"];
  const goalTask = lead?.task_id !== undefined ? tasks.find((t) => t.id === lead.task_id) : undefined;
  if (goalTask) {
    const notes = tasks
      .filter((t) => t.parent_task_id === goalTask.id)
      .sort((x, y) => Date.parse(x.created_at) - Date.parse(y.created_at) || (x.id < y.id ? -1 : 1))
      .slice(0, maxNotes)
      .map((t) => ({ id: t.id, title: t.title, status: t.status }));
    goal = { title: goalTask.title, notes };
  }
  const taskStatus: Record<string, TaskStatus | undefined> = {};
  for (const a of agents) taskStatus[a.id] = tasks.find((t) => t.id === a.task_id)?.status;
  const gone = agents.filter((a) => a.state === "exited").map((a) => a.id);
  const desks = capacity !== undefined ? Math.min(assignment.desks.length, capacity) : assignment.desks.length;
  return { assignment, desks, seats, goal, exited: gone.length, gone, taskStatus, waiting: waiting.size, waitingSince, deskless, layout: opts.layout };
}

// freshMessages returns the messages not seen before that were created at or
// after sinceMs, oldest first, and adds every given message to seen. The
// office animates only these, so loading history does not replay it.
export function freshMessages(seen: Set<number>, lists: Readonly<Record<string, readonly Message[]>>, sinceMs: number): Message[] {
  const fresh = new Map<number, Message>();
  for (const list of Object.values(lists)) {
    for (const m of list) {
      if (seen.has(m.id)) continue;
      seen.add(m.id);
      if (Date.parse(m.created_at) >= sinceMs) fresh.set(m.id, m);
    }
  }
  return [...fresh.values()].sort((x, y) => x.id - y.id);
}
