// The office as data: who sits where, in which pose, and what the lead's
// whiteboard shows. The scene draws this; the HTML nameplates label it. Pure.
import type { AccessRequest, Agent, Message, Task, TaskStatus } from "../api";
import { noteGrid } from "./art";
import { type Assignment, type Slot, assignDesks } from "./layout";
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
  seats: Seat[]; // one per agent
  goal?: { title: string; notes: Note[] }; // the lead's goal and its sub-tasks
}

// The whiteboard has room for this many notes, as many as the sub-tasks a
// lead may create (20, ADR 0017).
export const maxNotes = noteGrid.cols * noteGrid.rows;

export function buildOffice(
  prev: Assignment,
  agents: readonly Agent[],
  tasks: readonly Task[],
  accessRequests: readonly AccessRequest[],
): OfficeModel {
  const assignment = assignDesks(prev, agents);
  const waiting = new Set(accessRequests.filter((r) => r.status === "pending").map((r) => r.agent_id));
  const title = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const byId = new Map(agents.map((a) => [a.id, a]));

  const seat = (slot: Slot, a: Agent): Seat => ({
    slot, agent: a, pose: poseOf(a, waiting.has(a.id)), look: lookOf(a.id), taskTitle: title(a.task_id),
  });
  const seats: Seat[] = [];
  const lead = assignment.lead !== undefined ? byId.get(assignment.lead) : undefined;
  if (lead) seats.push(seat({ kind: "lead" }, lead));
  assignment.desks.forEach((id, index) => {
    const a = id !== undefined ? byId.get(id) : undefined;
    if (a) seats.push(seat({ kind: "desk", index }, a));
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
  return { assignment, desks: assignment.desks.length, seats, goal };
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
