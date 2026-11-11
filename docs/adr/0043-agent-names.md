# 0043. Agent names

- **Status:** Accepted; extended 2026-11-09 (agents see the names, see the end)
- **Date:** 2026-11-09

## Context

Agents were identified in the UI by their random hex id (`c3d4e5`). With five or more agents,
people mix them up: "the one fixing the filter" is easier to remember as a name than as six hex
digits. Ozan asked for a human first name per agent that stays the same for the agent's
lifetime, is unique among active agents, comes from a diverse list of common names, and does not
replace the id in the API, logs and branch names.

## Options considered

1. **Names chosen in the browser from a hash of the id.** No daemon change. But two agents can
   hash to the same name, and different browsers or UI versions could show different names.
2. **Names chosen by the agents** (asked in their prompt). Not deterministic, and an agent's text
   is not trusted input for something the UI uses as a label.
3. **Names chosen by the daemon when the agent starts, and stored with it.** One source for every
   device; uniqueness can be checked against the agents the engine knows; survives restarts.

## Decision

Option 3. `internal/engine/names.go` holds 130 common first names from Europe, Turkey, the
Middle East, Central and South Asia, East and Southeast Asia, Africa, the Americas and Oceania:
a mix of names usually given to women, to men and to either. They are written in ASCII (Ines,
not Inês), two to eight letters, so they fit an office nameplate and type the same in a search
box.

- `Start` picks the name under the engine lock: the id's FNV-1a hash chooses a position in the
  list, and the first free name from there wins. A name is free if no listed agent (running or
  exited, not removed) has it and no other start in progress has reserved it. If all 130 are
  taken, it must only be free among running agents; after that the name gets a number
  ("Emma 2").
- The name is stored in `agents.name` (migration 0012) and served as `name` in the agent JSON. A
  relaunch (follow-up, conflict round, resume) keeps it, and so does a daemon restart. Agents
  recorded before this migration get a name at the next start of the daemon.
- The id stays the key everywhere: the API, the team MCP tools, the logs (`agent_id`), branch
  names and commit trailers. The UI shows the name first, the short id as secondary text, and the
  full id in the tooltip.

## Consequences

- People, the UI and the agents talk about "Yuki"; logs still say `c3d4e5`. An id in an agent's
  message matches the short id the UI shows next to the name.
- A name is unique among the agents in the list, not forever: a removed agent's name can come
  back for a new agent. Usage history of a removed agent shows its id.
- The list is fixed in code; changing its order changes which names new agents get, never the
  names of existing agents (they are stored).

## Extension (2026-11-09): agents see the names

Agents saw their teammates as `agent-c3d4e5` or a bare id, so a Tech Lead's messages to the user
named nobody the user could find. Now an agent sees the same names as the user, always next to
the full id, which is what the team tools take (`send_message` addresses an agent by id):

- its first prompt says "In this team you are Yuki (c3d4e5f6a7b8)", and asks it to name a
  teammate the same way, with name and agent id;
- delivered messages begin `From Yuki (worker, agent id c3d4e5f6a7b8):`;
- reports to the lead begin `Report from worker Yuki (c3d4e5f6a7b8)`;
- `list_tasks` (the caller, its lead, a task's agents), `get_briefing`, `assign_task` and
  `start_qa` carry a `name` or `agent_name` field;
- the briefing's "waiting on you" lines and the restart notice name agents the same way.

The full id, not the UI's six characters, because a short id passed to a tool would not resolve.
The UI's short id is its prefix, so the two still match. Commit trailers, GitHub comments and
the git identity keep `agent-<id>`: they leave agent-office, and names are not unique over
time. Tools still take ids, not names. Accepting a name in `send_message` would be possible
(names are unique in the list), but an id cannot become ambiguous later.

## Amendment (2026-11-11, Phase 14): unique per project

Names are unique among the listed agents of one project (ADR 0046): each project's engine names its
own agents, so two offices may both have an Ada. Nothing is lost, since names never leave their
project in the UI or the team tools, and ids stay globally unique.
