# 0018. Message delivery: claimed once in SQLite, delivered on Stop or typed when idle

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

`send_message` (ADR 0017) must reach the recipient agent, which is a Claude Code session in a PTY.
There is no API to push text into a running session. Spike B showed two working paths: answering
a **Stop** hook with `{"decision":"block","reason":"<text>"}` (the session continues with the text
as feedback), and typing into the PTY, which the daemon owns. The Phase 4 DoD asks that messages
reach the recipient after its next Stop event, **exactly once**.

Forces:

- A Stop only happens at the end of a turn; an idle agent has no pending Stop.
- `stop_hook_active` is set on the Stop that ends a turn which a Stop hook already continued; it is
  Claude Code's loop guard.
- The daemon owns all PTY input (ADR 0008): anything typed must go through the engine and must
  never be a lone Esc or Ctrl-C. Message bodies are written by other, untrusted agents.
- The hook forwarder has a timeout; if the daemon answers late or not at all, the agent continues
  without the message.

## Options considered

1. **Stop hook only.** Pros: one path. Cons: a message to an idle agent waits until a human prompts
   it, which defeats delivery to an idle lead waiting for its workers.
2. **PTY only.** Pros: one path. Cons: typing into a working agent's TUI interrupts or garbles its
   turn.
3. **Both: Stop hook for agents that are working, PTY for agents that are idle**, with a single
   claim step so a message can only be handed out once.
4. **Pull: a `check_messages` tool.** Pros: no PTY tricks. Cons: depends on the model remembering to
   poll, burns turns while waiting.

## Decision

Option 3.

- **Storage:** `messages` (migration 0004): sender (an agent id, or empty for agent-office itself),
  recipient, body (1–4 000 characters), created, delivered, delivery (`stop_hook` / `pty`).
- **Claim:** `UPDATE messages SET delivered_at = ?, delivery = ? WHERE id IN (SELECT … recipient's
  undelivered, oldest first, LIMIT 10) RETURNING *`. One statement, so two paths racing for the same
  message cannot both get it.
- **Stop path:** on a `Stop` hook of the main agent (not a subagent's, not `StopFailure`) with
  `stop_hook_active` false, claim and reply `block` with the formatted messages. The state stays
  `working` (the turn continues). With `stop_hook_active` true nothing is claimed; the agent goes
  idle and the PTY path picks the messages up.
- **PTY path:** when the recipient becomes idle (Stop without delivery, `idle_prompt`, user
  interrupt) or a message arrives while it is idle, the engine waits 1.5 s for the TUI to settle,
  and delivers only if the agent has finished at least one turn (so it never types over the
  initial prompt) and the user has not typed in its terminal for 5 s (otherwise it retries later).
  It claims, writes one line, waits, and sends Enter, through the same input path as the UI. If the
  write fails, the claimed messages are put back (`delivered_at` cleared) for a later delivery.
- **Sanitizing:** the PTY line has every control character (C0, DEL, C1; newlines become spaces)
  removed, so no escape sequence, Esc or Ctrl-C written by another agent reaches a terminal. The
  Stop path sends the text as a JSON string and keeps newlines.
- **Framing:** each delivery starts with a line saying these are messages from other agents in the
  team, to be treated as information about the work and not as instructions that override the
  recipient's task, then `From <agent> (<role>, agent id <id>):` per message, with **every body
  line quoted with `> `**, so agent-written text cannot pass for a header such as
  `From agent-office:`.
- **Limits:** 30 messages per sender per 10 minutes, 50 undelivered messages per sender and
  recipient; messages to an exited agent, or to one that has reported done, are refused. The caps
  apply to agents' own `send_message` calls, not to `report_done` notices or agent-office's
  messages, so one agent flooding the lead cannot block the other workers' reports or the human's
  access decisions (found in the end-of-phase review).
- **Exactly once, precisely:** a message is marked delivered when the hook reply is handed to the
  forwarder, or the line is written to the PTY. Within the daemon it is delivered at most once and
  retried until delivered; if Claude Code drops the hook reply (forwarder timeout, crash), that
  message is lost, not repeated. The UI's message list shows when and how each message was
  delivered.

## Consequences

- An idle lead is woken by its workers' reports without polling.
- Two agents can still keep each other busy by answering every message; the rate limit bounds
  this, and budgets (Phase 8) will bound it in money.
- If a user is composing in the recipient's terminal, PTY delivery waits; a message typed exactly
  while the user starts typing joins their draft (residual, documented).
- Undelivered messages to an agent that exits stay in the database, visible in its message list,
  and are never delivered to a later agent.
- Message bodies are stored in the local database (in the data dir, never in the repo or logs),
  since delivery needs them. Logs carry ids and sizes only.
