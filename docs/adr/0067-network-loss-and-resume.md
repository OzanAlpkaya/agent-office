# 0067. Network loss and resume

- **Status:** Accepted
- **Date:** 2026-11-13

## Context

When the network drops (a VPN toggled, Wi-Fi gone), an agent whose turn ends with "API Error:
Connection lost mid-response" waits at its prompt until someone types into its terminal. With a team of
agents that is several terminals to visit.

Measured with Claude Code 2.1.323 (Spike F, `netcut.sh`: a CONNECT proxy in front of the API proxy that
drops every tunnel): a cut while the answer streams ends the turn at once with a **`StopFailure`** hook,
`error: server_error`, `last_assistant_message: "API Error: Connection lost mid-response. The response
above may be incomplete."`. A cut before the answer makes Claude Code retry about 10 times over 2 min
15 s; a 60 s cut heals by itself; a longer one ends the same way with "API Error: 502 status code (no
body)" (the daemon's API proxy answers 502 when the upstream is unreachable).

## Options considered

1. **Resume on a timer**: type "continue" some time after the failure; if the network is still down the
   turn fails again, and repeated failures fill the conversation.
2. **Watch the network**: wait until the API answers again, then resume once.
3. **Restart the session** with `--resume`: heavier, and the session is alive and fine.

## Decision

Option 2, for Claude Code agents.

- The adapter keeps one boolean: `Event.NetworkLost` on a `StopFailure` whose `error` is `server_error`
  or `unknown` and whose message is an API error about the connection (Claude Code's texts for a cut
  stream, connection errors, a 502/503/504 without a body). The text itself is not kept. Rate limits,
  authentication, billing, overload and invalid requests are not network losses.
- Such an agent shows as **`waiting_network`** ("Connection lost; resumes when the network is back";
  "offline" in the agent list, "Connection lost" on its card). The engine state stays idle underneath.
- One probe loop per engine while an agent waits: `GET https://api.anthropic.com/` every 5 s through the
  API proxy's transport, so `HTTPS_PROXY` (a VPN's proxy, the e2e's cut proxy) applies; no credential;
  any HTTP response means the network is back.
- Then, after 2 s, each agent still waiting gets one line typed into its terminal, like idle message
  delivery (ADR 0018: not while the user types, Enter after a delay): "agent-office: the connection to
  the API was lost and is back now. Continue the task from where you stopped." Any hook first (the user
  typed, the CLI recovered) ends the wait without typing.
- Messages to a waiting agent wait for the resumed turn; the stall watchdog counts the wait as busy.
- At most 5 automatic resumes per agent per hour; then it stays idle and the user's inbox says why.

## Consequences

- A network drop costs the team the outage plus a few seconds; nobody visits terminals.
- The probe checks the daemon's view of the network; a host-mode agent's own route could differ (rare).
- Codex agents are unchanged: their CLI reports errors differently.
- A misclassified server error would make the engine type one line after the API answers; the hourly
  limit bounds it.
- `TestNetworkDropEndToEnd` cuts a sandboxed haiku agent's network mid-answer and checks the resume.
