// What waits on the user (ADR 0044): the one source for the Approvals badge,
// its drawer, the tab title and the Office's "waiting for you" count. Pure.
import type { AccessRequest, Agent } from "./api";

// Waiting is one agent that cannot go on until the user acts: it has access
// requests to allow or deny (ADR 0019), a permission prompt open in its
// terminal (host mode), or both. Counts are of agents, not requests.
export interface Waiting {
  agent: Agent;
  requests: AccessRequest[]; // pending, oldest first
  permission: boolean; // a prompt in its terminal; answered there, not in the UI
}

// waitingOnUser lists the agents waiting on the user, longest waiting first.
// Exited agents wait for nobody: the daemon expires their requests when they
// exit, and a pending request of an agent not in the list is the same case
// seen before its update arrived.
export function waitingOnUser(agents: readonly Agent[], accessRequests: readonly AccessRequest[]): Waiting[] {
  const pending = new Map<string, AccessRequest[]>();
  for (const r of accessRequests) {
    if (r.status !== "pending") continue;
    pending.set(r.agent_id, [...(pending.get(r.agent_id) ?? []), r]);
  }
  const out: Waiting[] = [];
  for (const a of agents) {
    if (a.state === "exited") continue;
    const requests = (pending.get(a.id) ?? []).slice()
      .sort((x, y) => Date.parse(x.created_at) - Date.parse(y.created_at) || x.id - y.id);
    const permission = a.state === "waiting_permission";
    if (requests.length > 0 || permission) out.push({ agent: a, requests, permission });
  }
  // Requests carry their time; a permission prompt only the agent's start.
  const since = (w: Waiting): number => Date.parse(w.requests[0]?.created_at ?? w.agent.started_at);
  return out.sort((x, y) => since(x) - since(y) || (x.agent.id < y.agent.id ? -1 : 1));
}
