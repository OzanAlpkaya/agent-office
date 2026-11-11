# 0055. Merge and Push on the goal card: the user's squash merge of QA-passed work

- **Status:** Accepted
- **Date:** 2026-11-11
- **Amends:** ADR 0033 (what a passed QA run is used for), ADR 0017 (`report_done` of a Tech Lead)

## Context

A goal ends with QA's integration branch: every sub-task merged and tested (ADR 0033). In the
PulseGo sprints Ozan then squash-merged that branch into `main` by hand, with a commit message he
asked the Tech Lead to prepare, and pushed. The Tech Lead's prompt says merges and pushes are the
user's (ADR 0053's rule); the daemon's only pushes so far are agent branches for pull requests
(ADR 0020). The request: a Merge button when QA has passed, an optional Push button, both only on
the user's click after a confirmation that shows the branch, the target and the message, refused on
a dirty tree, a conflict or a target that moved, with a clear reason.

## Options considered

**How the merge is made**

1. **`git merge --squash` in the user's checkout, then `git commit`.** Cons: touches the working
   tree and index before anything is known to be clean; a conflict leaves the checkout half-merged.
2. **Plumbing first, then one move of the branch.** `git merge-tree --write-tree` computes the
   merged tree (refusing on conflicts), `commit-tree` makes the squash commit with the target as its
   only parent; nothing is touched until it exists. Then the branch moves: if it is checked out in
   the main repository, with `git merge --ff-only <commit>` (moves the branch and updates the files;
   refuses a non-fast-forward or an overwrite); if not, with a compare-and-swap `update-ref`.
3. **A pull request on GitHub.** Cons: needs `--github`; the projects that asked for this merge
   locally.

**"A target that moved"**

1. **Since QA tested:** refuse unless the target is an ancestor of QA's head. Cons: then a merge
   could never conflict, yet the request names conflicts separately; every other goal merged in
   between would force a new QA run.
2. **Since the confirmation:** the confirmation carries the target head and QA head it showed; the
   merge is refused if either changed. Commits on the target that QA did not test with are shown as
   a warning ("main has 3 commits QA did not test with"); a clean merge is allowed, a conflicting one
   refused.

**Where the commit message comes from**

1. **The goal's title and the lead's summary.** Cons: not a commit message.
2. **`report_done` gains `commit_message`** (Tech Lead only), stored with the summary. The
   confirmation shows it in an editable field; what the user confirms is committed.

## Decision

Option 2 in each case.

- `report_done` takes `commit_message` (1-4000 characters) from a Tech Lead; the role prompt asks for
  one in the repository's style. Every goal report is stored in `goal_reports` (migration 0016).
  Without a message the confirmation proposes the goal's title and the summary.
- `GET /tasks/{id}/merge` is the preview: QA's branch and head (the latest passed run's), the target
  (`merge_target`, default `main`) and its head, the files changed, untested target commits, whether
  the target is checked out, the message, the push remote and its URL, and `ready` with the refusal's
  reason. It changes nothing.
- `POST /tasks/{id}/merge {target_head, source_head, message}` repeats every check under a lock and
  merges only if the heads are what the confirmation showed. Refused (409, with the reason): the goal
  not in review or done, no passed QA run, QA's gate failing (ADR 0033), already merged, the target
  missing, checked out in another worktree, checked out with tracked changes or an unfinished merge,
  rebase or cherry-pick (untracked files are fine; git refuses itself if one would be overwritten), a
  conflict, no git identity, heads that moved. The commit is the user's (the repository's
  `user.name`/`user.email`) with an `Agent-Office-Goal: <id>` trailer. Each merge is recorded in
  `goal_merges` and published.
- `POST /tasks/{id}/push` pushes the goal's latest merge commit to its target branch on the project's
  `github_remote` (default `origin`): `git push --no-follow-tags <remote> <merge sha>:refs/heads/<t>`,
  exactly the merge the user confirmed (later local commits on the target stay local), never forced,
  without a terminal (ADR 0020); a rejected push is shown with git's reason. The push is recorded once.
- The preview lists the changed paths (up to 200) and flags those that run code outside the review:
  CI pipelines, git and package hooks, agent configuration (`.github/`, `.husky/`, `.githooks/`,
  `.claude/`, `CLAUDE.md`, ...). A remote URL's http(s) credentials are not shown. `merge_target` is
  accepted from this machine only, like `github_remote` (ADR 0046). (Security review, Phase 17.)
- Only the user starts them: no team tool exists for either, and the Tech Lead's prompt says it
  never merges or pushes. Any paired device may (it is the user acting, as with Accept and push on a
  pull request); both are logged with the commits.
- The goal card shows Merge… when the goal is in review (or done) and its latest QA passed on work
  not merged yet, then "Merged into main as abc12345" and Push…; the archive's record offers both too.

## Consequences

- The last manual step of a goal is one confirmed click, and the merge cannot be made from state the
  user did not see.
- The squash commit's tree is exactly QA's tested tree when the target did not move; when it moved,
  the user is told the combined result was not tested.
- The daemon now writes the user's target branch and, if checked out, its files; only through
  `git merge --ff-only`, which refuses rather than overwrites, and only after the checks above.
  The user's own repository hooks (`post-merge`) run as they would for the user's own merge.
- Pushing publishes; it is a separate click with its own confirmation naming the remote, its URL and
  the commit.
- Residual: in host mode an agent can write the main repository's git config and hooks, which the
  user's Merge (`post-merge`) or Push (`pre-push`, `pushurl`) then runs; host agents already run as
  the user, and refusing `core.hooksPath` would block repositories that use it. Sandboxed agents
  cannot reach the main repository's config (ADR 0014).
- Tests: `TestSquashMergeIntoCheckedOutBranch`, `TestSquashMergeRefusals`,
  `TestPushBranchToBareRemote` (git); `TestMergeGoalIntoCheckedOutMain` (every refusal, the merge,
  a rejected and a successful push to a local bare remote), `TestMergeGoalIntoBranchNotCheckedOut`,
  `TestMergeGoalNeedsAReport` (engine); `TestGoalRoutes` (API); `daily.spec.ts` (UI); the workflow e2e
  merges and pushes a real goal on a throwaway clone with a local bare remote.
