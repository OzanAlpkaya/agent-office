# 0062. The sub-task limit counts open work; superseded sub-tasks close

- **Status:** Accepted
- **Date:** 2026-11-12

## Context

A goal had at most 20 sub-tasks (ADR 0017), counted over every sub-task it ever had. Bouncibo's polish
rounds and QA bugs reached 20 with most of them done, and the Tech Lead could not file the next fix; the
cap was raised by hand to 40 in a local build. Sub-tasks the lead replaced with a new one stayed "in
progress" without an agent ("No agent" on the board) forever, and still counted.

## Options considered

1. **Raise the constant**: hides the problem until the next long goal; replaced sub-tasks still linger.
2. **Count open sub-tasks only, per project, and close replaced ones**: the limit bounds work in flight,
   which is what it is for; a separate ceiling bounds the total against a runaway loop.
3. **A task status "superseded"**: clearer in queries, but the status set is a CHECK constraint, the
   board's columns and every status check in the engine and the UI would change.

## Decision

Option 2, with superseding as "done plus a reason" rather than a new status (option 3).

- **Open** sub-tasks are to do, in progress and blocked. Reported (review), done and superseded ones do
  not count: a reported sub-task holds finished work. The limit is the project setting `max_sub_tasks`
  (1-100, default 20, a live setting, ADR 0061); `create_task`, QA's bug filing, plan validation, plan
  approval and the lead's prompt (`{max_sub_tasks}`) use it. `MaxTotalSubTasks = 200` bounds all of a goal's
  sub-tasks.
- `create_task` takes `supersedes`: sub-tasks of the goal, not done, with no running agent (else "stop_agent
  first"). Each is closed as done with `tasks.superseded_by` = the new task, and frees its place for the
  new one. An approved plan revision that drops a sub-task already worked on, with nobody on it, closes it
  with `superseded_by = "spec:<version>"`; one with a running agent is kept, as before.
- A superseded sub-task is not reported work: QA does not merge it and the QA gate does not ask for it. A
  dependency on it is a dependency on its replacement (followed through a few hops): it waits for it and
  starts from its work; one a revision dropped is satisfied.
- The board shows "Superseded" with what replaced it, and the modal links to the replacement.

## Consequences

- Long goals with many rounds keep working without changing code; a project that wants a tighter or
  looser limit sets it.
- A lead that forgets `supersedes` still leaves an open sub-task behind; the prompt and the limit's error
  name the field.
- `superseded_by` is free text by design (a task id or `spec:<n>`); readers check the prefix.
