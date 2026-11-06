# Spike B: Claude Code hook payloads

Throwaway code. Questions: which hook events fire when, what do the payloads look like, which
transport should hooks use to reach the daemon, and can a hook deliver a message to an agent?

Hooks are configured **only** in this folder's `.claude/settings.json`. Every event has an `http`
hook and a `command` hook (`curl` → the same receiver), so the two transports can be compared.
Captured payloads go to `out/` (gitignored: it contains prompts and paths).

```sh
go run . -scenario print         # claude -p, one allowed Bash call
go run . -scenario inject        # Stop hook injects a message once (-inject-via http|command)
go run . -scenario interactive   # TUI in a PTY: permission prompt -> approve -> idle -> /exit
go run . -summarize out/<run>    # shapes only: event names, enums, key types
```

Tested on 2026-11-06 with Claude Code 2.1.319 and `--model haiku`. The driver removes `CLAUDECODE`,
`CLAUDE_PID`, `CLAUDE_EFFORT` and `CLAUDE_CODE_*` from the child environment, the way a daemon
would launch it. That list includes a messaging token that must never leak into agents.

## Observed sequence (interactive run)

| t (ms) | Event | Discriminator | Meaning for agent state |
|---:|---|---|---|
| ~400 | SessionStart | `source=startup` | ready / idle |
| +4800 | UserPromptSubmit | | working |
| +1100 | PreToolUse | `tool_name=Write` | working (activity: Write) |
| +15 | PermissionRequest | `tool_name=Write` | **waiting for permission** (immediate) |
| +6000 | Notification | `notification_type=permission_prompt` | same thing, about 6 s late |
| (approve) | PostToolUse | `tool_name=Write` | working |
| +900 | Stop | `stop_hook_active=false` | turn finished → waiting for input |
| +800 | SubagentStop | has `agent_id`, empty `agent_type`, no SubagentStart | internal background agent; ignore for main state |
| +60000 | Notification | `notification_type=idle_prompt` | idle for 60 s |
| (/exit) | SessionEnd | `reason=prompt_input_exit` | exited |

In `-p` mode the same events appear without Notification. SessionEnd reports `reason=other`.

## Payload shapes (redacted)

Common keys: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `prompt_id`,
`permission_mode`, and on most events `effort{level}` and `scratchpad_dir`.

```jsonc
// PreToolUse (PermissionRequest has the same shape + permission_suggestions[], minus tool_use_id)
{ "hook_event_name": "PreToolUse", "session_id": "<uuid>", "prompt_id": "<id>",
  "cwd": "<path>", "transcript_path": "<path>.jsonl", "permission_mode": "default",
  "effort": { "level": "<level>" },
  "tool_name": "Write", "tool_input": { "file_path": "<path>", "content": "<text>" },
  "tool_use_id": "<id>" }
// PostToolUse adds: "tool_response": { ... }, "duration_ms": 0
// Stop: "stop_hook_active": false, "last_assistant_message": "<text>", "background_tasks": [], "session_crons": []
// Notification: "notification_type": "permission_prompt" | "idle_prompt", "message": "<text>"
// SessionEnd: "reason": "other" | "prompt_input_exit"
```

Payloads carry user prompts (`prompt`), file contents (`tool_input`) and assistant text. The daemon
should keep only what state tracking needs and treat the rest as sensitive.

## Transport findings

- **HTTP hooks** send `POST`, `Content-Type: application/json`, with a body identical to the
  command hook's stdin. Headers sent: Accept, Content-Length, Content-Type, User-Agent. The
  response body is interpreted like a command hook's stdout. The stream reports the HTTP status as
  `exit_code` (200).
- **HTTP hooks do not fire for SessionStart** (reproduced 3 times). Only the command hook ran.
  Every other registered event fired on both transports, a few ms apart.
- **User-level settings merge into agent sessions.** Ozan's global SessionStart hook ran inside
  the spike session too. The daemon must decide explicitly which setting sources an agent gets
  (`--setting-sources` + `--settings <file>`).
- **No workspace trust dialog** appeared for this folder in interactive mode. It is probably
  inherited from the already-trusted project directory; not verified, since the trust store is a
  protected file. Worktrees at new paths may show the dialog. Phase 1 must detect it from PTY
  output and surface it, not auto-accept it.
- **Identity:** the daemon passes `--session-id <uuid>` at launch, so every hook payload can be
  mapped to an agent without guessing. Hook calls still need authentication (any local process can
  POST to localhost). HTTP hooks support headers with env-var interpolation, which allows a
  per-agent token (to be tested in Phase 1).

## Message delivery via Stop hook

The receiver answered the first Stop with `{"decision":"block","reason":"New message from the lead
agent: ..."}`. The agent continued in the same session and followed the message. A second Stop
then arrived with `stop_hook_active=true`, and the receiver let it end. Result: it works over
**both** transports (HTTP response body, and command-hook stdout via `curl`), and
`stop_hook_active` prevents loops.

Limitation: this only works at the end of a turn. An agent that is already idle has no pending
Stop. Because the daemon owns the PTY, it can also deliver a message by typing it as user input.
Phase 4 should use both: Stop-hook injection for busy agents, PTY input for idle ones.

## Open questions

Answered in Phase 1 (see ADR 0003 and `docs/research/claude-code-trust.md`): no hook fires on
interrupt or on a dismissed permission prompt, and the trust dialog appears for new folders but not
for subfolders or worktrees of a trusted repo. Original list:

- Interrupt (Esc / Ctrl-C mid-turn): does Stop fire? If not, state can stay stuck at "working".
- Permission **denied** path (PermissionDenied / PostToolUseFailure) was not exercised.
- Per-agent auth header on HTTP hooks (env interpolation) not yet tested.
