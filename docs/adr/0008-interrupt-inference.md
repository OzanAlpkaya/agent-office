# 0008. Infer user interrupts from terminal input

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Agent state comes from Claude Code hooks (ADR 0003). The Phase 1 end-to-end run (Claude Code
2.1.319) showed two situations in which **no hook fires at all**:

- The user interrupts a running turn with Esc: no Stop, no StopFailure.
- The user dismisses a permission prompt with Esc: no PermissionDenied, no PostToolUseFailure.

No `Notification(idle_prompt)` followed either, even after 75 s. The next hook is the
UserPromptSubmit of the *next* prompt. Without help, the UI keeps showing `working` or
`waiting_permission` while the agent is in fact waiting for input. In Phase 4 that would also
block message delivery, which depends on knowing whether an agent is idle.

## Options considered

1. **Accept the stale state.** Pros: no guessing. Cons: wrong state indefinitely; breaks idle
   detection for message delivery.
2. **Timeout** (e.g. no hook for N seconds → idle). Cons: a long model turn or a long-running tool
   produces no hooks either, so any N is wrong in one direction or the other.
3. **Parse the PTY output** for the TUI's "Interrupted" or prompt markers. Cons: depends on the
   TUI's rendering (cursor positioning splits words, see the trust spike) and breaks on any UI
   change.
4. **Tail the transcript** for an interruption record. Cons: undocumented, internal format.
5. **Infer from input.** The daemon owns the PTY, and all keyboard input passes through
   `engine.Input`. A lone Esc (`\x1b`) or Ctrl-C (`\x03`) while `working` or
   `waiting_permission` almost always means interrupt or deny.

## Decision

Option 5. `engine.Input` raises a CLI-agnostic `UserInterrupted` event when the whole input chunk
is exactly `\x1b` or `\x03`. The state machine maps it to `idle` from `working` or
`waiting_permission`, and ignores it in every other state. Escape *sequences* (arrow keys
`\x1b[A`, function keys) arrive as one multi-byte chunk and are not interrupts. The engine test
checks that `\x1b[A` is ignored. The end-to-end deny/interrupt steps send a lone `\x1b`
themselves. In the browser, xterm.js 6.0.0 maps the Esc key to a lone `\x1b`
(`src/common/input/Keyboard.ts`, key code 27), so the UI produces exactly what the inference
expects. **Alt+Esc** produces `\x1b\x1b` and is deliberately not treated as an interrupt.
(Verified from the xterm.js source; a live browser check was not possible from here.)

## Limits

- **Esc is also used for menus and dialogs** (slash-command pickers, autocomplete, prompts inside
  a running turn). Pressing Esc to close a menu *during* a turn would wrongly show `idle` while
  the agent keeps working.
- Ctrl-C while idle (Claude Code's "press again to exit") does not change state. The exit itself
  is caught by SessionEnd or process exit.
- The inference only sees input that goes through the daemon. Input written to the PTY by other
  means is not inspected. Today nothing else writes to it.
- It encodes behaviour of a specific Claude Code version. If a future version adds an interrupt
  hook, the adapter should use it, and this inference can be dropped.

## How a wrong guess is corrected

The inference never overrides hooks; it only fills the gap until the next one. Any later real
hook moves the state back: PreToolUse/PostToolUse → `working`, PermissionRequest →
`waiting_permission`, Stop → `idle`, SessionEnd or process exit → `exited`. So the worst case is a
turn without further tool calls shown as `idle` until its Stop arrives, and it ends in `idle`
anyway. Every inferred transition is logged at debug level (`user interrupt`, with the state it
came from).

## Consequences

- Terminal input must always go through `engine.Input`, never straight to the PTY. Phase 4's
  PTY message delivery must use the same path, and never send a lone Esc or Ctrl-C.
- Tests cover Esc from `waiting_permission`, and arrow keys being ignored (`internal/engine`), as
  well as the transition table (`internal/hooks`).
