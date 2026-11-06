# Spike E: sandbox evaluation (rootless Podman vs Claude Code's built-in sandbox)

Throwaway. Question: what should isolate a worker agent that runs with full permissions?

```sh
./run-podman.sh       # rootless Podman worker + allowlist egress proxy (builds a FROM scratch image; no pulls)
./run-cc-sandbox.sh   # the same kind of probe through Claude Code's Bash tool with sandbox enabled (haiku)
```

Tested 2026-11-06: CachyOS, Podman 6.1.4 (rootless, netavark, cgroups v2 with memory + pids
delegated), bubblewrap present, Claude Code 2.1.319.

## Podman setup under test

- The worker runs on a network created with `--internal --disable-dns`: no route out and no DNS.
- The egress proxy runs in its own container, on the default network plus the internal network
  at a static IP. It allows only `CONNECT host:443` for listed hosts, so traffic stays TLS end to
  end and the proxy never sees request contents.
- The worker runs with `--userns=keep-id --read-only --cap-drop=all --security-opt no-new-privileges
  --pids-limit 128 --memory 256m`. Only the worktree is mounted (`/workspace`), plus the CA bundle
  read-only. The secret comes from `podman secret create` + `--secret ...,type=env`.

## Results

| Check | Podman worker | Claude Code sandbox (Bash) |
|---|---|---|
| HTTPS to allowlisted domain | 200 (via proxy) | 200 (via its proxy) |
| HTTPS to other domain | 403 from proxy | blocked, reported as `sandbox_violations` |
| Bypass proxy (direct TCP / IP) | network unreachable | blocked |
| DNS | none (no resolver reachable) | not tested |
| Write inside workspace / cwd | ok, files owned by host uid 1000 | ok |
| Write outside | read-only rootfs; host fs not mounted | blocked |
| **Read host files** (e.g. home dir) | **not visible** (only `/workspace` exists) | **visible by default** (`~/.ssh` exists check succeeded); needs `denyRead` rules |
| Capabilities | `CapEff=0`, `NoNewPrivs=1` | n/a |
| Secret in env | present (`--secret type=env`) | not tested (Bash inherits the session env) |
| Secret visible in `podman inspect` | **no** with `podman secret`; **yes** with plain `-e` | n/a |
| Reach a host unix socket (mode 0600) via bind mount, with `--network none` | **yes** (`ping` → `pong`) | n/a |
| Covers Read/Edit/Write tools, hooks, MCP servers | **yes**: the whole process tree is inside | **no**: per the docs, Bash/Monitor only. Other tools rely on permission rules; hooks and MCP servers run outside |

## Findings

- **On an `--internal` network with DNS enabled, the network's resolver also answered for the
  dual-homed proxy and refused external names. `--dns` did not override it.** Fix: disable DNS on
  the internal network and use a static proxy IP. This is better anyway, since it removes DNS as an
  exfiltration path.
- **`-e SECRET=...` is readable via `podman inspect`** by anything running as the same user.
  Use `podman secret` (type=env); the secret is then visible only inside the container.
- Rootless mapping with `--userns=keep-id` makes worktree files owned by the host user. Git in
  the host worktree keeps working without `chown`.
- Claude Code's sandbox is a good **inner** layer for Bash commands, but it is not a boundary for
  the agent as a whole: file tools, hooks and MCP servers run outside it, and reads are open by
  default.

## Not yet tested (Phase 3)

- Running Claude Code itself inside the container. That needs an image with the CLI, auth as an
  env var (API key) instead of mounting the host credential store, and the Anthropic API domains on
  the allowlist.
- Full hook/MCP traffic over the mounted socket. The socket path itself is verified above; the
  plan is a small `agent-office hook` / `agent-office mcp` forwarder (see ADRs 0003 and 0006).
- Running the built-in sandbox *inside* the container as defence in depth (needs nested user
  namespaces; may not work with `--cap-drop=all`).
- Non-HTTP egress (git over SSH to GitHub): either an SSH-aware allowlist or pushing from the host.
