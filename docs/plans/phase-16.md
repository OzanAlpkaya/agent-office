# Phase 16 plan: Autonomous team

- **Status:** In progress on `phase-16/autonomous-team`. Written 2026-11-11 with the recommended
  option for every choice, as Ozan asked ("implement it without waiting for my plan approval; take
  the recommended option on every choice, record decisions in ADRs and give me one end-of-phase
  summary").
- **Roadmap:** [docs/plan.md](../plan.md), Phase 16. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Source:** Ozan's PulseGo Sprint 3 run, where he stepped in about ten times. Each incident below is
  one he reported.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A goal runs from start to "ready for review" without the user relaying anything. The Tech Lead sees
what the user saw (why an agent died, who waits on whom, what is undelivered) and has the tools to
act on it; the daemon recovers what it can by itself and wakes the lead when the team is stuck. The
user is asked only for decisions that are theirs: merges and pushes, secrets, pack changes and
anything irreversible.

## What exists and what it means

- **Exit handling** (`engine.watch`) records `exit_reason` (`process_exit`, `stopped`, ...) and the
  exit code of the PTY's process; for a sandboxed worker that is the `podman run` client, whose code
  is the container's. Nothing says *why*; the UI shows "process exited".
- **Worker containers run with `--rm`**, so `podman inspect` after the exit finds nothing.
  Checked on this machine (rootless Podman 6.1.4, runc, systemd cgroups): `State.OOMKilled` stays
  `false` even for a real OOM kill of the container's main process, and the container's cgroup is
  removed about 6 ms after the kill, before a host-side watcher can read `memory.events`. Inside the
  container, the agent's parent (`agent-office sandbox-init`) can read its own cgroup's
  `memory.events` (`oom_kill 1`) and `memory.max` after the agent died.
- **Resume** (ADR 0035) continues a conversation in the same workspace; only the user starts it.
- **Messages** (ADR 0018) to an agent that is not running are refused ("agent ... is not running").
  A resumed session receives messages queued since the start of the session it continues; a
  relaunch only those queued since its own start.
- **Access requests** (ADR 0019) are decided by the user; an approval adds the host to the running
  session's in-memory grants (`sandboxRes.grants`, a fresh `sandbox.Grants{}` per launch), and the
  agent's exit expires its pending requests. That is the request #4 bug: the approval belonged to
  the session that asked, so the resumed session did not have it.
- **Save work** commits a sandboxed agent's uncommitted changes, only when the user presses it.
- **Worker memory** is the daemon's `--sandbox-memory` (default 2g) for every project.

## Decisions (recommended options)

| # | Question | Decision |
|---|---|---|
| 1 | How is an OOM kill detected? | `sandbox-init` writes an exit report (the agent's signal, its cgroup's `oom_kill` count and `memory.max`) to a daemon-created file mounted into the worker; the daemon also reads `podman inspect`'s `State.OOMKilled` (workers no longer use `--rm`; the daemon removes them after the inspect). Either source means "out of memory". While a worker runs, the daemon polls its cgroup's `memory.events`, so an OOM kill of a child process (a `go test`) is told to the agent and the UI too. The report is written inside the sandbox, so it is advisory: it labels an exit, it grants nothing. |
| 2 | What is an unexpected exit? | `process_exit` with a non-zero code, a signal or an OOM kill. An exit with code 0 is "ended by itself": the lead is told, no automatic resume. Stops by the user, the lead, a report or the daemon are expected. |
| 3 | Who is resumed automatically? | Workers, Tech Leads and architects, once per agent (`agents.auto_resumes`, persisted). Not while the daemon shuts down, not for a done goal, not for a resume that failed to load its conversation. The resume prompt says what happened and that uncommitted work was saved. |
| 4 | Where does a second failure go? | To the goal's running Tech Lead as a message with the reason and the next steps (`resume_agent`, `reclaim_task`). A Tech Lead's or architect's own failure, or a worker without a running lead, goes to the user's inbox. |
| 5 | New lead tools | `resume_agent` (an exited worker of its goal, optional message), `stop_agent` (a running worker of its goal, with a reason), `reclaim_task` (an in_progress or blocked sub-task whose agents all exited goes back to todo), `decide_access` (see 9). Authorization in code, from the caller's socket, like every team tool (ADR 0017). |
| 6 | Where does a reassigned sub-task start? | From the previous worker's branch when it has commits (its saved work included); otherwise as before (dependencies or the goal's base). |
| 7 | Messages to agents that are not running | Queued when the recipient can be resumed (worker, lead, architect, assistant with its workspace); the sender is told "queued until it is resumed". A relaunch (ADR 0022) also gets messages queued since its predecessor exited. Removed agents and QA/reviewers still refuse. |
| 8 | Stall watchdog | Daemon code, per engine: a goal in progress whose Tech Lead runs, every agent of the goal idle or exited, no message pending for a running agent, nothing waiting on the user (an unanswered question or blocked item, a pending access request the user decides, a plan waiting for approval), for `--stall-after` (default 3 min, 0 off). Then the lead gets a status summary (agents and states with exit reasons, open sub-tasks, undelivered messages, what it can do). At most 3 nudges per stall, each after twice the previous wait; then the user's inbox says the team is stuck. Any agent working again ends the stall. |
| 9 | Access decisions | Project setting `access_decider`: `user` (default, as today), `lead`, `lead_policy` (the lead may approve only hosts in `lead_access_hosts`, a host list like the egress allowlist; others go to the user). The lead may always deny. Requests reach the lead as a message; the user can still decide first in the UI. Decisions record who (`user` or `lead` and its agent id), a reason (required for the lead) and a scope: `agent` (that agent, every later session) or `project` (every agent of the project). Approvals are stored as grants (`access_grants`) and loaded into every launch: that fixes request #4. Both settings widen the sandbox, so they are accepted from this machine only (ADR 0046). Every lead decision is also an inbox info item for the user. |
| 10 | The lead's rule on questions | In the Tech Lead's role prompt: decide anything reversible yourself (which worker, retries, resumes, reassigning, temporary waivers); ask the user only for merges and pushes, secrets, pack or project setting changes and anything irreversible. A waiver is recorded in the repository by the worker of that sub-task (an entry in `WAIVERS.md`: what, why, how to undo) and named in the report, so it is in the branch the user reviews; the lead's workspace stays read-only. |
| 11 | Worker memory | `resources.memory` in pack.yaml and `worker_memory` in project settings (`512m` to `64g`); a project's value wins over its pack's, which wins over `--sandbox-memory`. The exit message names the limit and where to raise it. |
| 12 | Uncommitted work on exit | Every exiting worker with uncommitted changes gets a WIP commit (the user's identity plus the agent's trailer, the agent's hooks off), then the usual exit import. Sandboxed through the git helper (the existing Save work), on the host with git in the worktree. The agent gets a queued message about it. |
| 13 | Inbox | Grouped by goal (then agent-office's own items), agents within a goal; answered and read items collapsed into a "n earlier" line per group; briefings in their own section. |
| 14 | Memory proposals | The assistant's prompt and `propose_memory`'s description: a note is text agents read; it changes no setting, permission, network access or budget, so it must not claim such effects ("agents won't need to request access again"). |
| 15 | Smaller findings | The go pack allows `storage.googleapis.com` and installs `make` (pinned, ADR 0041). |

## Sub-phases

1. **Schema** (migration 0015): `agents.exit_signal`, `agents.exit_detail`, `agents.oom_killed`,
   `agents.auto_resumes`; `access_requests` rebuilt with `decided_by` gaining `lead`, plus
   `decider_agent_id`, `decision_reason`, `scope`; new `access_grants`.
2. **Exit reasons:** `sandbox-init` exit report, `podman inspect`, host signals; `explainExit` in
   plain words; `AgentInfo.exit_detail`, `exit_signal`, `oom_killed`; the UI shows them on the card
   and in the terminal's closing line.
3. **Autosave on exit** (sandbox and host).
4. **Queued messages** for agents that are not running.
5. **Self-recovery and lead notifications.**
6. **Lead tools** `resume_agent`, `stop_agent`, `reclaim_task`; reassigned sub-tasks start from the
   previous branch.
7. **Access:** grants, scopes, the decider setting and `decide_access`; the UI's approve-for-project.
8. **Stall watchdog.**
9. **Worker memory** per pack and project; the running OOM watch.
10. **Prompts and the go pack.**
11. **Inbox UI.**
12. **`TestAutonomousTeamEndToEnd`**, ADRs, docs, PROGRESS.

## Risks

- **A real OOM kill in tests** needs the kernel to pick the agent: a test raises the agent's
  `oom_score_adj` (allowed for the same user) and starts a memory hog next to it.
- **Automatic resumes cost tokens**; bounded to one per agent, and none for the deterministic causes
  that cannot be fixed by a retry (a failed conversation load).
- **The lead deciding access** widens what an agent-written reason can achieve; off by default,
  confined by the host policy, recorded with a reason, and the user sees every decision.
- **The watchdog could wake a lead in a loop**; bounded nudges, then the user.
- **haiku leads may not act on a message** (memory: name the next steps explicitly); every
  notification lists the tools to use.

## Answers to open questions

Every choice took the recommended option, as Ozan asked; they are the decisions above.

## Changes during the phase

(appended during the phase)
