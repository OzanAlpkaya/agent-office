// AgentName shows an agent's first name with its short id as secondary text
// (ADR 0043). The full id is in the tooltip; ids stay the key everywhere else.
import { type JSX, createContext, useContext } from "react";
import type { Agent } from "./api";
import { agentLabel, shortId } from "./agents";

// AgentsContext holds the daemon's agents, so a component that only has an
// agent id (an inbox item, a review, a usage row) can show the name.
export const AgentsContext = createContext<readonly Agent[]>([]);

// useAgentLabel returns a function from an agent id to its name (or short id).
export function useAgentLabel(): (id: string) => string {
  const agents = useContext(AgentsContext);
  return (id) => agentLabel(agents.find((a) => a.id === id), id);
}

export function AgentName({ agent, id, role = false }: {
  agent?: Pick<Agent, "id" | "name" | "role">;
  id?: string; // looked up when agent is not given; shown as a short id if unknown
  role?: boolean; // prefix the role name
}): JSX.Element {
  const agents = useContext(AgentsContext);
  const a = agent ?? agents.find((x) => x.id === id);
  const full = a?.id ?? id ?? "";
  const tip = [a?.role, a?.name, full].filter(Boolean).join(" · ");
  return (
    <span className="agent-name" title={tip}>
      {role && a?.role && <span className="agent-role">{a.role} </span>}
      <span className="agent-first">{agentLabel(a, id)}</span>
      {a?.name && <span className="agent-id mono">{shortId(full)}</span>}
    </span>
  );
}
