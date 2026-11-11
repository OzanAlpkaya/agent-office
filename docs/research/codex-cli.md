# Codex CLI: integration surface

**Verified 2026-11-11 against Codex CLI 0.167.1 in Spike G** (`spikes/codex/README.md`): hooks,
usage, resume, folder trust, the ChatGPT-plan login behind a credential-injecting proxy, and the
network-less worker. Where the Phase 0 notes below differ, the spike wins:

- Hooks are inline `[[hooks.<Event>]]` tables in `config.toml` (or `hooks.json`); they need trust,
  which `--dangerously-bypass-hook-trust` skips for one run (embedded mode, no shared server).
  Events seen: SessionStart, UserPromptSubmit, PreModelRequest, PostModelRequest (`token_usage`),
  PreToolUse/PostToolUse, Stop, SessionEnd.
- Usage: `PostModelRequest.token_usage`, and the same numbers in the Responses stream's
  `response.completed` event.
- The session id is Codex's (UUIDv7); `codex resume <id> [prompt]` continues under the same id.
- SessionStart arrives only with the first prompt, fresh or resumed (found in the Phase 15 e2e run);
  without a prompt Codex sends no hook until SessionEnd.
- ChatGPT's backend sends the Responses stream without a Content-Type.
- A custom model provider with `requires_openai_auth = true` plus `chatgpt_base_url` send the
  ChatGPT bearer to agent-office's proxy, which adds the real token: the container holds placeholder
  tokens only (and the account id). The TUI needs HTTPS for the ChatGPT base URL (custom CA via
  `CODEX_CA_CERTIFICATE`) and a seeded folder trust.

The Phase 0 notes follow (docs only, written before Codex was installed; reviewed 2026-11-06). The official docs
moved from developers.openai.com/codex to learn.chatgpt.com/docs. Items marked *(secondhand)* come
from community sources and must be verified against the installed version before use.

## How a supervisor can track a Codex session

| Surface | What it gives | Fit for agent-office |
|---|---|---|
| **Lifecycle hooks** (`~/.codex/hooks.json`, `<repo>/.codex/hooks.json`, or `[hooks]` in `config.toml`) | Command hooks with JSON on stdin: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `model`. Events: SessionStart, SessionEnd, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest, PreModelRequest, PostModelRequest (with `token_usage`), Stop (`stop_hook_active`, `last_assistant_message`), SubagentStart/Stop, Pre/PostCompact, Interrupt. Exit 2 blocks, with the reason on stderr. | **Best match**: almost the same model as Claude Code hooks, so the same state machine works. Caveats: command hooks only (no HTTP type documented); project hooks need a trusted project and a `/hooks` review. |
| `notify` program | One event, `agent-turn-complete`, with thread/turn ids and the last message. | Too coarse: cannot signal "waiting for input". |
| `codex exec --json` | JSONL events: `thread.started`, `turn.started/completed` (with `usage`), `item.*`, `error`. | Good for non-interactive workers. Not a live TUI. |
| `codex app-server` *(secondhand)* | JSON-RPC 2.0 over stdio: `thread/start`, `turn/start`, `turn/interrupt`, notifications for turns and items. Schema generated from the installed binary. | Richest, but a different interaction model from a PTY. Worth an ADR when Codex support starts. |

- **Token usage:** `PostModelRequest.token_usage` (hooks) or `turn.completed.usage` (exec JSON).
  Rollout files under `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` *(secondhand)* are the
  fallback and change between versions.
- **Config:** `CODEX_HOME` (default `~/.codex`) relocates config, auth, history and logs. MCP
  servers are declared in `config.toml` under `[mcp_servers.<id>]`. Codex has its own sandbox
  (`sandbox_mode`, `approval_policy`). When agent-office provides the sandbox, Codex would run in
  `danger-full-access` mode inside the container, mirroring the Claude Code plan.

## Consequences for the design

- The `internal/agent` adapter interface must not assume HTTP hooks or Claude-specific fields. Each
  adapter translates its events into a common set: `SessionStarted`, `TurnStarted`, `ToolStarted`,
  `ToolFinished`, `PermissionRequested`, `TurnFinished`, `Idle`, `SessionEnded`, `UsageReported`.
- Ship one small `agent-office hook` subcommand that forwards stdin to the daemon. It serves Codex
  (command-only hooks) and Claude Code's SessionStart (no HTTP hook) alike.

Sources: learn.chatgpt.com/docs/hooks, /docs/non-interactive-mode, /docs/config-file/config-reference,
/docs/config-file/config-advanced. The app-server details come from a community mirror of the Codex
repository README.
