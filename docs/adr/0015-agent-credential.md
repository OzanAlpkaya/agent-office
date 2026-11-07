# 0015. Agent credential: a user-only token file, delivered over the agent socket

- **Status:** Accepted; the delivery part is superseded by ADR 0016 (credential injection on the
  API socket). Socket delivery below is kept as the documented fallback.
- **Date:** 2026-11-07
- **Amends:** ADR 0007 (secrets via `podman secret`)

## Context

Sandboxed workers run Claude Code in a container (ADR 0007). They cannot use the host's credential
store, so Claude Code authenticates with a token passed in `CLAUDE_CODE_OAUTH_TOKEN`. Ozan chose a
**subscription token** created with `claude setup-token`, not an API key (2026-11-07).

That token is long-lived and stands for Ozan's Claude account. Requirements (decided by Ozan):

- Ozan creates and stores it himself. It is never pasted into a chat with an assistant.
- It is never on disk outside that one file, and never in argv, logs, the database, the repo,
  or `podman inspect`.

ADR 0007 planned `podman secret` (type=env). Spike E showed that `podman inspect` does not show such a
secret. But Podman's default file driver keeps secrets in its own store on disk (to be confirmed in
Spike F), so the token would end up in a second file that agent-office does not control.

## Options considered

**Where the daemon gets the token**

1. **Env var in the daemon's environment.** Cons: ends up in shell profiles or history; inherited by
   every child unless scrubbed; readable via `/proc/<pid>/environ` for the daemon's lifetime.
2. **A token file readable only by the user.** Pros: one well-known place, standard Unix
   permissions, easy to rotate (replace the file), nothing to install. Cons: plaintext at rest,
   protected only by file permissions and disk encryption, like `~/.ssh` keys.
3. **The OS keyring (Secret Service over D-Bus).** Pros: encrypted at rest. Cons: a new
   dependency, needs a running keyring daemon and an unlocked session, awkward headless. Possible
   later as an alternative source behind the same reader.

**How the token reaches the agent**

1. **`podman secret` type=env.** Cons: copies the token into Podman's secret store on disk.
2. **`-e CLAUDE_CODE_OAUTH_TOKEN=...`.** Cons: visible in `podman inspect` and in the podman process's
   argv (Spike E).
3. **Over the per-agent unix socket.** The in-container init (`agent-office sandbox-init`) asks the
   daemon for the credential once, before starting `claude`. Pros: no extra copy on disk, nothing in
   argv or container config. It reuses the socket identity model (ADR 0003): only that agent's
   socket can ask for it. Cons: needs the init process (it is also needed for the egress bridge).
4. **Credential-injecting API proxy.** The container never holds the token. Claude Code talks to
   the daemon (`ANTHROPIC_BASE_URL`), and the daemon adds the credential. Pros: a prompt-injected
   agent cannot read or exfiltrate the token. Cons: depends on how Claude Code sends OAuth
   credentials and whether it starts without one. Undocumented, so Spike F checks whether it is
   feasible.

## Decision

Source option 2 and delivery option 3:

- **Path:** `$XDG_CONFIG_HOME/agent-office/oauth-token`, by default
  `~/.config/agent-office/oauth-token`. `AGENT_OFFICE_OAUTH_TOKEN_FILE` can point elsewhere (it holds
  a path, never the token).
- **Permissions, checked by the daemon on every read** (`sandbox.ReadTokenFile`):
  - The file is a regular file, not a symlink (opened with `O_NOFOLLOW`; checks run on the opened
    file), owned by the user, mode `0600` or `0400`.
  - Its directory is owned by the user and not writable by group or others (`0700` recommended).
  - At most 4 KiB. Exactly one token, with no inner whitespace or control characters.
  - Errors name the file and the problem, never the contents.
- **Creation (done by Ozan, without the token touching shell history):**
  ```sh
  install -d -m 0700 ~/.config/agent-office
  claude setup-token                                   # prints the token in your terminal
  (umask 077; cat > ~/.config/agent-office/oauth-token) # paste the token, Enter, then Ctrl-D
  chmod 600 ~/.config/agent-office/oauth-token
  ```
- The daemon reads the file **when an agent is launched**, not at startup. A replaced token takes
  effect for the next agent, and the value is not kept in memory between launches. In memory it
  is a `sandbox.Secret`, which prints and logs as `[redacted]`.
- **Delivery:** the socket answers a credential request **once per container start** and only
  before the session's first hook event. `sandbox-init` puts the token into `claude`'s environment
  only: never argv, never a file, never a mounted path. The host's `~/.claude` credentials are
  never mounted.
- Spike F showed that option 4 works; ADR 0016 switches to it. The token file and its checks
  stay the same.

## Consequences

- Host mode is unaffected: host agents use Claude Code's normal login.
- **With socket delivery, the agent can read its own credential** (it is in its process
  environment). Under ADR 0016 this applies only if the fallback is enabled. A prompt-injected
  agent could try to exfiltrate it through an allowlisted domain. The egress allowlist narrows this
  but cannot close it, because TLS hides request contents from the proxy. This is a known
  residual risk in the threat model. It is the main reason to evaluate option 4, and the reason to
  rotate the token if an agent is suspected of misbehaving.
- The token is plaintext at rest in one file. Its protection is file permissions plus disk
  encryption.
- Assistants working in this repo never read the token file; only the daemon (and the Spike F
  harness) open it, and neither prints it.
