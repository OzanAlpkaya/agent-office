# 0035. Resuming conversations and restoring agents after a restart

- **Status:** Accepted; which session a resume continues amended in Phase 10 (2026-11-08) and Phase 17
  (2026-11-11)
- **Date:** 2026-11-08
- **Amends:** ADR 0011 (restart semantics), ADR 0022 (relaunch path, message delivery to a new
  session), ADR 0026 (host usage of resumed sessions)

## Context

Until Phase 9 a daemon restart ended every agent (`daemon_restart`, ADR 0011): its workspace and
branch stayed, but the conversation was gone, and Claude Code's own "Resume this session with:
claude --resume …" line could not be used (the session belongs to the agent's worktree, and for
sandboxed agents to their config dir inside the sandbox). The roadmap's Phase 9 DoD asks that a
restarted daemon restores agents; Ozan's backlog asks that the resume line becomes meaningful.

Spike (2026-11-08, Claude Code 2.1.319, haiku; `docs/plans/phase-9.md`):

- `claude --resume <old> --fork-session --session-id <new>` continues the old conversation under
  the new id: every hook carries `<new>`, `SessionStart` has `source: "fork"`, the old transcript
  is untouched. `--session-id` with `--resume` but without `--fork-session` is refused.
- An unknown id prints "No conversation found with session ID: …" and exits 1 before any hook.
- Interactive: a positional prompt is submitted; without one the TUI waits.
- The forked transcript repeats the parent's assistant records (same message ids) and its
  `cost-state` is cumulative.

Transcripts live in the user's `~/.claude/projects/<cwd>/` (host) and in the agent's config dir
under the data dir (sandbox), so both survive restarts and reboots.

## Options considered

**How a session continues**

1. **`--resume <old>` (same id).** Cons: the CLI refuses `--session-id` with it, so the daemon
   would learn the id only from the first hook; one id would name several processes (ADR 0028's
   correlation id, the `sessions` row, usage keys and budgets).
2. **`--resume <old> --fork-session --session-id <new>`.** Pros: one id and one row per process as
   before; the hook check is unchanged; the old transcript stays intact. Cons: the fork copies
   the parent's records into its transcript (handled in usage, below).
3. **A fresh session with a summary in the prompt** (the relaunch path of ADR 0022). Cons: loses
   the conversation; the model re-reads what it already knew.

**After a restart**

1. **Nothing automatic; a Resume button.** Cons: the DoD says the daemon restores agents.
2. **Restore automatically, opt-out (`--restore-agents`, default on).** Idle agents come back
   without a prompt and spend nothing until someone writes to them; an agent cut off mid-turn gets
   a short prompt from agent-office.
3. **Restore automatically and re-send the last prompt.** Cons: the daemon does not keep prompts,
   and replaying one could repeat work.

**Relaunches (review follow-ups, conflict rounds, architect escalation)**

1. **Switch them to resume.** Pros: the model keeps its context. Cons: every round drags the whole
   (possibly long, compacted) conversation into its input; the prompts already carry what a round
   needs; the e2e-proven behaviour changes.
2. **Keep them fresh.**

## Decision

Option 2, option 2 and option 2.

- **Resume** (`Engine.Resume`, `POST /api/agents/{id}/resume {prompt?}`, a Resume button on exited
  agents' cards and terminals): the relaunch path of ADR 0022 with `--resume <latest session>
  --fork-session --session-id <new>` in both command lines (host and container). Same agent id,
  workspace, branch, base, config dir and role; a new `sessions` row with `resumed_from`
  (migration 0009) and a fresh session budget (ADR 0027).
  - Who: exited workers, architects, leads and assistants whose workspace exists, in the daemon's
    sandbox mode, within `--max-agents`; leads, architects and assistants one at a time; a lead's
    or architect's goal must not be `done`. QA agents and reviewers are refused: their runs and
    reviews end with them (ADR 0021, 0033).
  - The task's status is left as it is (a resume continues; reports and the user move tasks).
  - Messages queued for the conversation (delivery's `since` is the resumed session's start) are
    delivered to the new session; without a prompt, idle delivery may start after `SessionStart`.
  - The memory section is not appended again; the conversation has it.
  - A CLI that exits before its first hook ends with `resume_failed`.
  - **Which session** (amended in Phase 10, after Ozan's test showed a sandboxed worker "resume
    failed" after its second restart): Claude Code writes a fork's transcript only once the fork
    gets a message (checked with 2.1.319: a fork shown without a prompt has no transcript, and
    `--resume` of it prints "No conversation found"). An idle restore and a Resume without a prompt
    make exactly such forks. The resume therefore continues the latest session, skipping resumed
    sessions in which no turn finished for the session they resumed (same conversation). Original
    sessions are never skipped, so a first turn cut off by a crash still resumes its own session.
    The engine tests' fake CLI models Claude Code's behaviour (`AO_FAKE_CONV_DIR`);
    `TestRestoreTwice` and the host e2e (step 7d) restore an agent twice.
  - **Interrupted forks** (amended in Phase 17, Ozan's backlog): the Phase 10 rule skipped every
    resumed session in which no turn finished, so a fork that got a prompt (a restore's "continue"
    prompt, a Resume with a message, a delivered message) and was killed before its turn ended was
    skipped too, and its partial conversation lost. Checked with Claude Code 2.1.322 (haiku, print
    mode): a fork killed during a tool call, and one killed right after its prompt was recorded
    before any answer, both have a transcript with the prompt (and the partial turn), and
    `--resume` of each continues it (it knew the codewords given in the fork and what it was doing).
    So sessions record whether they got a prompt (`sessions.prompted`, migration 0016, set on the
    main agent's first `UserPromptSubmit`), and the skip applies only to resumed sessions that
    neither got a prompt nor finished a turn. `TestResumeContinuesInterruptedSession` fails without
    the change.
- **Restore:** `agents.resume_state` records what an agent was doing when the daemon stopped it:
  the live state on a clean shutdown, the row's last state after a crash (set by startup
  reconciliation). With `--restore-agents` (`AGENT_OFFICE_RESTORE_AGENTS`, default true) the next
  start, after the sandbox restart imports and GitHub's restart rules, resumes those agents once:
  architect, lead, assistant, then workers by start time, within `--max-agents`. `working` and
  `waiting_permission` agents get agent-office's prompt ("the daemon restarted while you were
  working … check where you were and continue"); idle ones none. Not restored: QA and reviewers;
  agents still `starting` (no conversation yet); workers whose review or conflict round ended with
  the daemon (the round lives with the running agent, ADR 0022; they wait for Send to worker);
  agents of the other sandbox mode or without a workspace; agents beyond the slot limit. One inbox
  `info` notice from agent-office lists what came back and what did not, with reasons. All
  `resume_state` values are then cleared, so only the latest stop counts.
- **Host usage of a resumed session** (ADR 0026): the transcript reader skips message ids that the
  agent's earlier sessions recorded, and Claude Code's own cost for the session is its
  `cost-state` minus the CLI cost of the sessions it continues (never below zero). Sandboxed usage
  is metered per request and needs nothing.
- **Relaunches stay fresh** (ADR 0022 unchanged).

## Consequences

- "Resume this session" now has an agent-office equivalent, for host and sandboxed agents alike;
  the terminal says so instead of the Phase 8 note.
- A restart no longer costs the team its conversations. A mid-turn agent's interrupted tool call
  is lost; the restore prompt tells it to check where it was.
- Resuming a long conversation costs its (cached) input on the next turn; idle restores cost
  nothing until used. Budgets apply per session and goal as before.
- The behaviour depends on undocumented details of Claude Code 2.1.319 (copied records, cumulative
  `cost-state`). Tests pin both with fixtures, and the e2e checks them live; a change shows up as
  a failed usage check or a `resume_failed` agent, not as silent damage.
- Residual: a worker in a hand-started conflict round without GitHub ("Resolve conflict") is
  restored after a crash without its round, so its report is not held back for a missing merge
  commit; the conflict check on `report_done` runs again and records the conflict.
