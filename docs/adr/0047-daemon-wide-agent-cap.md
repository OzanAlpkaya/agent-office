# 0047. A daemon-wide agent cap across projects

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

`--max-agents` bounded the agents of the one project, so spend and host load were bounded by one
number. With several projects (ADR 0046) each project has its own engine, which counted only its
own agents. The DoD asks for one cap across projects, with an optional cap per project.

## Options considered

1. **Each engine asks the others for their counts.** Needs every engine's lock from inside another
   engine's start: two projects starting at once would take each other's locks in opposite orders
   and deadlock.
2. **A shared pool the engines report to.** Each engine tells the pool how many agents it holds
   (running and starting); a start reserves a slot in the pool while holding its own lock. The pool
   never calls into an engine, so the only lock order is engine → pool.
3. **A semaphore taken at start and released at exit.** Simple, but every path that ends an agent
   (exit, failed start, failed relaunch, a resume that never ran) would have to release exactly
   once; the engine already derives "running" from its agents, so a second counter can drift.

## Decision

Option 2. `slotPool` holds each project's count. A start or relaunch (`reserveSlotLocked`, under the
engine's lock) first checks the project's own cap (`max_agents`, at most the daemon's), then asks
the pool to reserve: the check of the sum against `--max-agents` and the reservation are one
critical section, so two projects cannot both take the last slot. After a start finishes or fails
and when an agent exits, the engine reports its count again (`syncSlotsLocked`), derived from its
agents as before, so the pool cannot drift from what runs. A refusal says which cap was reached and
how many agents the other projects run. The Tech Lead's prompt ("at most N agents") uses the
project's effective cap.

## Consequences

- An idle agent keeps its slot in every project (ADR 0029's rule for idle leads, now daemon-wide):
  a project whose Tech Lead idles holds one slot of the daemon's cap.
- Tested with concurrent starts from two projects (`TestHubCapHoldsUnderConcurrentStarts`: exactly
  the cap starts), resumes (`TestHubCapCountsResumes`), a per-project cap (`TestHubProjectCap`), a
  slot freed in one project used by another (`TestHubCapAcrossProjects`) and live in
  `TestMultiProjectEndToEnd`.
- `GET /api/status` reports the cap and how many agents run across projects.
