# 0016. Sandbox network: no network, per-agent egress and API sockets, credential injection

- **Status:** Accepted; Claude Code's background requests amended in Phase 17 (2026-11-11)
- **Date:** 2026-11-07
- **Amends:** ADR 0007 (internal network + shared proxy container); **supersedes the delivery part
  of ADR 0015** (the token file and its checks stay; socket delivery is kept as a fallback)
- **Amended by:** ADR 0023 (with a stack pack, the pack's hosts replace the default allowlist), ADR 0026
  (the API socket meters token usage), ADR 0027 (it refuses requests once a budget is spent)

## Context

ADR 0007 planned an `--internal --disable-dns` network per project, with a shared, dual-homed
allowlist proxy container at a static IP. ADR 0015 delivered the OAuth token into the container
over the agent socket. Spike F (`spikes/sandbox-claude`) tested a simpler network and a stronger
credential path with a real Claude Code 2.1.319 session:

- `--network none` with a loopback→unix-socket bridge inside the container works. The CONNECT
  proxy runs in the daemon, and every agent has its own socket.
- With a **placeholder** token in the container, `ANTHROPIC_BASE_URL` pointing at a loopback bridge
  to a per-agent API socket, and `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, Claude Code's only
  call to Anthropic was `POST /v1/messages` with `Authorization: Bearer <placeholder>`. The daemon
  swapped in the real token and the session worked, including tool use. No CONNECT tunnel to
  Anthropic was opened at all.
- Without `NO_PROXY=127.0.0.1,localhost`, Claude Code sends the plain-HTTP API call into the CONNECT
  proxy (405).
- A fresh config needs `platform.claude.com` and shows four dialogs. Pre-seeded config keys remove
  both (see Decision).

## Options considered

**Network**

1. **Internal network + shared proxy container** (ADR 0007). Cons: an extra container to manage,
   subnet and static-IP config, and a shared proxy cannot tell agents apart.
2. **`--network none` + per-agent sockets to proxies in the daemon.** Pros: no network
   configuration at all; the socket identifies the agent, as with hooks (ADR 0003). That enables
   per-agent allowlists, logs and UI. Cons: needs an init process in the container for the bridges.

**Credential**

1. **Token delivered into the container** (ADR 0015, over the agent socket). Cons: the agent can
   read it from its own environment and try to exfiltrate it through any allowed domain.
2. **Token injected by the daemon on the API socket.** Pros: the token never enters the container,
   so a prompt-injected agent cannot leak it, and `api.anthropic.com` needs no CONNECT access.
   Cons: depends on undocumented Claude Code behaviour (base URL plus placeholder token), which a
   future version could change.

## Decision

Network option 2 and credential option 2.

**Container network:** `--network none`. The in-container init (`agent-office sandbox-init`) runs
the bridges, then starts the agent CLI as its child (tty inherited, SIGTERM/SIGHUP forwarded):
- `127.0.0.1:3128` → egress socket
- `127.0.0.1:8080` → API socket

The agent's environment includes:
- `HTTPS_PROXY` / `https_proxy=http://127.0.0.1:3128`
- `NO_PROXY` / `no_proxy=127.0.0.1,localhost`
- `ANTHROPIC_BASE_URL=http://127.0.0.1:8080`
- `CLAUDE_CODE_OAUTH_TOKEN=<fixed placeholder>`
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`
- `DISABLE_AUTOUPDATER=1`

Each agent's sockets live in `$XDG_RUNTIME_DIR/agent-office/agents/<id>/` (directory 0700, sockets
0600) and are bind-mounted one by one: the hook socket (later also MCP), `egress.sock` and
`api.sock`.

**Egress socket** (CONNECT proxy in the daemon):
- `CONNECT` only, port 443 only, host names only (no IP literals). The allowlist has exact names,
  plus `.suffix` entries for subdomains.
- Default allowlist: `proxy.golang.org`, `sum.golang.org`, `registry.npmjs.org` (Ozan's minimum, so
  agents can build and test). No Anthropic domain is needed. `AGENT_OFFICE_EGRESS_ALLOW` adds names
  until stack packs (Phase 6) provide them per stack.
- TLS stays end to end; the proxy never sees contents. Every denial is logged per agent (host:port
  only) and shown in the UI.

**API socket** (reverse proxy to `https://api.anthropic.com`):
- **Only `POST /v1/messages` is forwarded** (path match; the query string is passed through).
  Every other method or path gets a 403 with a JSON error. It is **logged with method and path and
  surfaced in the UI as an agent event** ("API request blocked: GET /v1/models"). That way a future
  Claude Code version that needs another endpoint fails visibly, not silently, and the endpoint
  can then be added on evidence.
- The incoming request must carry exactly `Authorization: Bearer <placeholder>`, otherwise it is
  rejected. All incoming credential headers (`Authorization`, `X-Api-Key`) are removed, and the real
  token is set on the outgoing request.
- **Responses stream through unbuffered** (`httputil.ReverseProxy` with `FlushInterval: -1`). The
  request body is streamed with a size cap (32 MiB), never read into memory. There are dial, TLS and
  response-header timeouts, but no overall timeout, because streams are long.
- Logs per request: agent, method, path, status, duration. **Never headers, bodies or the
  query string.**
- The token is read with `sandbox.ReadTokenFile` when the agent is launched and held as a
  `sandbox.Secret` only while that agent runs. A replaced file takes effect for the next agent.

**Pre-seeded Claude Code config** (per agent `CLAUDE_CONFIG_DIR`, never the user's):
- `.claude.json`: `hasCompletedOnboarding: true`, `lastOnboardingVersion: <running CLI version>`,
  `projects["/workspace"].hasTrustDialogAccepted: true`.
- In the daemon's `--settings` file: `theme`, `skipDangerousModePermissionPrompt: true`, next to the
  hooks.
- These keys are undocumented and were tested with **Claude Code 2.1.319**, recorded as a constant
  in the adapter. The daemon logs the CLI version at startup (it already does) and **warns at
  startup and in the UI when the version differs** from the tested one. The detection of agents
  stuck in `starting` stays as the safety net.

**The CLI binary:** the versioned file behind the `claude` symlink (`readlink -f`), mounted
read-only. Claude Code updates itself by switching the link, so a running worker keeps its version.

**Fallback:** ADR 0015's socket delivery (the init fetches the token once over the agent socket and
sets the real `CLAUDE_CODE_OAUTH_TOKEN`) stays documented and was shown to work in Spike F. It is
not implemented in Phase 3. If a Claude Code version breaks injection, it becomes a configuration
option, with the exfiltration risk stated in the UI.

## Consequences

- **Token usage per agent (Phase 8)** can be read on the API socket, straight from the responses:
  the `usage` numbers in `message_start` / `message_delta` events, read from the stream as it
  passes. **Numbers only, never content.** This is more direct than parsing session files and is
  attributed to the agent by its socket.
- **Per-agent budget limits** can live in the same place later: deterministic code refuses new
  requests once an agent's budget is spent, outside the model's control (Phase 4/8).
- Residual risks (threat model):
  - The agent can spend inference through the API socket. That is its job; budgets bound it later.
  - Allowlisted registries are an exfiltration channel. `proxy.golang.org` fetches arbitrary module
    paths from their origin, so data can leave encoded in a module path. The allowlist narrows
    exfiltration but cannot close it.
- Denials of `downloads.claude.ai` and `github.com` (the official plugin marketplace auto-install)
  are expected and harmless. ~~The UI shows them like any other denial.~~ In the Phase 3 runs (Claude
  Code 2.1.319) these were the only denied hosts.
- **Amended in Phase 17** (Ozan: they looked like problems on every worker's card). Measured with
  Claude Code 2.1.322, a sandboxed haiku worker in a scratch daemon, the egress proxy logging each
  refused CONNECT's headers: at start, before the first tool call, Claude Code sent
  `CONNECT downloads.claude.ai:443` (no User-Agent) and `CONNECT github.com:443` from `git`
  (`User-Agent: git/2.47.3`; `plugins/marketplaces` appears in its config dir), in every run and with
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` and `DISABLE_AUTOUPDATER=1` set. The 2.1.322 binary
  has `CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL`; with it set, two runs (one with a
  subagent) refused nothing at all, and a control run without it brought both requests back. No
  `count_tokens` call was seen in these runs. So:
  - Workers get `CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1` (and callers cannot override
    it): the requests are no longer made. Workers get their tools from the stack pack.
  - What Claude Code may still send on its own is listed in `internal/sandbox/background.go`:
    `downloads.claude.ai:443` (its own download and version host) and `POST /v1/messages/count_tokens`
    (reported by Ozan). These are still refused exactly as before, but recorded as
    `egress_background` / `api_background`, logged at debug level, and shown on the agent's card as a
    quiet "n background requests of Claude Code refused (expected)" line apart from the sandbox events.
    Anything not on the list (`github.com` from an agent's own `git`, `curl` or `go get` included)
    stays a warning. The list is exact names and routes, never patterns.
- Events are stored per agent (rate-limited) and listed by `GET /api/agents/{id}/events`, so the
  UI still shows them after a reload; live ones arrive as `sandbox_event` updates.
- If Claude Code changes how it authenticates or which endpoints it calls, sessions fail with a
  visible, logged rejection. The fix is a small, evidence-based change to this proxy, or the
  fallback.
