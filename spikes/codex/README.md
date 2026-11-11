# Spike G: Codex CLI behind agent-office

Throwaway. Question (Phase 15): can a Codex agent run like a Claude Code agent: in a PTY with hooks
for its state, with its usage readable, resumable, and in the network-less worker with the
credential injected by the daemon, so the container never holds the real token?

Tested 2026-11-11: Codex CLI 0.167.1 (npm `@openai/codex`, native `codex` and `codex-code-mode-host`,
static-pie musl), ChatGPT plan login, model `gpt-6.1-sol` (the plan's default), rootless Podman with
the agent-office base worker image. Output goes to `out/` (gitignored).

```sh
CGO_ENABLED=0 go build -o out/spike .
./out/spike placeholder -account-from ~/.codex/auth.json -out <home>/auth.json   # fake tokens, real account id
./out/spike proxy -listen 127.0.0.1:18080 | unix:<sock> -auth ~/.codex/auth.json [-tls-cert … -tls-priv …]
./out/spike hooklog -out out/hooks.jsonl -event <name>                           # a hook command
./run.sh tls ; ./run.sh tui <home> <log> '<steps json>' -- <codex args>          # TUI in a PTY behind TLS
```

The proxy reads the real access token and account id from the user's `~/.codex/auth.json` at every
request and never logs them; Codex itself runs with a scratch `CODEX_HOME` whose `auth.json` holds
only placeholder tokens (unsigned JWTs with a far expiry, refresh token `ao-placeholder`). The
user's `~/.codex` is never used by the spike's Codex, so its refresh token cannot rotate.

## Results

| Check | Result |
|---|---|
| Login shape (keys and lifetimes only) | `auth_mode` chatgpt; `tokens.{id_token, access_token, refresh_token, account_id}`; access token lifetime **240 h**, id token 1 h |
| Placeholder login + a custom provider (`base_url`, `wire_api = "responses"`, `requires_openai_auth = true`) + `chatgpt_base_url` at the proxy | **every** ChatGPT backend request goes to the proxy with the placeholder bearer; the proxy puts the real one on; `exec` answers. No request to `auth.openai.com` (no refresh: the placeholder never expires) |
| The same in the network-less worker (`--network none`, read-only rootfs, `sandbox-init` bridging 127.0.0.1:8080 to the API socket, egress socket refusing everything) | works: the shell tool ran (`sandbox.txt`), three model calls; **no CONNECT attempt at all**; needs `codex-code-mode-host` next to `codex` ("execution host is unavailable" otherwise) |
| Requests at start (with plugins, apps, analytics, updates and the shared daemon off) | `GET codex/models`, `GET wham/accounts/check`, `GET wham/settings/user`, then `POST codex/responses` per model call |
| Interactive TUI | refuses a plain-HTTP `chatgpt_base_url` ("workspace backend must use an HTTPS origin"): works with TLS on loopback and a CA in `CODEX_CA_CERTIFICATE`; a fake account id fails the workspace check ("selected workspace missing from routing discovery"), so the placeholder carries the **real account id** (an identifier, not a credential) |
| TUI first-run dialogs | folder trust ("Trust this folder?"), seeded with `[projects."<path>"] trust_level = "trusted"`; no others with the flags below |
| Hook events (inline `[[hooks.<Event>]]` in `config.toml`, `--dangerously-bypass-hook-trust`) | SessionStart (`source` startup / resume), UserPromptSubmit, PreModelRequest, PostModelRequest (`status`, `token_usage`), PreToolUse / PostToolUse (`tool_name` Bash, `tool_input`), Stop (`stop_hook_active`, `last_assistant_message`), SessionEnd (`reason`). Fields: `session_id`, `cwd`, `transcript_path`, `model`, `permission_mode`, `turn_id`. PermissionRequest not seen (approvals bypassed) |
| States | SessionStart → idle once the first turn's Stop arrives; UserPromptSubmit/PreModelRequest/PreToolUse → working; Stop → idle; SessionEnd → exited. A prompt typed into the TUI plus Enter starts a turn |
| Usage | `PostModelRequest.token_usage` {input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens, total_tokens}; the same numbers in the stream's `response.completed` event (`usage.input_tokens`, `input_tokens_details.cached_tokens`, `output_tokens`, `output_tokens_details.reasoning_tokens`), so the API socket can meter it like Claude's |
| Session id | chosen by Codex (UUIDv7), not by the caller; learned from the first hook |
| Resume | `codex resume <id> [prompt]` continues the conversation (recalled a codeword) under the **same** session id, SessionStart `source: resume`; rollout files in `$CODEX_HOME/sessions/` |
| Warnings in the TUI | "Running without the shared background server: --dangerously-bypass-hook-trust requires embedded mode" (wanted: one self-contained process per agent) |

## Findings

- **The real credential does not have to be in the container.** Codex accepts a placeholder login
  and sends its ChatGPT bearer to whatever base URL it is given; the daemon side swaps in the real
  token. This is ADR 0016's design for Claude Code, with two differences: TLS on the loopback leg
  (the TUI insists on HTTPS; the bridge is a byte pipe, so TLS ends on the daemon side of the
  socket, with a per-daemon CA the container trusts), and the account id in the placeholder.
- **Refresh stays with the user's own Codex CLI.** The daemon only reads the access token (10-day
  lifetime) at each request; it never uses or rotates the refresh token, so it cannot race the
  user's CLI. When the token has expired, requests fail and the user runs Codex once (or `codex
  login`). The daemon can warn ahead of time from the token's `exp`.
- **Only a few backend paths are needed**; everything else (plugins, MCP-over-HTTP, analytics) is
  off by config and can be refused on the socket.
- **Hooks are close to Claude Code's**, so the same state machine applies; the adapter maps names
  and reads `token_usage`. The session id has to be learned, which changes the hook check and the
  resume bookkeeping (Codex resumes under the same id; Claude Code forks to a new one).
