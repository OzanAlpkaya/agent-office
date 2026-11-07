# Phase 3 plan: Isolation & security

- **Status:** Approved 2026-11-07; done on `phase-3/isolation`, merged into `main`.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 3. Progress: [PROGRESS.md](../../PROGRESS.md).

This is the plan as approved, followed by the answers to its open questions and the changes made
during the phase. ADR numbers in the plan text are the proposed ones; see "Changes during the
phase" for the final numbering.

## Context

The roadmap says "mount only the worktree", but that can't work. A git worktree's `.git` file points back into the main repo's `.git/`. To make git work inside the container we would have to mount that directory with write access. Then the agent could set `core.fsmonitor`, `core.hooksPath` or hooks, and those would run **on the host** the next time the daemon runs `git status` in that worktree (`internal/engine/engine.go:609`). So I'm proposing a change to the git model below. Everything else follows ADR 0007.

## Goal
Worker agents run Claude Code with permission prompts off, and only inside a rootless Podman container. Inside the container they can't see the host's files, SSH keys, ssh-agent or git credentials, and their only network access is an HTTPS allowlist. That makes "do not push" (`internal/engine/tasks.go:196`) a technical limit rather than just an instruction. Getting commits off the agent is the daemon's job. Phase 3 also closes the ADR 0012 gap first, because Phase 3 ships the second migration.

## Out of scope
- Pushing and PRs (Phase 5). Phase 3 only gets the agent's branch into the main repo, ready to push.
- Stack packs (Phase 6). Until then the allowlist comes from an env var.
- `request_access` (Phase 4), Codex in containers, cache volumes per project, and nesting Claude Code's own sandbox inside ours (I'll only record whether it works).

## Approach
1. **Schema guard (ADR 0013).** `store.Open` compares the database's version (`GetDBVersion`) with the newest migration built into the binary (`ListSources`). If the database is newer, it refuses with a typed `ErrSchemaTooNew` that names both versions and leaves the file untouched. Before applying pending migrations, it writes a backup with `VACUUM INTO agent-office.db.pre-v<N>`. Restoring that backup is the downgrade path, so no down migrations are needed.
   - Rejected: tolerating unknown versions. An older binary's sqlc queries might break at runtime, or write rows that ignore the newer schema's rules.
   - The first test pins what goose v3.29 does today when the database is ahead. I expect it silently does nothing, but I haven't checked.
2. **Git in the sandbox (ADR 0014, amends 0010).** Each agent gets its own clone made with `git clone --shared` and no remote. The main repo's `objects/` is mounted read-only so the clone can read its objects. The clone's own `.git` can be written but is treated as untrusted. **The host never runs git inside the clone.**
   - To check status and dirtiness, the daemon runs `podman exec … git status --porcelain` and treats the output as untrusted data.
   - To collect work, the agent runs `git bundle create` inside the container. On the host, the daemon runs `git bundle verify` and fetches from the bundle into `refs/heads/agent-office/<…>` in the main repo. A bundle is plain data, so no config or hooks from the agent run on the host.
   - Rejected: mounting the main `.git` with write access (the host-execution problem above) and `:O` overlay mounts (commits would be lost when the container ends).
3. **Container (ADR 0007 decisions):** `--read-only --cap-drop=all --security-opt no-new-privileges --userns=keep-id`, plus pids and memory limits. Mounts are only:
   - the clone,
   - the read-only objects directory,
   - the agent's socket,
   - the read-only `agent-office` and `claude` binaries,
   - a config dir for this agent (transcripts stay on the host for Phase 8),
   - tmpfs for `$HOME` and `/tmp`.

   The env is built from an allowlist instead of being scrubbed from the host's env. So there is no `SSH_AUTH_SOCK`, no `GH_TOKEN`, no `~/.ssh` and no `~/.gitconfig`. The daemon still runs `podman run --rm -it` inside its PTY, so the terminal path doesn't change.
4. **Egress over the agent's socket (ADR 0015, amends 0007).** The container runs with `--network none`. A small init process inside it (`agent-office sandbox-init`) listens on `127.0.0.1:3128` and pipes traffic to the agent's unix socket. The daemon runs the allowlist proxy, which only allows CONNECT to port 443.
   - Rejected: the dual-homed proxy container from Spike E. It needs another container, subnet and static IP config, and a shared proxy can't tell agents apart. Because the socket identifies the agent, we get allowlists and denial logs for each agent, which Phase 4 needs.
5. **Secrets.** The same init process asks the daemon for the agent CLI's credential over the socket, once, before it starts `claude` as a child process. The credential never touches disk, `podman inspect` or argv.
   - ADR 0007 picked `podman secret` instead. I'll switch only if the spike confirms that Podman's file driver stores secrets on disk in plain text.
6. **Bypass flag guard.** `--dangerously-skip-permissions` can only be set on the sandbox launch path, and a test checks that host mode can never get it. Host mode stays as an explicit `AGENT_OFFICE_SANDBOX=host` with prompts on, and the UI labels it "unsandboxed". If Podman is missing, the daemon fails loudly and never falls back to host mode on its own.

## Changes
- `internal/store/store.go`: version guard and backup. New migration `0002`: `agents.sandbox` (`host`/`podman`), `agents.container_name`, `worktrees.kind` (`worktree`/`clone`). Then update `queries.sql` and run `make generate`.
- `internal/git`: `CloneShared`, `FetchBundle`, `VerifyBundle`.
- `internal/sandbox`: a podman argument builder (a pure function), run/stop/exec, reconciling orphan containers by label at startup, the allowlist CONNECT proxy, and an image check.
- `cmd/agent-office`: the `sandbox-init` subcommand and new flags/env vars.
- `internal/agent/claudecode.go`: container launch spec (`CLAUDE_CONFIG_DIR`, pre-seeded onboarding/trust state, bypass flag only in this path).
- `internal/engine`: chooses the sandbox mode and handles collect, remove and restart for clones.
- `build/worker/Containerfile` and `make worker-image`.
- `web/`: a sandbox badge on each agent and a host-mode warning.
- Docs: ADRs 0013–0015, notes in 0007 and 0010 saying they are amended, the threat model, `architecture.md`, `.env.example`, `PROGRESS.md`.

## Data / API changes
- Migration 0002 only adds columns with defaults, so existing rows read as `host`/`worktree`.
- `AgentInfo` gains `sandbox`.
- New env vars, names only: `AGENT_OFFICE_SANDBOX`, `AGENT_OFFICE_WORKER_IMAGE`, `AGENT_OFFICE_EGRESS_ALLOW`, `AGENT_OFFICE_SANDBOX_MEMORY` / `_PIDS`, plus the credential's variable name (see question 1).

## Risks & edge cases
- **Running git on the host against data the agent controls.** Covered by the "never run git in the clone" rule plus a test. What remains: you opening the clone in your IDE or shell on the host. I'll document that and point to the branch in the main repo instead.
- **The daemon gets SIGKILLed while containers are running.** Orphans are removed by label on startup, and stopping an agent uses `podman stop` by container name.
- **Claude Code's onboarding, trust and bypass-confirmation state in a fresh config dir is undocumented.** The spike covers it, and agents stuck in `starting` are already surfaced.
- **The allowlist reduces exfiltration but doesn't stop it.** Allowed domains, including the Anthropic API itself, are still a channel. I'll record this in the threat model.
- Each agent starts with cold Go caches, so the first build is slow. Bundles get a size cap.
- The host `claude` binary needs glibc, so the image can't be built `FROM scratch`.

## Test plan
- **Unit tests:** the schema guard (a database at goose version 999 is refused and its bytes are unchanged; a backup is written only when migrations are pending), the podman argument builder (exact mounts, no `-e` with secrets, bypass flag only in sandbox mode), the proxy allowlist, and env allowlisting.
- **Integration tests:** clone, bundle and fetch against temporary repos. A row-survival test for migration 0002.
- **`make sandbox-test` (needs Podman, build tag, not part of `make check`).** Inside a real worker it checks:
  - reading `~/.ssh` or files outside `/workspace` fails, and `SSH_AUTH_SOCK` is unset,
  - `ssh -T git@github.com` and `git push` over HTTPS fail,
  - a domain not on the allowlist gets 403, and direct TCP or DNS is unreachable,
  - the secret is in the agent's env but not in logs, the DB, the repo or `podman inspect`.
- **`make e2e`** on the playground in sandbox mode, including the daemon SIGKILL case.

## Steps (each one a commit with `make check` green)
1. Branch `phase-3/isolation`. Schema guard, backup and tests, plus ADR 0013.
2. Spike F `spikes/sandbox-claude/`: Claude Code in the container (auth, trust/onboarding pre-seed, bypass prompt, hooks over the socket, proxy over loopback-to-unix, which API domains are required, memory needs, where `podman secret` stores data, the bundle round trip). Write up findings.
3. ADRs 0014 and 0015, plus the amendment notes.
4. Git clone and bundle helpers with integration tests.
5. `internal/sandbox`: argument builder and proxy (unit tests), then lifecycle and reconcile.
6. `sandbox-init`, the Containerfile, `make worker-image`.
7. Migration 0002 and the engine and adapter wiring for sandbox mode.
8. `make sandbox-test` isolation suite.
9. UI badge and host-mode warning.
10. e2e in sandbox mode; update the threat model, architecture, `.env.example` and `PROGRESS.md`; write the phase summary.

## Open questions
1. **Claude Code auth in the container:** an API key (`ANTHROPIC_API_KEY`, billed per token) or a subscription token from `claude setup-token` (`CLAUDE_CODE_OAUTH_TOKEN`)? This decides the credential name and the allowlisted domains.
2. **Host mode:** keep it as an explicit unsandboxed option with prompts (my recommendation), or remove it?
3. **Image:** my recommendation is a Debian-slim base pinned by digest, with git and Go, and with `claude` mounted read-only from the host so its version always matches. The alternative is installing `claude` in the image.
4. **Commit identity in the sandbox:** use your git `user.name`/`user.email` passed in as env (my recommendation), or a bot identity?

## Answers to the open questions

1. **Auth:** a subscription token from `claude setup-token` (`CLAUDE_CODE_OAUTH_TOKEN`), not an API
   key. Treated as a long-lived secret: never on disk (beyond the user's own file), in argv, logs or
   `podman inspect`.
2. **Host mode:** kept as an explicit, clearly labelled unsandboxed option with prompts on.
3. **Image:** Debian slim pinned by digest, with `claude` mounted read-only from the host. The
   symlink is resolved and the versioned binary mounted, since Claude Code updates itself by
   switching the link.
4. **Commit identity:** the user's `user.name` / `user.email` as author, plus a `Co-Authored-By`
   trailer naming the agent (`<agent-name> (agent-office)`) with a noreply address.

Added at approval: the default egress allowlist includes `proxy.golang.org`, `sum.golang.org` and
`registry.npmjs.org`, alongside the Anthropic domains the spike requires.

## Changes during the phase

- **Development data dir:** never run the daemon or tests against the real data dir; Makefile and
  test helpers default to scratch dirs (CLAUDE.md rule).
- **Token source (ADR 0015):** the daemon reads the token from a user-only file
  (`~/.config/agent-office/oauth-token`, mode 0600, directory 0700); it is never pasted into chat.
- **Injection instead of socket delivery (ADR 0016):** after Spike F, the token never enters the
  container. The daemon's API socket forwards only `POST /v1/messages`, injects the token, streams
  responses unbuffered, and logs and surfaces every rejected request. Socket delivery stays a
  documented fallback. The Claude Code version is logged at startup with a warning when it differs
  from the tested one.
- **Subordinate uid (ADR 0014):** `--userns=keep-id` dropped, so agent repositories belong to a
  subordinate uid and git run by host tools (prompt, editor, file manager) refuses to work there.
- **Worker image:** includes `ca-certificates` and the playground's toolchain (Go 1.27.3);
  per-project toolchains move to the Phase 6 stack packs. Pinning Debian package versions was added
  to Phase 11.
- **Step 7 split** into 7a (sandboxed start) and 7b (import, removal, save work, restart).
- **Imports never move a branch backwards (ADR 0014):** rewritten history goes to a new
  `<branch>-rewrite-N`; a manual "Import now" exists for running agents, reusable by Phase 4's
  `report_done`.
- **DoD reworded:** "the OAuth token is absent from the agent's env, logs, DB, repo and
  `podman inspect` (tested)", since with injection the token is deliberately not in the agent's env.
- **Steps 8–10:** `GET /api/agents/{id}/events` so sandbox events survive a reload; the sandbox e2e
  covers a daemon SIGKILL and stop; the nested Claude Code sandbox stays untested (ADR 0007).
