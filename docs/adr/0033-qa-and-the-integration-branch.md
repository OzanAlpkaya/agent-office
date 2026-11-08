# 0033. QA: an integration branch per goal, bugs as sub-tasks, a gate on the goal's report

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0017 (the goal's report_done needs a passing QA run)

## Context

Ozan (2026-11-08): QA, after workers report, merges their branches into an integration branch
inside the sandbox, runs the full test suite, adds tests for the plan's acceptance criteria, runs
the app and exercises it, and files bugs as sub-tasks for the Tech Lead. Distinct from the
Reviewer, which reads one pull request's diff.

Pairwise conflict checks (ADR 0032) miss combinations, and each worker only tests its own branch:
nobody runs the sub-tasks together before the user does.

## Options considered

**Where the integration happens**

1. **In the main repository by the daemon** (merge commits made with plumbing). Cons: the daemon
   cannot resolve conflicts or run tests; tests must run in a sandbox anyway.
2. **In QA's own sandboxed workspace**: its clone starts at the goal's base with every reported
   sub-task branch present as a branch, and QA merges them with git like a person would.

**Who starts QA**

1. **The daemon, when the last sub-task reports.** Cons: the lead may want to fix something first;
   bug sub-tasks would trigger runs in a loop.
2. **The Tech Lead with `start_qa`**, refused while a sub-task is in progress.

**What QA's verdict gates**

1. **Nothing** (advice). Cons: the lead can report a goal QA failed.
2. **The goal's `report_done`:** a passing run on the current heads of the reported sub-tasks,
   and no open bug sub-task from QA.

## Decision

Option 2 in each group.

- **`start_qa`** (Tech Lead): refused while a sub-task is `in_progress` or when none is reported
  with a branch. Starts the `qa` role (behaviour `qa`, one at a time) on the goal, on
  `agent-office/qa-<slug>-<id>` at the goal's base (spec commit or HEAD). In sandbox mode the
  reported sub-task branches are created in its repository when the clone is set up
  (`CreateSharedCloneWith`, before the agent has access; their objects come from the main
  repository through alternates); host worktrees share the main repository's branches. Its prompt
  lists the branches in dependency order and every reported sub-task's acceptance criteria. A
  `qa_runs` row records the branches and heads it was given.
- **`report_qa(verdict, summary, bugs)`**: `pass` or `fail` (a failure needs at least one bug); at
  most 10 bugs. The integration branch must be committed and is imported like a worker's (ADR
  0014); a `pass` is refused unless every branch QA was given is an ancestor of its head. Each bug
  becomes a `todo` sub-task (`Bug: <title>`, created by QA, within the 20 per goal; the rest are
  listed in the message). The run is finished once, the lead gets the verdict,
  summary and bug ids, and QA ends after its turn. A QA agent that ends without reporting leaves
  its run `error`; a daemon restart does the same to runs left `running`.
- **Gate** (on by default; `--qa=false` / `AGENT_OFFICE_QA=false` turns it off): the lead's goal
  report is refused unless the latest run passed, every reported sub-task's current head equals
  the head that run merged, and every bug sub-task QA filed is `review` or `done` (fixed, or closed
  by the user). Independently of QA, every sub-task of the goal must be `review` or `done`, so
  planned work cannot disappear silently (the user closes what is not needed).
- `GET /api/qa-runs`; runs are in the `/api/events` snapshot and `qa_run` updates.
- The integration branch is never pushed by the daemon; worker pull requests stay the unit of
  review. QA's added tests live on the integration branch (backlog: offering it as a pull
  request).

## Consequences

- The goal reaches `review` only after its sub-tasks were merged and tested together, which is
  where pairwise checks fall short.
- A QA run costs a session per round; the gate makes the lead pay it at least once per goal.
  `--qa=false` is there for small projects and quick experiments.
- QA's tests do not reach the default branch through the worker PRs. Until there is a PR for the
  integration branch, the user merges it by hand if they want them.
