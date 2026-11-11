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

## Authenticated runs (OAuth token from `claude setup-token`, ADR 0015)

| Check | Result |
|---|---|
| `auth`: token over the agent socket, `claude -p` (haiku) | works; identical fingerprint on both sides; hooks `SessionStart → UserPromptSubmit → Stop → SessionEnd` |
| Egress of `auth` | `api.anthropic.com` only; one denied `http-intake.logs.us5.datadoghq.com` (log shipping) |
| Egress of `auth` with `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` | 3 tunnels, all `api.anthropic.com` |
| `inject`: container holds a placeholder, `ANTHROPIC_BASE_URL` → daemon proxy adds the real token | **works**; Claude Code sends `Authorization: Bearer <placeholder>`, no `x-api-key` |
| Egress of `inject` with the non-essential flag | **no CONNECT tunnels at all**; the only API path is `POST /v1/messages` |
| Egress of `inject` without the flag | background calls go direct via CONNECT to `api.anthropic.com` with the placeholder only |
| First interactive start, fresh config | connectivity check to **`platform.claude.com`**; exits ("Unable to connect to Anthropic services") if denied |
| Dialogs on a fresh config (interactive, bypass mode) | 1. theme 2. security notes 3. workspace trust (`/workspace`, "No, exit" preselected) 4. bypass-permissions warning ("No, exit" preselected). No login screen with a token in the env |
| Where the answers are stored | `.claude.json`: `hasCompletedOnboarding`, `lastOnboardingVersion`, `projects["/workspace"].hasTrustDialogAccepted`; settings: `theme`, `skipDangerousModePermissionPrompt` |
| Pre-seeded config (those 3 keys) + the 2 settings keys in the daemon's `--settings` file | **no dialogs**, straight to the prompt; no `platform.claude.com` check; `SessionStart` after ~0.4 s |
| Remaining denied egress, interactive | `downloads.claude.ai`, `github.com` (official plugin marketplace auto-install); harmless |
| Tool use in bypass mode | no prompts; `PreToolUse/PostToolUse(Bash)` hooks; files owned by the host user |
| Memory / processes during a session | ~162 MB, 29 PIDs |
| Nested user namespaces (prerequisite for Claude Code's own bubblewrap sandbox) | allowed even with `--cap-drop=all`; the sandbox itself not tested (needs bubblewrap + socat in the image) |

**Re-check with Claude Code 2.1.322 (2026-11-11, Phase 14 review):** `./run.sh interactive` with the
pre-seed (`lastOnboardingVersion` 2.1.322) and `FLAG_SETTINGS=1`: no dialog, straight to the prompt
in bypass mode, `SessionStart` after 0.3 s. Controls, one key left out each: without
`hasTrustDialogAccepted` the trust dialog ("No, exit" preselected); without the settings keys the
bypass-permissions warning ("No, exit" preselected); without `hasCompletedOnboarding` and
`lastOnboardingVersion`, or with no seed at all, the welcome/onboarding screen. Same keys, same
dialogs as 2.1.319. Without `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` the CLI also tried
`raw.githubusercontent.com` (denied); the daemon sets that variable, and a full `make e2e` with
2.1.322 saw only the known `downloads.claude.ai` and `github.com` denials.

## Findings (authenticated)

- **The token does not have to enter the container.** With a placeholder token, `ANTHROPIC_BASE_URL`
  pointing at a loopback bridge to a per-agent API socket, and the non-essential traffic flag, the
  daemon adds the credential to `POST /v1/messages` and nothing else leaves the container for
  Anthropic. A prompt-injected agent can still spend inference through that proxy (as it does
  anyway), but it cannot read or exfiltrate the token. `api.anthropic.com` then does not need to be
  on the CONNECT allowlist at all. Needs `NO_PROXY=127.0.0.1,localhost`, otherwise Claude Code
  sends the plain-HTTP API request into the CONNECT proxy (405).
- **Socket delivery (ADR 0015 as written) also works**, as a fallback if injection breaks with a
  future Claude Code version.
- **Pre-seeding is required for interactive workers.** Without it the trust and bypass dialogs
  block with "No, exit" preselected, and a fresh config needs `platform.claude.com`. Keys are
  undocumented and version-dependent (`lastOnboardingVersion` is the CLI version), so the
  daemon keeps the stuck-in-`starting` detection.
- `setup-token` tokens have only the `user:inference` scope: bootstrap is skipped ("403 for OAuth
  token without profile scope"); nothing in a session needed more.
- A pasted token picked up a stray ESC byte once. It showed up only as API 401s and a misleading
  `400 (no body)`. The daemon's reader now rejects anything outside printable ASCII and names the
  offset.
- Resource limits of 2 GB memory and 256 PIDs leave ample headroom for one session. Builds and
  test runs inside the worker will need more; make them configurable.

## Not done

- Claude Code's built-in sandbox nested inside the worker (prerequisite met, test deferred).
- Long sessions: token refresh behaviour, many tool calls, subagents. Covered by the Phase 3 e2e run.

## Addendum: agent repository owned by a subordinate uid (`./subuid.sh`)

Question: host tools (shell prompt, editor, file manager) also run git in folders they open, and
the daemon's rule cannot bind them. Does running the worker **without `--userns=keep-id`**, as
container uid 1000 (host: subordinate uid 100999), let git's `safe.directory` check protect them?

| Check | Result |
|---|---|
| Clone after `podman unshare chown -R 1000:1000` | owned by `100999:100999` on the host |
| Agent commit inside, main objects via read-only alternates (umask 022 repo) | ok |
| Host `git status` / `git log` / git inside `.git`, with a planted `core.fsmonitor` | refused ("detected dubious ownership"); **trap not run** |
| `starship prompt` in the clone (default and the user's config) | trap not run |
| **Control:** same clone owned by the host user (keep-id style), host `git status` | **trap ran** |
| Control: `starship prompt` in the user-owned clone | trap not run (starship 1.27 is not a vector here) |
| Bundle from a helper container to stdout, import on the host | ok |
| Socket 0600 owned by the host user, used by container uid 1000 | not writable |
| Same socket after `podman unshare chown 1000:1000` | writable (still 0600) |
| Host `rm -rf` of the clone / `podman unshare rm -rf` | permission denied / ok |
| Host user reads files the agent wrote | yes (container umask 022) |

Decision recorded in ADR 0014. VS Code's and Dolphin's git integrations were not tested (no
headless way; Dolphin's plugins are not installed on this machine).
