# 0066. The Tech Lead's view of its team's branches

- **Status:** Accepted
- **Date:** 2026-11-13

## Context

The Tech Lead's workspace is a read-only clone (ADR 0029) made when it started. Workers' work reaches
the main repository as imported branches (ADR 0014), QA's as an integration branch (ADR 0033); none of
them is in the lead's clone, and the clone cannot fetch (read-only, no remote). In Bouncibo Goals 03
and 04 the lead reported twice that it could not open the renders a worker made.

## Options considered

1. **Refresh the lead's clone** with the team's refs from outside: the host must not run git in an
   agent's clone (ADR 0014), so this needs a helper container per refresh, and images still need a file
   the Read tool can open.
2. **Mount checked-out copies** of each branch read-only into the lead's container: copies of whole
   repositories per sub-task, refreshed on every report; mounts are fixed at start, so a directory of them.
3. **Team tools**: the daemon reads the files from the main repository with git plumbing and returns them
   through the lead's MCP server, images as MCP image content.

## Decision

Option 3: two Tech Lead tools.

- `list_files(source, path, recursive)` and `read_file(source, path)`. `source` is a sub-task id of the
  lead's own goal (the branch of its worker, as `branchOfTask` finds it for QA) or `"qa"` (the goal's
  newest QA run with a head). Other goals, unknown ids and workers calling are refused.
- Plumbing in the main repository only: `git ls-tree -z -l` and `git cat-file` on the resolved commit;
  paths are repository-relative, cleaned, no `..` or control characters. Listings stop at 500 entries.
- `read_file` sniffs the content: PNG, JPEG, GIF and WebP up to 3.75 MB (about the model's 5 MB once
  base64-encoded) come back as **MCP image content**, so the model sees the image; UTF-8 text without NUL
  up to 256 KB as text, marked as agent-written; anything else is refused with its type and size.
- The tech-lead role prompt names the tools and says to check a sub-task's output this way before
  accepting it or starting QA.

## Consequences

- The lead sees renders and screenshots without a writable or refreshed workspace, and only its own
  goal's work.
- Reading costs the lead's context: images are large; the size limit bounds one call.
- Codex leads get the same tools; whether Codex shows MCP image content to its model was not checked.
- Nothing runs in an agent's clone, and the git commands take no option from the agent (the commit is a
  sha the daemon resolved; the path follows `commit:` or `--`).
