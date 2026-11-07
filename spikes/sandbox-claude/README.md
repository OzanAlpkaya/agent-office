# Spike F: Claude Code inside a rootless Podman worker

Throwaway. Question: can a worker run Claude Code with full permissions in a container with **no
network**, **no host credentials**, and its egress, hooks and credential going over unix sockets
the daemon controls? Input for ADRs 0014 (git), 0015 (credential) and 0016 (egress).

```sh
./run.sh build        # spike binary + static agent-office + image (Debian trixie-slim, pinned digest)
./run.sh probe        # isolation checks, no Claude Code
./run.sh version      # mounted claude binary + the real `agent-office hook` forwarder
./run.sh noauth       # claude -p without credential: which domains it contacts (no tokens spent)
./run.sh auth         # claude -p, credential delivered over the agent socket (haiku)
./run.sh inject       # claude -p, credential added by a daemon-side API proxy (haiku)
./run.sh interactive  # interactive session in a PTY, for onboarding/trust/bypass dialogs
```

Output goes to `out/` (gitignored). The harness never prints or logs a credential: logs carry
event names, host names, lengths and booleans only.

Tested 2026-11-07: CachyOS, Podman 6.1.4 rootless (netavark), Claude Code 2.1.319, git 2.56,
base `docker.io/library/debian:trixie-slim@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392`
(Debian 13.7, glibc 2.41).

## Worker setup under test

- `podman run --rm --init --network none --userns=keep-id --read-only --cap-drop=all
  --security-opt no-new-privileges --pids-limit 512 --memory 4g`.
- Mounts: workspace (rw), per-agent Claude config dir (`CLAUDE_CONFIG_DIR=/config`, rw), the agent
  socket and the egress socket, the static `agent-office` binary and the spike binary (ro), the
  hook settings file (ro), and **the resolved, versioned claude binary**
  (`readlink -f $(command -v claude)` → `~/.local/share/claude/versions/<v>`), ro.
- `HOME=/tmp/home` on the default tmpfs; `DISABLE_AUTOUPDATER=1`.
- The in-container init (`spike init`, the model for `agent-office sandbox-init`) bridges
  `127.0.0.1:3128` to the egress socket, fetches the credential over the agent socket when asked,
  then runs the command as a child with the tty inherited. `HTTPS_PROXY=http://127.0.0.1:3128`.
- The host side (`spike host`) serves the agent socket (hook events + one-shot credential) and the
  egress socket (CONNECT, port 443 only, host allowlist).

## Results so far

| Check | Result |
|---|---|
| Host home, `~/.ssh`, `/root/.ssh`, `~/.gitconfig`, `~/.claude`, `~/.config/agent-office` | not visible |
| `SSH_AUTH_SOCK`, `GH_TOKEN`, `GITHUB_TOKEN`, `GIT_ASKPASS`, `ANTHROPIC_API_KEY` | unset |
| Write to rootfs / to `/workspace` | read-only fs / ok |
| DNS; direct TCP to IPv4/IPv6 (incl. github.com:22) | network unreachable |
| Proxy: `github.com:22`, `github.com:443`, `example.com:443`, `api.anthropic.com:80` | 403 |
| Proxy: proxy.golang.org, sum.golang.org, registry.npmjs.org | 200 |
| `git ls-remote https://github.com/...` | fails: `CONNECT tunnel failed, response 403` |
| Mounted claude binary (`--version`) on a read-only rootfs | works; needs only glibc |
| Real `agent-office hook` forwarder → mounted socket | works (`SessionStart` received, exit 0) |
| Hooks from `claude -p` (no credential) | `SessionStart`, `UserPromptSubmit`, `SessionEnd` arrive |
| Egress of `claude -p` without credential | only `api.anthropic.com:443`; with `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`: none |
| `podman secret` (file driver) at rest | value stored **base64, not encrypted** in `<graphroot>/secrets/filedriver/secretsdata.json` (0600) until `podman secret rm` |
| Shared clone (`git clone --shared`, no remote) with the origin's `objects/` mounted ro at the same path | commit inside works; origin objects not writable |
| Hand-off: `git bundle create base..branch` inside, `git bundle verify` + `git fetch <bundle>` on host | works; author and `Co-Authored-By` trailer preserved |
| Agent sets `core.fsmonitor` in its clone; host runs `verify` + `fetch` from the bundle | **not executed** |
| Same clone; host runs `git status` **inside the clone** | **executed on the host** (marker file created) |

## Findings

- **No network at all works.** `--network none` plus a loopback→unix-socket bridge replaces
  Spike E's internal network, static proxy IP and dual-homed proxy container. The daemon sees which
  agent opens each tunnel, because each agent has its own socket.
- **The host must never run git inside an agent's repository.** Git config the agent can write
  (`core.fsmonitor`, `core.hooksPath`, ...) runs commands for whoever runs git there. Bundles are
  inert data: `verify` and `fetch` from a bundle ran nothing. Status checks of the clone have to
  run inside the container (`podman exec`), and their output is untrusted data.
- **Build the mounted `agent-office` with `CGO_ENABLED=0`.** The default `make build` binary is
  dynamically linked against the host glibc. It runs in the image today (needs GLIBC_2.34; the
  image has 2.41), but that is luck: a host-only symbol would break every worker.
- **`podman secret` would put the OAuth token on disk** (base64) in Podman's store. This confirms
  ADR 0015's socket delivery.
- `--shared` clones reference the origin's object store by absolute path, so it is mounted
  read-only at the same path. This reveals one host path name to the agent. Acceptable, and it
  avoids hardlinks (a writable hardlinked object file would let the agent corrupt the origin).

## Pending (need the OAuth token file, ADR 0015)

- `auth`: an authenticated `claude -p` run: required Anthropic domains, the hook sequence,
  one-shot credential delivery.
- `inject`: whether Claude Code works with a placeholder token while a daemon-side proxy adds the
  real one (`ANTHROPIC_BASE_URL`), so the token never enters the container.
- `interactive`: onboarding, workspace trust and `--dangerously-skip-permissions` dialogs in a
  fresh `CLAUDE_CONFIG_DIR`. Which config keys pre-seed them (key names only). Memory use.
- Nested Claude Code sandbox inside the container: record only if cheap.
