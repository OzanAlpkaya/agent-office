# 0065. Claude Code's dangerous rm prompts in the sandbox

- **Status:** Accepted
- **Date:** 2026-11-13

## Context

Sandboxed workers run Claude Code with bypass permissions on (ADR 0007, 0016), so they do not ask
before they act. One check is left that no mode turns off: before an `rm`/`rmdir` whose target it
cannot check (command substitution, a possibly empty variable, a glob after a `cd`, the working
directory itself), Claude Code shows "Dangerous rm operation ... Do you want to proceed?" and blocks the
command by itself after 2 minutes. In Bouncibo these prompts waited in a worker's terminal that nobody
watched.

Measured with Claude Code 2.1.323 in Spike F's sandboxed worker (`spikes/sandbox-claude/README.md`):
the prompt raises a `PermissionRequest` hook first (`tool_name: Bash`, `tool_input.command`, `cwd`,
`permission_mode: bypassPermissions`); a reply
`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` runs the
command without the prompt, also when it arrives 40 s later while the prompt is on screen; `deny` with a
`message` refuses it and the model reads the message. Commands Claude Code can check itself (`$(pwd)`,
`D=x; rm -rf "$D/y"`, literal paths) never prompt.

## Options considered

1. **Leave it to the user**: the prompt waits, as before; a worker can sit for minutes or lose its
   command after 2.
2. **Approve every such prompt in the sandbox**: the container limits the damage, but an rm that
   empties the workspace or its `.git` loses the worker's work, and the user asked for the clear cases only.
3. **Turn the check off** (`CLAUDE_CODE_EVEN_MORE_DANGEROUSLY_SKIP_PERMISSIONS`): the same as 2, and any
   `PermissionRequest` hook (agent-office registers one for state tracking) keeps it from taking effect.
4. **Decide in the daemon**: approve what is provably inside the workspace, let the Tech Lead decide the
   rest within the prompt's time, else leave it to the user.

## Decision

Option 4, for Claude Code agents in the sandbox only (host-mode agents run on the user's machine; Codex
has no such check).

- **The rule** (`internal/rmcheck`): the command is parsed with a real bash parser (`mvdan.cc/sh/v3`,
  shfmt's; a hand-written splitter would be the hole). It is approved when it has at least one `rm` or
  `rmdir` and every operand of every one resolves, before the command runs, to a path **strictly inside
  `/workspace` with no `.git` component**. Resolution covers literals and quotes, `$PWD`, variables
  assigned a literal earlier in the command, relative paths against the hook's `cwd` and literal `cd`s
  (after `cd x;` both directories must pass, after `cd x &&` the new one), and a glob in the last part
  only, never one that can match a dot file. Directories on the way must not be symlinks, checked with
  `Lstat` on the host side of the workspace mount. Anything not modelled is a no: substitutions, other
  variables, `~`, braces, loops, functions, subshells, `eval`, `source`, `sh -c`, `xargs`, `find`, `rm`
  named in another command's arguments, a command name built from a variable. A fuzz test checks that an
  approval only ever names targets inside the workspace.
- **The reply**: `allow` at once. The worker stays as it was (no "needs you"); the event is recorded as
  `permission_auto`.
- **The Tech Lead**: a worker of a goal whose Tech Lead runs gets its other rm prompts decided by that
  lead. The daemon holds the hook, sends the lead an agent-office message with the request id, the
  command (one line, at most 500 characters, quoted as the worker's text) and the directory, and the
  lead answers with `decide_command` (`request_id`, `approve`, `reason`). No answer within 100 s is a
  refusal that tells the worker to use literal paths. Each decision goes to the user's inbox. Requests
  live in memory only while their hook is held; one per worker. A hook from the worker meanwhile (the
  user answered in its terminal) ends the request without a reply.
- **Everyone else** (no lead, the lead itself, the Architect, QA, reviewers, a prompt that is not about
  rm, a session not in bypass mode): no reply; the prompt waits for the user, as before.
- **Timeouts**: the `PermissionRequest` hook has its own settings entry, `agent-office hook --wait` with a
  120 s timeout, whose forwarder waits 115 s; the daemon holds a hook at most 110 s, and closing an agent's
  socket ends a held hook at once.

## Consequences

- Clear rm commands in a worker's own workspace no longer stop it; unclear ones get an answer within
  2 minutes, from the lead, with a reason the user can read.
- The rule is deliberately narrow: commands Claude Code prompts for are by definition the ones it could
  not check, so most go to the lead. The approved ones are the cases the rule resolves better than
  Claude Code's check (a glob after a literal `cd`, a `$PWD` path).
- New dependency: `mvdan.cc/sh/v3`.
- The command text reaches the lead as agent-written data, like other worker text (ADR 0017); the lead's
  prompt says to refuse what could reach outside the workspace or its `.git`.
- Claude Code may change its prompts; the spike's checks are the reference, and the hook path fails safe
  (no reply leaves the prompt to the user).
