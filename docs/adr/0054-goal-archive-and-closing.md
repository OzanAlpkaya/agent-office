# 0054. The board shows active goals; done goals are closed together and archived

- **Status:** Accepted
- **Date:** 2026-11-11
- **Amends:** ADR 0017 (what marking a goal done ends), ADR 0011 (tasks keep every status)

## Context

After four PulseGo sprints (Phase 17's source), most cards on the Board were done goals and their
sub-tasks: the running goal was hard to find. Closing a goal took one status change for the goal,
one per sub-task left in review, and one Remove per exited agent; workers of the goal kept running
after it was marked done (ADR 0017 left them alone because "one still working may hold work").
Nothing showed a finished goal as a whole: what it cost, who worked on it, what QA found.

Since Phase 16 an exiting worker's uncommitted work is committed on its branch (ADR 0051), so
stopping a worker no longer loses work.

## Options considered

**What the Board shows**

1. **Everything, with a filter for done.** Cons: the done sub-tasks of active goals still crowd
   the Done column, and a filter that hides done tasks hides finished sub-tasks of running goals.
2. **Active goals and all their sub-tasks; done goals elsewhere; a picker for one goal.** A goal's
   finished sub-tasks stay visible while the goal runs, and leave with it.

**Where finished goals go**

1. **A Done column that collapses.** Cons: still loads every card; nowhere to show a goal's record.
2. **An archive view** listing done goals, each opening into its record. Pros: room for agents,
   cost, QA and the report; nothing is deleted.

**What marking a goal done does**

1. **As before** (Tech Lead, architect, QA end; sub-tasks and workers untouched).
2. **Close the goal:** sub-tasks become done in the same transaction, every running agent of the
   goal and its sub-tasks ends (`goal_done`), reviewers of its pull requests finish their review.
   The UI asks first when an agent of the goal is busy (working, starting, waiting on a permission);
   an idle Tech Lead after its report is the normal case and is not asked about.

**Removing a goal's agents**

1. **Remove on marking done.** Cons: a dirty workspace would need a decision then and there; a
   resume of a done goal's worker becomes impossible by accident.
2. **One explicit action on the done goal** ("Remove n exited agents"), using the normal
   work-preserving removal (ADR 0010) for each.

## Decision

Option 2 in each case.

- The Board shows top-level tasks that are not done ("active goals"; plain tasks count as goals)
  and their sub-tasks of any status; a sub-task whose goal is missing stays visible. A picker shows
  one goal; the choice is remembered per browser and project (local storage, a convenience) and
  falls back to all active goals when that goal is done or gone.
- **Archive (n)** lists done goals, most recently closed first, with their date, sub-task count,
  cost, last QA verdict and merge. A goal opens into its record: description, the Tech Lead's report
  (summary and commit message, ADR 0055), sub-tasks with status and branch, every agent that worked
  on it including removed ones (name, role, CLI and model, how it ended), its cost (the usage the UI
  already loads, by goal), its QA runs, and its merges. `GET /tasks/{id}/record` builds it from the
  store (`ListGoalAgents` includes removed agents). Reopen moves the goal back to review.
- Marking a goal done closes it as in option 2 (`CloseChildTasks` in the status change's
  transaction; `endGoal` stops every running agent of the goal and its sub-tasks except reviewers).
- `POST /tasks/{id}/remove-agents` removes every exited agent of a **done** goal one by one with
  `Remove` and reports what was removed and what was kept and why (a dirty workspace, an error);
  running agents are not touched. The archive's record offers it.
- Nothing is deleted from the store: removed agents stay as rows with `removed_at` and appear in
  the record. As before, removing an agent deletes its saved terminal history (ADR 0036) and its
  workspace; branches with commits stay.

## Consequences

- The Board stays as long as the work in progress, not the project's history.
- Marking a goal done is now a real close: workers stop. The confirmation names how many busy
  agents will be stopped, and their uncommitted work is committed on exit (ADR 0051).
- The archive's agents and record come from the store, so they survive removals and restarts; its
  cost uses the existing usage records, which include removed agents' sessions.
- Tests: `TestReportDone` (sub-tasks done, the working worker ended), `TestRemoveGoalAgents`,
  `TestGoalRoutes`, Vitest `goals.test.ts`, Playwright `daily.spec.ts`; live in the workflow e2e.
