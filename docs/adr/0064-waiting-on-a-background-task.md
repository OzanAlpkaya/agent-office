# 0064. A state for an agent waiting on a background task

- **Status:** Accepted
- **Date:** 2026-11-12

## Context

A worker that starts `./check.sh` with Claude Code's `run_in_background` ends its turn and waits.
Claude Code notifies the model when the command finishes (checked in the 2.1.322 bundle: "a background
task you started with `run_in_background`, you will be notified"). Meanwhile the Stop hook says the turn
ended, so agent-office showed the worker as Idle. On the board that looked like a stuck worker, and the
stall watchdog (ADR 0052) could count it as idle too.

## Options considered

1. **Hooks only**: mark the agent from a `run_in_background` PreToolUse until its next turn. There is no
   hook when the background command ends, so an agent whose command finished but which was not woken
   would stay "waiting" forever.
2. **The process tree**: after such a turn, check whether processes the agent started are still running,
   and poll until they are gone. This is true to what runs, but needs a reliable way to tell the
   command's processes from the CLI's own long-lived helpers (MCP servers).
3. **Ask the CLI**: Claude Code has no interface for its background tasks that a hook can reach.

## Decision

Option 2, started by the hook signal of option 1.

- The Claude Code adapter reads one boolean of the tool input: `run_in_background` on a Bash
  `PreToolUse` (`agent.Event.Background`). The command and anything else stay undecoded, as before.
- On that PreToolUse (the hook blocks the tool until the daemon answers), the engine takes a snapshot of
  the agent's processes: below its CLI's PTY process on the host, and in the container's cgroup in a
  sandbox (the cgroup the memory watch already finds, ADR 0051). Only pids, parents and the first two
  words of a command line are read from `/proc`. Nothing is logged or stored.
- When a turn of that session ends idle, processes that are new since the snapshot and are not
  agent-office's own helpers (`agent-office mcp`, `agent-office hook`) mean a background command still
  runs. The agent is then shown as `waiting_background` ("Waiting on background task"), rechecked every
  5 seconds, and back to Idle once they are gone. Any hook that moves the agent (a new turn, a tool, a
  permission prompt) ends the wait at once. Zombies do not count.
- The engine's own state stays idle, so messages are still delivered (they start a turn, as for an
  idle agent). The shown state is in the agent view, the board card ("Background task"), the agent list,
  and the office, where the character stays at its desk with code on its screen. The wait is recorded as a
  reduced event, so the run info counts it as waiting time. The stall watchdog treats it as busy.
- Any failure (no snapshot, `/proc` or the cgroup not readable) leaves the agent idle, as before. Codex
  agents are unchanged.

## Consequences

- A worker waiting for its checks reads as such, and the watchdog does not wake its lead for it.
- A process an agent starts after the snapshot and leaves running for another reason (a dev server
  started in the foreground with `&`) also counts as a background task. That is what it is.
- Tested with a fake agent on the host (`TestWaitingOnBackgroundTask`, a real `sleep`) and in a real
  worker container (`TestSandboxedWaitingOnBackgroundTask`).
