# 0036. Session history: every session's terminal output, saved in the data dir

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0002 (scrollback outlives the process), ADR 0028 (retention covers it)

## Context

A session's terminal output lived only in the daemon's memory (the 256 KiB scrollback ring of
ADR 0002). After a restart an exited agent showed "terminal not available", and a relaunched or
resumed agent's earlier sessions could not be read at all. The Phase 9 DoD asks that a restarted
daemon restores history.

Forces: output can contain anything the agent printed (prompts, code, a pack secret it echoed),
so it is sensitive like message bodies (ADR 0018); sandboxed agents' transcripts belong to the
subordinate uid and are 0600 (ADR 0014), so the daemon cannot read them without a helper container;
a crash must not lose much.

## Options considered

1. **Rebuild the history from transcripts.** Cons: needs a renderer for an undocumented format;
   sandbox transcripts are not readable by the daemon; it shows the conversation, not what the user
   saw.
2. **Append every PTY chunk to a log file.** Pros: nothing lost. Cons: unbounded files; a hot write
   path in the PTY reader; TUI redraws make the raw stream much larger than the scrollback.
3. **Save the scrollback ring periodically and at exit.** Pros: bounded (256 KiB per session),
   identical for host and sandboxed agents, replays exactly like a re-attach. Cons: a crash loses
   the last few seconds.

## Decision

Option 3.

- `terminal.Session.Snapshot` returns the scrollback and a counter that changes with output; per
  running session a goroutine saves it every 5 s when it changed and once when the process exits,
  to `<data dir>/terminals/<project>/<agent>/<session>.log` (directory 0700, file 0600, temp file
  and rename).
- `GET /api/agents/{id}/sessions` lists the agent's sessions (start, end, `resumed_from`, turns,
  tool calls, live, saved); `GET /api/agents/{id}/sessions/{sid}/terminal` returns one session's
  bytes (the live scrollback for the running one), `application/octet-stream`, `no-store`, at most
  1 MiB, read without following symlinks. A session of another agent is not found.
- The UI's terminal pane shows a session picker when there is more than one, and replays saved
  output into a read-only terminal, also for agents of an earlier daemon run.
- Removing an agent deletes its saved terminals; the daily retention loop deletes files older than
  `--event-retention-days` (ADR 0028). Paths and contents are never logged.

## Consequences

- History survives restarts and reboots; "terminal not available" is gone.
- Output is now on disk: user-only files in the data dir, like the database (threat model row).
  They are deleted with the agent and by retention, never written to the repository or logs.
- A crash loses up to 5 s of output. The live terminal still has everything while it runs.
