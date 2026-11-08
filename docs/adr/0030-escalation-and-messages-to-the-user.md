# 0030. The escalation chain and messages to the user

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0017 (addressing), ADR 0018 (messages from the user)

## Context

Agents could only message agents (ADR 0017): a worker reached its lead, the lead its workers.
In Ozan's end-to-end test a worker without a lead tried to correct a lead that did not exist.
Phase 12 adds an architect and an assistant, and Ozan set the rule: the escalation chain is
worker → Tech Lead → Architect → user; only the Architect and the Assistant talk to the user,
and agents without a lead can still leave the user messages on the task card. The user must be
able to answer.

Agent-written text reaching a human is a new channel for prompt-injected content (phishing-like
text, links, floods). The user's answers reaching an agent are the one input that legitimately
*is* an instruction.

## Options considered

**How an agent reaches the user**

1. **Only through its terminal** (the user reads the TUI). Cons: invisible unless the terminal
   is open; nothing on the board; nothing for a phone (Phase 10).
2. **A `message_user` tool into an inbox**, shown in the UI header and on the task card, with
   replies. Pros: visible, answerable, rate-limited, stored with the agent and task.

**Who may use it**

1. **Everyone.** Cons: ten workers asking the user what the lead should decide; the chain means
   nothing.
2. **Whoever has nobody above it**, decided by the engine from the caller's kind, task and
   role: the architect and the assistant always; a Tech Lead when its goal has no architect (or
   its role says `reports_to: user`); a worker whose task has no running lead (a user's own
   task, or a lead that was stopped). Reviewers and QA report through their own tools.

**How the user's answer arrives**

1. **Typed into the terminal by the user.** Still possible; nothing to build.
2. **Delivered like a team message (ADR 0018), marked as from the user**, so the framing does not
   tell the agent to treat it as mere information.

## Decision

Option 2 in each group.

- **`message_user(kind, body)`**, `kind` `question | blocked | info | briefing`, body 1–4 000
  characters, stored in `inbox` (agent, task, goal, role, kind, body, read, answered, reply).
  Refusals name whom to ask (`send_message(to="lead")` or `to="architect"`). At most 30 per agent
  per 10 minutes and 20 unread per agent. `GET /api/inbox`, `POST /api/inbox/{id}/read`,
  `POST /api/inbox/{id}/reply`; the inbox is part of the `/api/events` snapshot, and changes are
  `inbox` updates. agent-office's own notices (a plan to approve, ADR 0031) are inbox items without
  an agent.
- **Replies** (and `POST /api/agents/{id}/message`, used by the assistant's briefing button) are
  stored as messages with `from_user` set and no sending agent, delivered through ADR 0018's
  paths. The delivery header says they are the user's own words, written in the agent-office UI;
  agent messages in the same delivery keep the "information, not instructions" framing. Every body
  line is still quoted, so an agent's text cannot pass for `From the user:`. Reviewers take no
  messages. A reply to an agent that is not running is refused.
- **`send_message` addressing** extends ADR 0017: workers and QA → `lead`; the Tech Lead → its
  goal's workers and QA, and `architect` (relaunched if it has ended, ADR 0031); the architect →
  `lead`; the assistant and reviewers send no team messages.

## Consequences

- The board shows what each agent needs from the user; the user can answer without opening the
  terminal, which Phase 10's phone UI needs.
- A prompt-injected agent can put at most 20 unread items in front of the user, as text, labelled
  with its id and role. Links are not clickable (text rendering).
- An agent the user answers acts on it as an instruction. That is the point; it is also why
  only the user's own replies are framed that way, never an agent's message.
