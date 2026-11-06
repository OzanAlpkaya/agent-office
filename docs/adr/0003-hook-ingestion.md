# 0003. Hook ingestion: command hook forwarder over a per-agent unix socket

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

Agent state (working, waiting for permission, idle, exited) comes from the agent CLI's hooks, not
from scraping terminal output. The daemon must receive every event, know which agent sent it, and
be able to answer (Stop-hook message injection). Agents will later run in containers without
network access to the host. Evidence: `spikes/hooks`, `spikes/sandbox`, `docs/research/codex-cli.md`.

Spike findings (Claude Code 2.1.319):
- HTTP hooks work (POST JSON, response parsed like stdout) but **do not fire for SessionStart**.
- Command hooks fire for every event, and their stdout can block a Stop with a message.
- Codex documents only command hooks.
- A container with `--network none` can reach a host unix socket through a bind mount.
- User-level settings, including the user's own hooks, merge into spawned sessions.

## Options considered

1. **HTTP hooks to a loopback port.** Pros: no extra process per event. Cons: no SessionStart; the
   endpoint needs a separate auth scheme; unreachable from an internal-only container network;
   Claude-only.
2. **Command hook running `agent-office hook`**, which forwards stdin to the daemon over a
   **per-agent unix socket** and prints the reply. Pros: every event, same mechanism for Codex,
   works inside containers, and the socket path is the identity (mode 0600, one socket per agent,
   only that socket mounted into that agent's container). Cons: one short process spawn per event
   (a few ms).
3. **Tail the transcript JSONL.** Pros: no hooks needed. Cons: undocumented format; no
   permission/idle signals; no way to answer.

## Decision

Option 2. The daemon writes a settings file per agent, with every relevant event mapped to
`agent-office hook`, and passes it with `--settings`. The forwarder reads `AGENT_OFFICE_SOCKET`,
sends `{event payload}`, waits for the reply (with a timeout below the hook timeout) and writes it
to stdout. The daemon checks that the payload's `session_id` matches the session it launched on
that socket. Adapters translate CLI-specific payloads into common events (`SessionStarted`,
`TurnStarted`, `ToolStarted`, `ToolFinished`, `PermissionRequested`, `TurnFinished`, `Idle`,
`SessionEnded`). Events with an `agent_id` (subagents) never change the main agent's state.

`PermissionRequest` is the signal for "waiting for permission": it arrives immediately, while
`Notification(permission_prompt)` arrives about 6 s later.

## Consequences

- If the daemon is down, the forwarder fails fast with exit 0 and no output, so the agent is not
  blocked.
- Hook payloads contain prompts, file contents and assistant text. The daemon keeps only the
  reduced fields it needs and never persists raw payloads.
- The child env is scrubbed of the daemon's own `CLAUDECODE`/`CLAUDE_CODE_*` variables (one is a
  messaging token).
- **User settings are an explicit option, not an assumption** (decided by Ozan, 2026-11-06):
  - **Host mode:** a config option `inherit_user_settings` (bool, **default `true`**; its exact name
    and location get fixed with the Phase 1 config) decides whether an agent loads the user's own
    settings and hooks, which may be protective (e.g. secret guards). `true` uses Claude Code's
    default setting sources plus the daemon's `--settings` file (hook lists merge). `false` passes
    `--setting-sources project,local`. The effective value is logged at agent start and shown in
    the UI.
  - **Container mode:** agents always get a clean, project-defined config: a dedicated
    `CLAUDE_CONFIG_DIR` inside the container, containing only the daemon-generated settings and
    the project's settings. The user's config directory is never mounted. The option does not
    apply.
- Not yet observed: Stop on interrupt (Esc), and the permission-denied path. Phase 1 adds a
  process-exit and timeout fallback so state cannot stay "working" forever.
