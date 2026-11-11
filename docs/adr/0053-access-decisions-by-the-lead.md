# 0053. Access decisions by the Tech Lead, scoped grants that outlive a session

- **Status:** Accepted
- **Date:** 2026-11-11
- **Amends:** ADR 0019

## Context

ADR 0019 made every network access request the user's decision, granted to the asking agent's
running session only. In the PulseGo run that meant relaying routine requests (a module mirror)
while the Tech Lead could have decided, and request #4 (`storage.googleapis.com`), approved, was
not honoured after the worker was resumed: the grant lived in the session's in-memory set
(`sandboxRes.grants`, new for every launch), so the resumed session started without it. The agent's
exit also expired its other pending requests.

## Options considered

1. **Keep the user as the only decider**, fix only the resume bug.
2. **Let the lead decide everything.** Widens what a prompt-injected worker can talk its lead into.
3. **A per-project choice**: `user` (default), `lead`, or `lead_policy` (the lead may approve only
   hosts in a list the user wrote; anything else stays the user's).

## Decision

Option 3, plus stored grants.

- **`access_decider`** and **`lead_access_hosts`** are project settings (`project set
  --access-decider ... --lead-access-hosts ...`, the Projects panel). They widen the sandbox, so the
  API accepts them from this machine only (ADR 0046). A request the lead decides goes to it as a
  message naming the `decide_access` call; the user can still decide first in the UI.
- **`decide_access`** (Tech Lead only): a request of a worker of its own goal, never its own; a reason
  is required; with `lead_policy`, an approval outside the list is refused by the engine (a denial
  is always allowed). The decision records `decided_by = lead`, the lead's agent id, the reason and
  the scope (migration 0015 rebuilds `access_requests` for the new values), and becomes an inbox
  item for the user.
- **Scopes and grants.** An approval is a row in `access_grants`: `agent` (the asking agent, every
  later session, and the later workers on its sub-task: one taken back and assigned again continues
  the same work; found by the e2e run, where the new worker was refused the host) or `project` (every
  agent of the project). It reaches the running sessions it
  covers at once and every later launch loads it. Pending requests stay open while their agent can
  be resumed (an approval then applies to its next session) and expire when it is removed or cannot
  be resumed. That replaces 0019's expiry on exit and at daemon start.

## Consequences

- Request #4 holds after a resume: `TestSandboxedGrantSurvivesResume` (real Podman) fails with the
  launch's grant loading taken out ("the approval did not reach the resumed session") and passes
  with it; `TestUserDecidesByDefault` checks the stored grant without Podman.
- With `lead`, a persuasive worker can get any one host approved for its project; `lead_policy`
  bounds that to the user's list. Both are off by default.
- Project-wide grants are not revocable from the UI yet; removing the project's agents does not
  remove them. Backlog.
- Tested: `TestLeadDecidesAccess`, `TestUserDecidesByDefault`, `TestAccessRequests`,
  `TestRestartKeepsAccessRequests`, `TestProjectSettingsParseAndValidate`; live in
  `TestAutonomousTeamEndToEnd`.
