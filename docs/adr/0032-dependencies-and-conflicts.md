# 0032. Dependencies, file ownership and conflicts between parallel workers

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0017 (assign_task), ADR 0022 (a relaunch can resolve a conflict)

## Context

In Ozan's end-to-end test two workers on one goal both edited `internal/httpapi/handler.go`;
nothing noticed until the second pull request conflicted on GitHub. Every worker branched from
the main repository's HEAD, so a sub-task that needed another's code could not have it either.
Ozan asked for conflict handling built from the plan's dependencies and file ownership, QA's
integration branch (ADR 0033), `git merge-tree` checks in the main repository on `report_done`,
and relaunching a worker to resolve conflicts after another PR merges.

Constraints: the host never runs git inside an agent repository (ADR 0014); the user's checkout
is never touched; the model's word is not a guarantee, so order must be enforced where it costs
money (a session per start).

## Options considered

**Enforcing order**

1. **Prompt only** ("do dependent sub-tasks one after the other"). Cons: the Phase 8 e2e showed
   haiku does not reliably follow such rules.
2. **`assign_task` enforces it:** a sub-task with unmet dependencies is refused with what it waits
   for.

**When a dependency is met**

1. **When it is `done`** (merged by a human). Cons: every team run stalls on the user.
2. **When it is `review` or `done`** (reported with committed work), and the dependent branches
   *from that work*, so it has the code it needs.

**Parallel sub-tasks on the same files**

1. **Detect afterwards only.** Cons: the second worker's session is already spent.
2. **Refuse to run them at the same time** unless one depends on the other, from the files each
   sub-task declares; still detect afterwards for undeclared overlaps.

**Detecting conflicts**

1. **Try a merge in a scratch worktree.** Cons: a checkout per check; slow; touches disk state.
2. **`git merge-tree --write-tree`** in the main repository: the real merge algorithm, objects
   only.

**After a pull request merges**

1. **Tell the user.** Cons: the most mechanical case of follow-up work stays manual.
2. **Relaunch the worker of each now-conflicting pull request** to merge the new base (ADR 0022's
   relaunch, a bounded number of rounds), and offer the same by hand.

## Decision

Option 2 in every group.

- **Sub-task plan fields** (migration 0008): `depends_on` (task ids of the same goal), `files`
  (repository-relative paths, directories ending in `/`, globs; clean, no `..`), `acceptance`,
  `role`. From an approved spec (ADR 0031) or the lead's `create_task`.
- **`assign_task`** refuses a sub-task whose dependencies are not `review`/`done`, and one whose
  declared files overlap those of a *running* sub-task of the goal when neither depends on the
  other (transitively). Overlap errs towards overlap: equal paths, a path under a declared
  directory, a glob matching a path, two globs whose literal prefixes nest.
- **Base of a sub-task's branch:** one dependency's imported head; for several, folded in order:
  one containing the other is skipped, otherwise `merge-tree` + `commit-tree` make a merge commit
  in the main repository (refused, naming the files, if they conflict); without dependencies the
  goal's spec commit, else HEAD. The base is the agent's protected base (ADR 0014), so its commits
  are not checked or pushed as the worker's.
- **On `report_done`** (after the import and the move to `review`): `merge-tree` of the reported
  head against the base branch (`<remote>/<base>` if fetched, the local base branch, else HEAD) and
  against every other `in_progress`/`review` sub-task of the goal that has a branch and no
  dependency relation. Results are stored on both tasks (`tasks.conflicts`, `conflicts_checked_at`),
  shown on the task cards and in `list_tasks`, returned to the worker and sent to the lead with the
  report. Never a refusal.
- **After a merge on GitHub** (the poll loop sees it): fetch the base into
  `refs/remotes/<remote>/<base>` of the main repository, re-check every other open pull request,
  record conflicts (clear resolved ones) and, within **2 automatic rounds per pull request**
  (`pull_requests.conflict_rounds`), relaunch its worker: an idle worker the user is not typing in
  is ended first. **Resolve conflict** (`POST /api/tasks/{id}/resolve-conflict`) does the same by
  hand, also without GitHub.
- **The resolve round:** a sandboxed worker gets the new base as `refs/remotes/agent-office/base`
  in its repository, set by a helper container (the host does not run git there); a host worker
  merges the commit directly. Its prompt says to merge it, resolve the conflicts, run the tests
  and commit; `report_done` is refused until there is a new commit (the follow-up round of ADR
  0022, marked as a conflict round, so `report_no_change` does not apply). The report then pushes
  a fast-forward and the pull request updates.

## Consequences

- Order and file ownership hold whatever the model does; the model still chooses *what* to
  declare. Sub-tasks without declared files are not checked before they run, only after.
- Dependent pull requests are stacked: they contain their dependencies' commits until those
  merge. A dependency's later follow-up commits are not in the dependent branch; the conflict
  check shows if that matters.
- `merge-tree` checks are pairwise. Three branches that merge pairwise but not together are found
  by QA's integration branch (ADR 0033).
- A conflict-resolution session costs a session per round; the round limit and budgets (ADR 0027)
  bound it.
