# 0037. Relevant project memory: scopes the user controls, read on demand, exported as Markdown

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0034 (what reaches a prompt, who may list memory), ADR 0029 (behaviour ceilings)

## Context

ADR 0034 put every accepted memory entry into every agent's first prompt, newest first, within
6 000 characters. Phase 9's DoD asks that agents read *relevant* project memory at task start; the
Phase 12 backlog adds per-task relevance and export. As memory grows, a frontend convention in a
store worker's prompt is noise, and the 6 000 characters run out.

What an agent's task is about is known: sub-tasks declare the files they touch (ADR 0032), and
every agent has a role name.

## Options considered

**How relevance is decided**

1. **Keyword matching** between an entry and the task text. Cons: fuzzy, hard to predict, easy to
   game with agent-written task titles.
2. **A model ranks entries.** Cons: cost and latency at every start; non-deterministic; an
   injected task text could steer it.
3. **Explicit scopes on each entry** (role names, file patterns), set by the user or proposed by
   the assistant and accepted by the user. Pros: deterministic, testable, visible in the UI.

**Entries that do not fit the prompt**

1. **Dropped silently.**
2. **Readable on demand with `list_memory`**, which the prompt mentions.

**Export**

1. **The daemon writes `docs/memory.md` into the repository.** Cons: the daemon would commit to the
   user's branches; agents could then change memory through merged PRs (ADR 0034).
2. **A Markdown download**; the database stays the source.

## Decision

Option 3, option 2 and option 2.

- `memory.roles` and `memory.paths` (migration 0010, JSON arrays, at most 10 each): role names
  must exist; paths follow a sub-task's file rules (repository-relative, directories ending in `/`,
  globs). Empty means every role / every task.
- An entry **applies** to an agent when it has no roles or the agent's role is among them, and no
  paths or a declared file of the agent's task overlaps one of them (ADR 0032's overlap rule).
- The first prompt (and a relaunch's) lists the applicable accepted entries: scoped ones first,
  then unscoped, each group newest first, within 6 000 characters, and says how many were left out
  for length and how many belong to other roles or files, pointing to `list_memory` when the agent
  has it.
- `list_memory` joins the ceilings of the architect, lead, worker and qa behaviours (built-in roles
  inherit it; user roles that list their tools may add it). Non-assistants get accepted entries
  only, each marked `applies_to_you`. Reviewers do not get it: they read the most injectable input
  there is and need nothing beyond their prompt.
- `propose_memory` and the user's API accept `roles` and `paths`; the UI shows and edits them.
- `GET /api/memory/export` returns the accepted entries as Markdown, grouped by kind with their
  scopes; the memory panel links to it.

## Consequences

- A worker on `internal/store/` gets the store conventions first and not the web ones; an entry
  can be targeted at QA or the Tech Lead only.
- Who decides stays the same (ADR 0034): only accepted entries reach agents; scopes only narrow.
- An entry scoped too narrowly is missing from prompts it would have helped; `list_memory` still
  shows it and the prompt says that more exists.
- Agents without declared files (the lead, the architect, a user-started worker) get only entries
  without paths in their prompt.
