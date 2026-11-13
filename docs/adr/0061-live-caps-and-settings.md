# 0061. Agent caps and settings that change while agents run

- **Status:** Accepted
- **Date:** 2026-11-12

## Context

Running Bouncibo, the user wanted more agents for a goal while its team was running. Three things got in
the way. A project's Max agents setting reopened the project's engine, and that was refused while any
agent ran. The daemon-wide cap (`--max-agents`, ADR 0047) could only change with a restart. And the UI did
not say which cap a start meets, or that the Tech Lead, the Architect and QA take slots too.

## Options considered

1. **Reopen less often:** keep one engine, but rebuild its options on every change under a lock and swap
   them. Every read of `e.opts` would need the lock.
2. **Split the settings:** settings read when an agent starts or a team tool runs live behind an atomic
   pointer the engine loads at each use; settings the engine is built from (pack image, egress proxy,
   secrets, the GitHub client, role CLIs) still reopen it.
3. **Allow the reopen with agents running:** the engine owns the agents' PTYs, sockets and containers,
   so this means a hand-over between engines. Too much for the gain.

## Decision

Option 2.

- `liveOptions` (`internal/engine/live.go`) holds `max_agents`, `max_sub_tasks`, `model`, the roles with
  `role_models`, `codex_model`, budgets, `access_decider`, `lead_access_hosts`, `merge_target`, and the
  workers' memory and pids limits. Every reader loads the pointer; a change stores a new value. Running
  agents keep what they started with, and the next start uses the new values.
- `Hub.Update` compares the effective settings. If only live fields (or the office layout, ADR 0057) change,
  it checks them (roles, the budget models through `HubOptions.Check`) and applies them to the open
  engine, with agents running. If `pack`, `secrets_dir`, `egress_allow`, `github`, `github_remote`,
  `reviewer`, `review_rounds` or `role_clis` change, the project reopens as before: refused while agents
  run, and the refusal names those fields.
- The daemon-wide cap is the slot pool's `max`, read under its lock at each start. `PUT /api/daemon
  {max_agents}` (Settings → Projects) changes it at once and saves it in `daemon_settings`, so it holds
  after a restart; a saved value wins over `--max-agents` (both are logged at start), and `null` goes
  back to the flag. Lowering it stops nobody; new starts wait.
- A project's cap is no longer clamped when it opens: each start meets `min(project, daemon)`. The UI shows
  this as "Effective: K (the smaller of this project's N and the daemon's M). The Tech Lead, Architect and
  QA count too." under Max agents, and as "running / effective" over the agent list. The refusal says the
  same. Engines publish `caps` updates, and the hub publishes `daemon` updates.

## Consequences

- Max agents, models and budgets can be tuned in the middle of a goal; nothing restarts.
- A new live setting must be read through `e.lv()`, never `e.opts`. Tests that changed `e.opts` after
  start use `setLiveForTest`.
- The project settings form says, before saving, when a change needs the agents stopped (`needsStop`
  mirrors `ReopenChanges`).
- ADR 0047's "the project cap is clamped at open" is replaced by the check at each start.

## Amendment (Phase 19 review, 2026-11-13): the daemon-wide cap is this machine's

Ozan's review: the daemon-wide agent cap decides how many agents run on this machine, so it is changed from
this machine only, like the sandbox settings (ADR 0046). `PUT /api/daemon` from a remote device (through
`tailscale serve`, ADR 0038) is refused with 403 and the reason. On a remote device, Settings → Projects shows
the cap without Save and says so. A project's settings, Max agents included, stay changeable from any
paired device.
