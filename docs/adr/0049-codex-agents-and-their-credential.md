# 0049. Codex agents, sandboxed, with the credential outside the container

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Phase 15 adds the OpenAI Codex CLI as a second agent CLI. Ozan signs in to Codex with his ChatGPT
plan (no API key). The rule he set for the phase: the container must never hold the real credential
(the same promise ADR 0015 and 0016 make for Claude Code); if it would have to, he decides.

Spike G (`spikes/codex/README.md`, Codex 0.167.1) checked the installed CLI:

- Its login (`~/.codex/auth.json`) is OAuth: an access token (10-day lifetime), a refresh token, an
  id token and the ChatGPT account id. Codex refreshes it itself.
- A custom model provider with `requires_openai_auth = true`, plus `chatgpt_base_url`, sends every
  ChatGPT-backend request (models, accounts, settings, the Responses stream) to a URL of our choice
  with the login's bearer. With a placeholder login, a proxy that swaps in the real token works,
  including in the network-less worker.
- The interactive TUI insists on an HTTPS ChatGPT base URL and checks the login's account id against
  the accounts list (a fake id fails).
- Hooks (inline `[[hooks.<Event>]]`, trust bypassed with a flag) are close to Claude Code's; usage is
  in `PostModelRequest.token_usage` and in the stream's `response.completed`; Codex picks its own
  session id and `codex resume <id>` continues it under the same id; SessionStart arrives only with
  the first prompt. Both binaries (`codex`, `codex-code-mode-host`) are static.

## Options considered

1. **Mount the user's login into the container.** Simplest; Codex refreshes its own token. Rejected:
   the real access and refresh tokens would be inside a container whose agent is untrusted.
2. **An OpenAI API key, injected like Claude Code's** (`OPENAI_API_KEY` placeholder, daemon adds the
   key). Not what Ozan uses, and a ChatGPT token cannot call the platform API.
3. **A placeholder ChatGPT login and a credential-injecting API socket** (ADR 0016's design).
   The daemon reads the access token and account id from the user's login at every request and
   puts them on; the refresh token is never read.
4. **Codex's app-server protocol** with externally supplied tokens. A different interaction model
   from a PTY with hooks; more to build and to keep up with.

## Decision

Option 3; no stop was needed, since the real credential stays outside.

- **In the container:** `CODEX_HOME` is the agent's config dir; the daemon writes `config.toml`, the
  placeholder `auth.json` and the loopback CA into its own runtime dir and mounts them read-only over
  the config dir's files. The placeholder holds unsigned JWTs expiring in 2100 (so Codex never tries
  to refresh), a refresh token that says it is not a credential, and the user's **account id and
  plan** (identifiers the TUI checks; they authenticate nothing). `config.toml`: the provider at
  `https://127.0.0.1:8080/backend-api/codex`, `chatgpt_base_url` at the same origin, hooks for every
  state through `agent-office hook`, the team MCP server (ADR 0006), the workspace trusted, and
  plugins, apps, analytics, in-app updates and the shared background server off. Codex runs with
  `--dangerously-bypass-approvals-and-sandbox` (the container is the sandbox, ADR 0007) and
  `--dangerously-bypass-hook-trust` (the hooks are agent-office's own, mounted read-only).
  `CODEX_CA_CERTIFICATE` points at the CA. The static Codex binaries are mounted read-only.
- **The API socket** (`sandbox.CodexProxy`): TLS from a per-daemon, in-memory CA whose certificate
  is name-constrained to 127.0.0.1 and localhost and whose private key never touches disk; it
  accepts only the placeholder bearer and the user's account, allows a fixed list of routes
  (models, accounts check, user settings, usage and rate limits, the Responses stream), puts the real
  token and account on, meters the stream's usage (the backend sends it without a Content-Type) and
  applies budgets to model requests. Refusals are sandbox events, as for Claude Code.
- **The login on the host** is read with the token files' checks (user-only file, no symlink) at
  every request. agent-office never refreshes it, so it cannot race the user's own Codex CLI; when
  the token expires, requests fail with a sandbox event saying to run Codex once or `codex login`,
  and `serve` warns when it expires within two days.
- **Host mode:** Codex agents are refused; the credential could not be kept from a host process.
- **Sessions:** the daemon's session row keeps Codex's own id (`sessions.cli_session_id`, migration
  0014), learned from the first SessionStart on the agent's hook socket; later hooks must carry it.
  A resume continues it (`codex resume <id>`); a restore after a crash does the same (ADR 0035). An
  agent launched without a prompt is marked idle once its TUI has had time to come up, since Codex
  sends no hook before the first prompt.
- **Messages** are typed into a Codex agent's terminal when it is idle (ADR 0018); the Stop-hook
  reply that delivers them to Claude Code is not used.
- **Version:** `TestedCodexVersion` (0.167.1); another version gets a warning. The generated config is
  checked against the installed CLI with `exec --strict-config` in the unit tests.

## Consequences

- Tested: `TestCodexProxy`, `TestReadCodexLogin`, `TestCodexConfig`, `TestCodexConfigLoadsStrictly`,
  `TestSandboxedCodexWorker` (real Podman, a fake Codex: placeholder only, the real token added
  outside, routes refused, session learning, foreign sessions refused, usage, team tools, report,
  resume), `TestSandboxedCodexReviewer`, `TestSandboxedCodexRestore`; with the real CLI in
  `TestCodexTeamEndToEnd` (a Codex worker in a Claude-led team, its usage, a SIGKILL and restore that
  recalls the conversation) and the GitHub e2e with a Codex reviewer.
- The container knows the user's ChatGPT account id and plan type. They are not credentials, but
  they identify the account to anything the agent can reach (only allowlisted hosts).
- A prompt-injected Codex agent can still spend the user's ChatGPT plan through the proxy, as a Claude
  agent can spend inference; budgets apply only with prices for its models (none are built in).
- Codex changes fast: the backend routes, the TUI's checks and the lazy SessionStart were found by
  running it; a new version may need the route list or the config updated (the warning says so).
