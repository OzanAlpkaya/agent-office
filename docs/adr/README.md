# Architecture Decision Records

Every significant decision, with the options that were considered and why one won. Numbered in the
order they were made; grouped here by topic. New ones start from [0000-template.md](0000-template.md).

**Start here** (the decisions that shape the system most):
[0007](0007-sandbox-approach.md) sandbox model ·
[0016](0016-sandbox-network-and-api-injection.md) no network, credential injection ·
[0014](0014-git-in-the-sandbox.md) git bundle hand-off ·
[0029](0029-roles-as-data.md) roles as data ·
[0017](0017-team-roles-and-authority.md) authority from the socket ·
[0039](0039-paired-devices.md) paired devices.

## Foundations

| ADR | Decision |
|---|---|
| [0001](0001-go-for-the-daemon.md) | Go for the daemon: I/O-bound concurrency over long-lived streams, one static binary |
| [0004](0004-persistence-sqlite.md) | SQLite (pure-Go modernc) with sqlc and goose |
| [0005](0005-ui-embedding.md) | React + Vite UI embedded with `go:embed` |
| [0009](0009-git-via-cli.md) | git through the git CLI, not a library |
| [0012](0012-forward-only-migrations.md) | Forward-only migrations |
| [0013](0013-schema-version-guard.md) | Refuse newer schemas; back up before migrating |
| [0042](0042-releases.md) | Releases: a clean tag, a deterministic tarball, uploaded by hand |

## Agents, terminals and state

| ADR | Decision |
|---|---|
| [0002](0002-terminal-transport.md) | PTY + WebSocket; sessions outlive browser tabs |
| [0003](0003-hook-ingestion.md) | Claude Code hooks through a forwarder on a per-agent unix socket |
| [0008](0008-interrupt-inference.md) | Infer interrupts from terminal input (no hook for them) |
| [0010](0010-worktree-layout.md) | One worktree and branch per agent; removal never loses work |
| [0011](0011-data-model-and-restart.md) | Data model and what a restart means |
| [0035](0035-resume-and-restore.md) | Resume conversations; restore agents after a restart |
| [0036](0036-session-history.md) | Every session's terminal output saved |

## Isolation and security

| ADR | Decision |
|---|---|
| [0007](0007-sandbox-approach.md) | Rootless Podman per worker; Claude Code's sandbox at most as an inner layer |
| [0014](0014-git-in-the-sandbox.md) | A shared clone per agent; work handed back as a verified git bundle |
| [0015](0015-agent-credential.md) | The OAuth token in a user-only file |
| [0016](0016-sandbox-network-and-api-injection.md) | `--network none`; egress allowlist and an API socket that injects the token |
| [0049](0049-codex-agents-and-their-credential.md) | Codex agents, sandboxed, with a placeholder ChatGPT login and the real token added outside |
| [0019](0019-access-requests.md) | Network access requests approved by a human, per agent |
| [0041](0041-reproducible-worker-images.md) | Worker images from a pinned Debian snapshot |
| [0038](0038-remote-access-tailscale-serve.md) | Remote access: the loopback daemon behind `tailscale serve` |
| [0039](0039-paired-devices.md) | Every request from a paired device; one-time codes, revocable tokens |

## The team

| ADR | Decision |
|---|---|
| [0006](0006-mcp-transport.md) | Team MCP server over a stdio bridge to the per-agent socket |
| [0017](0017-team-roles-and-authority.md) | Authority comes from the socket an agent calls on, never from arguments |
| [0018](0018-message-delivery.md) | Messages claimed once in SQLite, delivered on Stop or typed when idle |
| [0029](0029-roles-as-data.md) | Roles as YAML over six engine behaviours; a model per role |
| [0050](0050-agent-cli-per-role.md) | The agent CLI (Claude Code or Codex) per role, overridable like the model; usage per vendor |
| [0051](0051-exit-reasons-and-self-recovery.md) | Exit reasons in plain words (OOM from sandbox-init's report), one automatic resume, saved work on exit, the lead resumes, stops and reclaims its workers, messages wait for a resume |
| [0052](0052-stall-watchdog.md) | A stall watchdog in the daemon wakes the Tech Lead of a goal that does not move |
| [0053](0053-access-decisions-by-the-lead.md) | Access decisions by the Tech Lead per project (optionally within a host policy); grants scoped to the agent or project that outlive a session |
| [0054](0054-goal-archive-and-closing.md) | The board shows active goals; marking a goal done closes its sub-tasks and agents; done goals go to an archive with their record |
| [0055](0055-merge-and-push-from-the-goal.md) | Merge and Push on the goal card: the user's confirmed squash merge of QA-passed work, and its push |
| [0030](0030-escalation-and-messages-to-the-user.md) | Escalation chain worker → Tech Lead → Architect → user |
| [0031](0031-architect-and-specs.md) | The architect's plan: approved by the user, committed as a spec |
| [0032](0032-dependencies-and-conflicts.md) | Dependencies, file ownership, `merge-tree` conflict checks |
| [0033](0033-qa-and-the-integration-branch.md) | QA integrates the branches and gates the goal |
| [0034](0034-assistant-and-project-memory.md) | Assistant briefings; project memory only once the user accepts it |
| [0037](0037-relevant-memory.md) | Memory scoped to roles and files |

## GitHub and review

| ADR | Decision |
|---|---|
| [0020](0020-github-integration.md) | Token file, the daemon pushes over SSH, polled PRs, push gates |
| [0021](0021-pluggable-review.md) | One `Reviewer` interface: an internal reviewer agent or CodeRabbit |
| [0022](0022-review-follow-ups.md) | Review feedback relaunches the original worker, bounded rounds |

## Stacks, cost and observability

| ADR | Decision |
|---|---|
| [0023](0023-stack-packs.md) | Stack packs: user-controlled YAML for image, egress, secrets, skills, MCP |
| [0026](0026-token-usage-and-cost.md) | Usage metered on the API socket or read from transcripts; priced on read |
| [0027](0027-budgets.md) | Budgets per session and goal, refused on the API socket |
| [0028](0028-correlation-logs-retention.md) | Session id as correlation id; log format; retention |

## UI

| ADR | Decision |
|---|---|
| [0024](0024-office-view.md) | The office: a lazy-loaded PixiJS scene, art drawn in code |
| [0025](0025-ui-test-tooling.md) | Vitest for logic, Playwright against a mocked daemon |
| [0045](0045-office-floor-plan-and-errands.md) | Office v2: rooms, routes on a walkability grid, errands derived from what the UI sees |
| [0040](0040-pwa-and-phone-ui.md) | PWA, phone layout, terminal keys and clipboard |
| [0043](0043-agent-names.md) | A first name per agent, chosen and stored by the daemon |
| [0044](0044-agent-switcher-and-approvals-drawer.md) | Agent switcher, open tabs, approvals drawer |
| [0048](0048-ui-across-projects.md) | The UI across projects: one shown, the others watched lightly, a floor per project |

## Projects

| ADR | Decision |
|---|---|
| [0046](0046-projects-and-the-hub.md) | One engine per project under a hub; project settings override the daemon's flags |
| [0047](0047-daemon-wide-agent-cap.md) | A daemon-wide agent cap across projects, with an optional cap per project |
