# 0059. Branch clean-up: delete what is in the target, name the rest, record every head

- **Status:** Accepted
- **Date:** 2026-11-12
- **Amends:** ADR 0010 (branches kept on removal), ADR 0055 (after the merge)

## Context

Every worker, QA run, lead and reviewer gets an `agent-office/*` branch. Removing an agent deletes
its branch only when it has no commits (ADR 0010), so a done goal leaves its sub-task and QA
branches behind, and after a few sprints a repository has dozens. A goal's merge is a squash
(ADR 0055): its branches never become ancestors of `main`, so `git branch --merged` does not see
them. Ozan asked: when a goal is done, offer to delete its merged branches; never an unmerged one
without an explicit confirmation; keep the names in the archive; and a one-time clean-up of old
merged branches of a project. Also: after a successful Merge, offer to mark the goal done.

## Options considered

**What "merged" means**

1. **Ancestry only.** Cons: misses every squash-merged branch, which is all of them.
2. **Ancestry, or patch equivalence** (`git cherry`). Cons: a squash of several commits is not
   patch-equivalent to any one of them.
3. **Three classes:** *merged* (an ancestor of the target); *squashed* (its head is in a recorded
   goal merge whose commit is in the target, or merging it into the target changes nothing:
   `git merge-tree` gives the target's own tree, which also covers squash merges made by hand);
   *unmerged* (anything else).

**How a branch is deleted**: `git branch -D` (no check of what it points at) versus a
compare-and-swap `git update-ref -d refs/heads/<name> <head>` on the head the preview showed.

## Decision

Option 3 and the compare-and-swap. A branch checked out in any worktree, or whose agent is not
exited, is *in use* and never deleted. Only branches under `agent-office/` are listed or deleted.

- A done goal's modal and its archive record have "Clean up branches…": the branches of every
  agent of the goal and its sub-tasks that still exist. Merged and squashed are preselected;
  unmerged ones are listed apart, unselected, with how many commits only they have; choosing any
  asks a second time, naming each with its head. The request carries the unmerged names
  explicitly; the daemon refuses an unmerged branch that is not named.
- Settings → Project → Branch clean-up does the same for every `agent-office/*` branch.
- Every deletion is recorded in `deleted_branches` (migration 0017: project, goal, name, head,
  class, when) and logged; a goal's record lists its deleted branches with
  `git branch <name> <head>` to restore one. A deletion whose record fails is logged loudly with
  the head.
- Each requested branch is checked again under the merge lock: in use, moved since the preview,
  unmerged without being named, or not the goal's are refused with the reason; the rest proceed.
- After a successful Merge, the Merge dialog says what was merged and offers "Mark goal done"
  (with the running-agents warning of ADR 0054); nothing is marked done automatically. Once the
  goal is done, the same modal offers the clean-up.

## Consequences

- Deleting never loses a head: it is recorded, and an unmerged branch needs two deliberate steps.
- A branch whose agent is exited but not removed (sandbox mode) may be deleted; a later Resume or
  Import of that agent creates it again from the agent's own repository.
- The classification runs git per branch (`rev-list --count`, `merge-base --is-ancestor`,
  `merge-tree`); a project with hundreds of branches takes a moment for the preview.
