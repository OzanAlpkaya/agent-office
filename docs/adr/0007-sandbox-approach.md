# 0007. Isolation: rootless Podman per worker; Claude Code's sandbox at most as an inner layer

- **Status:** Accepted; implemented in Phase 3 (2026-11-07). Claude Code's sandbox nested inside
  the worker is still untested: its prerequisite (nested user namespaces) holds (Spike F).
- **Amended by:** ADR 0023 (one base image plus stack pack images instead of one worker image), ADR 0015 (credential source, not `podman secret`), ADR 0016 (`--network none`
  with per-agent egress and API sockets instead of an internal network and a proxy container;
  credential injection), ADR 0014 (git in the sandbox)
- **Date:** 2026-11-06

## Context

Worker agents should run with full permissions (no approval prompts), which is only acceptable if
they cannot harm the host: no access to host files beyond their worktree, no network beyond an
allowlist, and secrets they cannot leak through side channels. Evidence: `spikes/sandbox`.

## Options considered

1. **Rootless Podman container per worker.** Tested: internal DNS-less network plus an allowlist
   CONNECT proxy (allowed 200, denied 403, direct TCP/IP/DNS unreachable); read-only rootfs; only
   the worktree visible; `CapEff=0`, `NoNewPrivs=1`; worktree files owned by the host user
   (`--userns=keep-id`); `podman secret` env not visible in `podman inspect`. It covers the whole
   process tree: the CLI, all its tools, hooks and MCP servers.
2. **Claude Code's built-in sandbox** (bubblewrap + proxy on Linux). Tested: network allowlist and
   write restrictions work for Bash. But **reads of the host are open by default** (`~/.ssh` was
   visible), and per its docs it covers only Bash/Monitor. Read/Edit/Write rely on permission
   rules, and hooks and MCP servers run outside. Claude-only.
3. **bubblewrap directly, managed by the daemon.** Pros: lighter than containers. Cons: we would
   rebuild image, network and secret handling that Podman already provides.
4. **VMs (Firecracker / microVMs).** Pros: strongest isolation. Cons: heavy for a local
   multi-agent tool; out of scope.

## Decision

Option 1 is the security boundary. Each worker container:
- runs on a per-project `--internal --disable-dns` network. Egress goes only through the
  allowlist proxy at a static IP (`HTTPS_PROXY`), CONNECT to port 443 only. The allowlist comes
  from stack packs (Phase 6) plus the agent CLI's API domains.
- runs with `--read-only --cap-drop=all --security-opt no-new-privileges --userns=keep-id`, plus
  pids and memory limits. *(Revised in ADR 0014: no `keep-id`; workers run as container uid 1000,
  a subordinate id on the host, so host tools refuse to run git in agent repositories.)*
- mounts only its worktree, its agent socket, and the `agent-office` binary (read-only).
- gets secrets through `podman secret` (type=env), never plain `-e`.
- runs the agent CLI with its permission prompts disabled, **only inside the container**.

Claude Code's built-in sandbox may be added later as defence in depth, if it works nested in the
container. It is not relied on.

## Consequences

- Phases 1–2 run agents on the host **without** bypassing permissions. Full-permission mode is
  only offered when the sandbox is available.
- A worker image with the agent CLI is needed, and the CLI must authenticate with an API key from
  a secret, not by mounting the host's credential store.
- Git over SSH from inside the container is not covered by an HTTPS proxy. Default: the daemon
  pushes branches from the host; agents only commit locally.
- The internal subnet and the proxy IP become config values (documented in `.env.example`).
