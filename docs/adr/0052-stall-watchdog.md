# 0052. A stall watchdog in the daemon wakes the Tech Lead

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Twice in the PulseGo run the whole team sat idle, each agent waiting for another: workers had
exited, the Tech Lead waited for their reports, and nothing told anyone. Agents act only on input,
so a team that stops producing input stays stopped until the user notices.

## Options considered

1. **Ask the Tech Lead to poll** (a prompt rule). Burns tokens, and an idle model does not poll.
2. **An LLM supervisor** watching the team. Another agent to watch, with its own cost and failure
   modes, deciding something deterministic.
3. **Daemon code**: a timer per goal, waking the lead with facts.

## Decision

Option 3. Every few seconds the engine looks at each goal in progress with a running Tech Lead. The
goal is **stalled** when every agent of it (lead, architect, QA, workers of its sub-tasks) is idle or
exited, none is starting, relaunching or being typed to, no message waits for a running agent,
and nothing waits on the user: no unanswered question or blocked message, no plan waiting for
approval, no access request the user decides. After `--stall-after` (default 3 minutes, 0 off) the
lead gets a message from agent-office: who is idle or exited and why, the open sub-tasks with the
calls that move them (`assign_task`, `resume_agent(agent_id=...)`, `reclaim_task(task_id=...)`,
`send_message` to an idle worker, `start_qa` or `report_done` when everything is reported), the
undelivered messages and the access requests it decides.

Bounded: a nudge resets the wait, and the next one waits twice as long; after 3 nudges while nothing
in the goal changed (its sub-tasks' statuses, its agents and their sessions), the user's inbox says
the team is stuck, once. A change starts a new count.

## Consequences

- The incidents' "everyone waits" state ends within minutes without the user.
- A lead that answers a nudge with words only is nudged at most three times.
- A message stuck for a running agent counts as movement, so the watchdog stays quiet then; the
  delivery rules (ADR 0018) are what must not get stuck.
- Tested: `TestStallWatchdogWakesTheLead`, `TestStallWatchdogWaitsForTheUser`,
  `TestStallWatchdogIgnoresReportedGoals`; live in `TestAutonomousTeamEndToEnd`.
