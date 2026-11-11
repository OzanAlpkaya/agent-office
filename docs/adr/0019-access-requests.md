# 0019. Access requests: human-approved, per-agent egress grants

- **Status:** Accepted; amended by ADR 0053 (the Tech Lead may decide per project; approvals are stored grants for the agent or the project; pending requests stay open while their agent can be resumed)
- **Date:** 2026-11-07

## Context

Sandboxed workers reach the network only through their egress proxy's allowlist (ADR 0016). When
an agent needs a host that is not listed (a documentation site, another package registry), it is
stuck: the request is refused and shown as a sandbox event, and the only fix today is restarting
the daemon with `AGENT_OFFICE_EGRESS_ALLOW`. Phase 4's `request_access` lets the agent ask. The
threat model row "Agent escalates its own permissions" requires that a human approves and that
deterministic code makes the change.

## Options considered

**What can be requested**

1. **Network domains only, in Phase 4.** Pros: the one access the sandbox actually restricts at
   run time, with a natural grant point (the per-agent egress proxy). Cons: secrets and paths wait.
2. **Domains, secrets (env var names) and paths.** Cons: secrets need a way to pass a value into a
   container that ADR 0015/0016 deliberately avoid; paths need new mounts in a running container,
   which Podman cannot add. Both belong with the stack packs (Phase 6).

**What an approval grants**

1. **That running agent only, until it exits.** Pros: least privilege; nothing persists by
   surprise. Cons: the next agent has to ask again.
2. **The project's allowlist, permanently.** Pros: ask once. Cons: one approval silently widens
   every future agent's network; that is a configuration change and belongs in the stack pack.

## Decision

Option 1 in both groups.

- `request_access(kind, value, reason)`: `kind` must be `domain`; `value` is an exact host name
  (lower case, at least two labels, no IP literal, port, wildcard or `.suffix`); `reason` is 1–1 000
  characters. The schema (migration 0004) already allows the kinds `secret` and `path` for later.
- The engine refuses requests from host-mode agents (they are not network-restricted), for hosts
  already allowed, and beyond 5 pending requests per agent; a duplicate pending request returns
  the existing one.
- A request is a row in `access_requests` with status `pending` and an update for the UI. **Nothing
  is granted by creating it.** The approval queue in the UI shows the agent, the host and the
  agent's reason (rendered as text: it is written by the agent).
- `POST /api/access-requests/{id}/approve` or `…/deny` (state-changing, so the API's origin guard
  applies) decides it with a compare-and-swap from `pending`, so a request is decided once. An
  approval adds the host to the agent's **grant set**, which its egress proxy checks next to the
  allowlist; it applies to new connections from that agent only and disappears when the agent
  exits. Approving a request whose agent is no longer running is refused and the request expires.
- The agent gets a message (ADR 0018) from agent-office with the decision, so it knows when to
  retry.
- Pending requests expire when their agent exits and at daemon start (all agents of a previous
  run are gone, ADR 0011).

## Consequences

- A prompt-injected agent can ask for an exfiltration host, but only a human can grant it, and the
  reason text it gives is shown, not trusted. The UI says the grant is for this agent only.
- Grants are not persisted beyond the agent's run; the row records who asked for what and the
  decision, which Phase 6 packs can learn from.
- Host-mode agents get a clear refusal instead of a request that would do nothing.
