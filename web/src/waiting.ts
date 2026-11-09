// What waits on the user (ADR 0044): the one source for the Approvals badge,
// its drawer, the tab title, and the Office's "waiting for you" count and the
// queue at the user's door (ADR 0045). Pure.
import type { AccessRequest, Agent, InboxItem, Spec } from "./api";
import { needsAnswer } from "./workflow";

// Waiting is one agent that cannot go on until the user acts: it has access
// requests to allow or deny (ADR 0019), a permission prompt open in its
// terminal (host mode), a question to the user without an answer (ADR 0030),
// a plan waiting for approval (an architect, ADR 0031), or several of these.
// Counts are of agents, not of the things they wait for.
export interface Waiting {
  agent: Agent;
  requests: AccessRequest[]; // pending, oldest first
  permission: boolean; // a prompt in its terminal; answered there, not in the UI
  questions: InboxItem[]; // asked by this agent, unanswered, oldest first
  plan?: Spec; // its proposed plan, waiting for approval
}

const at = (iso: string): number => Date.parse(iso);

// waitingOnUser lists the agents waiting on the user, longest waiting first.
// Exited agents wait for nobody: the daemon expires their requests when they
// exit, and a pending request of an agent not in the list is the same case
// seen before its update arrived. A question or plan of an exited agent waits
// in the inbox and on the board, not here.
export function waitingOnUser(
  agents: readonly Agent[],
  accessRequests: readonly AccessRequest[],
  inbox: readonly InboxItem[] = [],
  specs: readonly Spec[] = [],
): Waiting[] {
  const pending = new Map<string, AccessRequest[]>();
  for (const r of accessRequests) {
    if (r.status !== "pending") continue;
    pending.set(r.agent_id, [...(pending.get(r.agent_id) ?? []), r]);
  }
  const asked = new Map<string, InboxItem[]>();
  for (const i of inbox) {
    if (!needsAnswer(i) || i.agent_id === undefined) continue;
    asked.set(i.agent_id, [...(asked.get(i.agent_id) ?? []), i]);
  }
  const plans = new Map<string, Spec>();
  for (const s of specs) {
    if (s.status !== "proposed" || s.author === undefined) continue; // no author: the user wrote it
    const prev = plans.get(s.author);
    if (!prev || s.version > prev.version) plans.set(s.author, s);
  }
  const out: Waiting[] = [];
  for (const a of agents) {
    if (a.state === "exited") continue;
    const requests = (pending.get(a.id) ?? []).slice().sort((x, y) => at(x.created_at) - at(y.created_at) || x.id - y.id);
    const questions = (asked.get(a.id) ?? []).slice().sort((x, y) => at(x.created_at) - at(y.created_at) || x.id - y.id);
    const plan = plans.get(a.id);
    const permission = a.state === "waiting_permission";
    if (requests.length > 0 || permission || questions.length > 0 || plan) out.push({ agent: a, requests, permission, questions, plan });
  }
  // Requests, questions and plans carry their time; a permission prompt only the agent's start.
  const since = (w: Waiting): number => {
    const times = [w.requests[0]?.created_at, w.questions[0]?.created_at, w.plan?.created_at]
      .filter((t): t is string => t !== undefined).map(at);
    return times.length > 0 ? Math.min(...times) : at(w.agent.started_at);
  };
  return out.sort((x, y) => since(x) - since(y) || (x.agent.id < y.agent.id ? -1 : 1));
}
