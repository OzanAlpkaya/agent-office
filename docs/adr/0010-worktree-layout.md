# 0010. Worktree layout and lifecycle

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Every agent gets its own git worktree and branch, so agents can work on the same repository in
parallel without touching each other's files or the user's checkout. Claude Code's workspace trust
follows the repository for worktrees, wherever they live (`docs/research/claude-code-trust.md`).
Worktrees hold agent work, which must never be lost by accident.

## Options considered

1. **Inside the repo** (`<repo>/.worktrees/<agent>`). Pros: easy to find. Cons: pollutes the
   user's checkout (needs a gitignore entry in every repo); tools that scan the repo (IDEs,
   linters, test runners) see every worktree.
2. **Next to the repo** (`<repo>-worktrees/`). Cons: clutters the user's projects folder.
3. **XDG data dir**: `$XDG_DATA_HOME/agent-office/worktrees/<project>/<agent-id>` (default
   `~/.local/share/...`). Pros: out of the user's way, one place to inspect and clean up, works
   for any repo. Cons: less visible, so the UI must show paths.

## Decision

Option 3 (Ozan's preference, confirmed by the trust finding).

- `<project>` = folder name of the repository's top level, slugged, plus `-` and the first 8 hex
  characters of the SHA-256 of its absolute path. Two repos with the same name don't collide.
- **Branch:** `agent-office/<task-slug>-<short-agent-id>` when the agent starts from a task,
  `agent-office/<agent-id>` otherwise. The task slug is ASCII-only, lowercase, hyphens only, at
  most 40 characters; Turkish letters are transliterated (ş→s, ğ→g, ı→i, İ→i, ö→o, ü→u, ç→c).
- **Base:** the repository's current `HEAD` commit when the agent starts (recorded).
- **Removing an agent never deletes uncommitted work:**
  - clean worktree, no new commits → worktree and branch are deleted;
  - clean worktree with commits → worktree deleted, **branch kept**;
  - dirty worktree → **worktree kept**; the UI offers "Discard changes", an explicit, confirmed
    action that force-removes the worktree (branch kept if it has commits).
- **Startup reconciliation:** `git worktree prune` per project. Worktrees recorded as active
  whose folder is missing are marked removed.

## Consequences

- Disk usage grows with each worktree. The UI lists kept worktrees so they can be cleaned up.
- Not a security boundary: until Phase 3, agents run on the host and can reach other paths.
- The trust behaviour is undocumented; agents stuck in `starting` are still surfaced in the UI.
