# 0063. Bases for follow-up work, and ports after a squash merge

- **Status:** Accepted
- **Date:** 2026-11-12

## Context

A sub-task started from its dependencies' work, else from the approved spec commit (ADR 0031, 0032). QA
started from the spec commit and merged every reported sub-task branch (ADR 0033). Two things went wrong in
Bouncibo:

1. Polish rounds, QA bugs and the lead's own fixes are sub-tasks created after the plan. They started from
   the spec commit, without the goal's finished work, so a fix for a bug QA found did not contain the code
   the bug was in.
2. The Merge button squash-merges the QA branch into the target (ADR 0055). Follow-up work in the same goal
   kept building on the pre-squash branches. The target then held the same changes as one squash commit,
   with other history, so the next merge saw every line the follow-up changed again as a conflict. Goal 02
   had 35 of them, none of them real.

## Options considered

1. **Rebase the old branches onto the target after a merge**: rewrites branches agents still work on, in
   sandboxed clones the daemon only imports from (ADR 0014).
2. **Merge (not squash) goals**: keeps history related, but drops the one-commit-per-goal target that the
   user chose (ADR 0055).
3. **Choose the base deterministically, and port old work at QA time**: new work starts where it should.
   Work that already exists on the old history is three-way merged onto the target with the
   already-merged part as the merge base (`git merge-tree --merge-base`, git 2.40+): only its new changes
   move. Nothing is rewritten; ports are new commits on branches of their own.

## Decision

Option 3.

- **The goal's latest finished work** is the latest finished QA run's head (passed or failed: a failed
  run's integration branch is where its bugs are), else the spec commit (else HEAD). Every reported or
  done, not superseded, sub-task head not in it yet is folded on top without a working tree. A head that
  does not fold cleanly is left out and logged.
- **A follow-up sub-task** is one that is not from the approved plan (no `spec_key`), or any sub-task once
  the goal was merged. With no dependencies it starts from the goal's finished work; with dependencies
  their work is folded onto it. Plan sub-tasks before a merge start from the spec commit as before, so
  they run side by side. A sub-task taken back from an exited worker continues its branch (ADR 0051).
- **The merged cut**: a goal's recorded merges whose commits the target contains. The merged heads are
  their QA heads and every sub-task head those QA runs merged. Once a cut exists:
  - the finished work and QA start from the target's current head;
  - QA merges only sub-tasks whose head is not in a merged head;
  - a head that is not merged and does not contain the latest merge commit is **ported**: merge-tree onto
    the target head, with the newest common ancestor of the head and the merged heads as the base, then
    committed with the target head as its only parent. QA merges it from `agent-office/port-<task>-<sha>`,
    and the run records both heads (`head` for the QA gate, `merge` for the pass check). A port that
    conflicts refuses `start_qa` with the files, so the lead assigns a sub-task that resolves them;
  - the QA gate treats merged sub-tasks as tested;
  - QA gets the acceptance criteria of the sub-tasks it merges in this run only: the merged ones were tested,
    and a follow-up may change what they did (the first e2e run failed a polish round three times against
    the criterion it replaced). A superseded sub-task's criteria are never given;
  - report-time conflict checks (ADR 0032) compare the port, so the squash itself is no conflict.

## Consequences

- A follow-up round after a squash merge merges cleanly unless the work really conflicts.
  `TestFollowUpAfterSquashMergeMergesCleanly` covers a new sub-task and a sub-task continued on its
  pre-squash branch (whose plain merge into `main` conflicts). It fails without the cut and without ports.
- Port and fold commits are created in the main repository: ports keep a branch until branch clean-up
  (ADR 0059) deletes them with the goal's other merged branches. Fold commits are unreferenced objects.
- Two merges of a goal must target the same branch for the cut to see the first; changing `merge_target`
  in between starts over from the spec's history.
