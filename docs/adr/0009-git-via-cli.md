# 0009. Run git through the git CLI

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Phase 2 creates, inspects and removes git worktrees and branches for every agent. The operations
must behave exactly as they do for the user in a terminal, since the user and the agents work in
the same repositories.

## Options considered

1. **The `git` CLI via `os/exec`** with argument lists. Pros: the reference implementation, so it
   honours the user's config, hooks and worktree metadata exactly. Full worktree support. Cons:
   process spawns, and output parsing needs care (use porcelain formats).
2. **go-git** (pure Go). Pros: no external binary, typed API. Cons: linked-worktree management
   is, as far as I know, newer and less complete than the CLI's (not evaluated in depth), and any
   reimplementation can differ subtly from the CLI the user and the agents use.
3. **libgit2 bindings.** Cons: cgo, which breaks the static binary (ADR 0004 avoids cgo for the
   same reason).

## Decision

Option 1, wrapped in `internal/git`:
- Commands are built from argument lists and never through a shell. Values that could look like
  options are separated with `--` where git supports it.
- Every call has a timeout and runs with `GIT_TERMINAL_PROMPT=0` (never block on credentials) and
  `LC_ALL=C` (stable messages).
- Machine-readable output only: `worktree list --porcelain`, `status --porcelain`, `rev-list --count`.
- Branch names come from a slug function: ASCII only, lowercase, hyphens, length-limited, with
  Turkish letters transliterated. They are additionally checked with `git check-ref-format`.

## Consequences

- `git` is a runtime requirement (tested with 2.56); the daemon checks for it at startup.
- Tests are integration tests against temporary repositories, not mocks.
