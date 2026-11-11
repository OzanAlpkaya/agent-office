# 0051. Exit reasons, one automatic resume, saved work and the lead's powers over its workers

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

In Ozan's PulseGo Sprint 3 run a sandboxed worker was OOM-killed four times and the UI showed only
"[process exited]"; the Tech Lead could neither resume nor stop its workers nor take a sub-task back
from one that had died, so Ozan relayed each time. A worker's uncommitted work stayed in its clone
until someone pressed Save work, and messages to a stopped worker were refused, so instructions
were lost.

Measured on this machine (rootless Podman 6.1.4, runc, systemd cgroups): Podman's
`State.OOMKilled` stayed `false` for real OOM kills, also when the container's main process was the
victim, and no `oom` event was emitted. The container's cgroup (`memory.events`, `oom_kill`) is
right, but it is removed about 6 ms after the kill, so a host-side watcher (inotify on the file)
races it and loses. Inside the container, the agent's parent `agent-office sandbox-init` outlives
the agent and reads its own cgroup's `memory.events` and `memory.max` after it died.

## Options considered

1. **`podman inspect` only.** What Ozan named, but it reports nothing on this machine.
2. **A host-side cgroup watcher.** Correct counts while the worker runs; too late at the end.
3. **sandbox-init writes an exit report** (signal, OOM kills, limit) into a daemon-created file
   mounted into the worker; the daemon reads it, `podman inspect` too, and a host-side watcher
   covers the running worker (a child's OOM kill).
4. For recovery: **always resume** (loops on a deterministic failure), **never** (today), or
   **once per agent**, then the lead.

## Decision

Option 3, and once per agent.

- **Exit reasons.** `agents.exit_signal`, `oom_killed`, `exit_detail` (migration 0015). The detail is
  agent-office's own sentence: "Out of memory: the kernel killed the agent because its worker
  reached its 2g memory limit ...", "Killed by signal KILL ...", "exited by itself with exit code
  N", "Stopped by the Tech Lead: <reason>". OOM means the report's signal is KILL with an `oom_kill`
  count, or `podman inspect` says so; worker containers no longer run with `--rm` so the daemon can
  inspect them, and it removes them itself (as before; orphans are cleaned at start). Host agents
  get the signal from their wait status.
- **The running watch** reads the worker cgroup's `oom_kill` every 3 s; an increase is a sandbox
  event and a message to the agent saying its command ran out of memory.
- **Failures.** A `process_exit` with a non-zero code, a signal or an OOM kill. An exit 0 is the CLI
  ending by itself; stops, reports and the daemon's own stops are expected.
- **One automatic resume** per worker, Tech Lead or architect (`agents.auto_resumes`), with a prompt
  saying what happened and that uncommitted work was saved; never while the daemon shuts down.
  The goal's Tech Lead is told; a second failure, an exit 0 or a failed resume goes to it with the
  reason and the exact calls to make (`resume_agent(agent_id=...)`, `reclaim_task(task_id=...)`).
  An agent with nobody above it (a Tech Lead, an architect, a worker without a lead) tells the
  user's inbox instead.
- **Saved work.** Every exiting worker with uncommitted changes gets a WIP commit (the user's
  identity, the agent's trailer, its hooks off): the Save work helper in the sandbox, `git` in the
  worktree on the host. Then the exit import runs as before. The agent gets a queued message about
  it. Without a git identity on the host, nothing is committed and the worktree stays dirty (kept
  on removal, as before).
- **The lead's powers.** `resume_agent` (an exited worker of its goal, with a message), `stop_agent`
  (a running one, with a reason, recorded as `stopped_by_lead`), `reclaim_task` (an in-progress or
  blocked sub-task without a running agent goes back to todo). A sub-task's next worker starts from
  the previous worker's branch when it has commits. All authorized by the caller's socket and its
  goal, like every team tool (ADR 0017).
- **Messages wait.** A message to a worker, lead, architect or assistant that is not running (or is
  ending after its report) is stored and delivered when it is resumed; the sender is told it waits.
  A relaunch (ADR 0022) also gets messages queued after its predecessor exited. The user's messages
  and inbox replies follow the same rule.

## Consequences

- The UI, the lead and the user see why an agent ended; an OOM kill says which limit to raise.
- The exit report is written inside the sandbox, by a process the agent could imitate: it is parsed
  strictly and only labels an exit. Whatever it says, an agent gets one automatic resume at most.
- Automatic resumes cost tokens; one per agent bounds it. A second automatic attempt after a long
  healthy run would be friendlier, at the price of a loop risk; not done.
- WIP commits change a worker's branch on exit. They are marked and the agent is told; squashing is
  the agent's or the user's choice.
- Tested: `TestInitWritesExitReport`, `TestUnexpectedExitResumedOnce`, `TestExitSavesUncommittedWork`,
  `TestMessageToStoppedWorkerWaits`, `TestLeadResumesAndStopsItsWorkers`,
  `TestReclaimTaskContinuesFromTheBranch`; with real Podman `TestSandboxedOOMKillIsExplained`,
  `TestSandboxedChildOOMIsReported`, `TestSandboxedRemoveKeepsDirtyAndSavesWork`; with real Claude
  Code `TestAutonomousTeamEndToEnd`.
