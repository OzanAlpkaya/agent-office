# 0031. The architect's plan: structured, versioned, approved by the user, committed as a spec

- **Status:** Accepted
- **Date:** 2026-11-08
- **Amends:** ADR 0017 (an approved plan creates a goal's sub-tasks), ADR 0022 (architects can be
  relaunched)

## Context

Ozan (2026-11-08): an Architect talks with the user before work starts, asks clarifying
questions, reads the repository read-only, and produces a plan (design, sub-tasks, dependencies,
files each sub-task touches, acceptance criteria) saved as `docs/specs/<goal>.md`, editable and
approvable in the UI; optional for small goals. The Tech Lead executes the approved plan and
escalates to the Architect if it breaks.

Forces:

- The plan drives deterministic behaviour (which sub-tasks exist, in which order they may start,
  which files they own, what QA checks), so it must be machine-readable and validated, not prose.
- The user must see and change it before money is spent on workers.
- The daemon must not commit to the user's branches or touch their working tree (CLAUDE.md, ADR
  0014), yet the spec should end up in the repository.
- The architect is read-only (ADR 0029): it cannot write the file itself.

## Options considered

**How the plan reaches agent-office**

1. **The architect writes `docs/specs/<goal>.md`** on a branch. Cons: needs a read-write
   architect; the daemon would parse Markdown to learn dependencies.
2. **A structured `submit_plan` tool**; the daemon validates it, stores it and renders the
   Markdown. Pros: one source for the UI, the sub-tasks and the file; validation errors go back to
   the architect.

**Where the spec lives**

1. **Only in the database.** Cons: not in the repository, as asked.
2. **Committed into the user's checkout or default branch.** Cons: the daemon would commit to the
   user's branch.
3. **Database as the editable source; on approval, a commit on a spec branch** built with plumbing
   (no checkout), which every sub-task branch starts from. Pros: the spec is in every branch, so
   workers can read the whole plan, and it reaches the default branch with the first merged PR;
   nothing of the user's moves.

**What approval does**

1. **Only marks it approved**; the Tech Lead creates the sub-tasks. Cons: the model re-types the
   plan and may drift from it.
2. **The daemon creates the sub-tasks from the plan**, ends the architect and starts the Tech Lead.

## Decision

Option 2, option 3 and option 2.

- **`submit_plan`** (architects only, for their goal): `summary`, `design` (Markdown), `risks`,
  `sub_tasks[]` with `key`, `title`, `description`, `role` (a worker-behaviour role), `depends_on`
  (keys), `files` (paths, directories ending in `/`, globs) and `acceptance` (criteria). Validated:
  1–20 sub-tasks, unique keys, dependencies that exist, no self-dependency, no cycle (the cycle is
  named), clean relative file entries, known worker roles, sizes. A valid plan becomes a new
  **proposed version** in `specs` (earlier proposals are superseded) with its rendered Markdown,
  and an agent-office inbox item tells the user (ADR 0030). The architect waits for questions,
  change requests or approval.
- **The user** reads the plan on the goal card, **edits** it (a new version by the user,
  `PUT /api/specs/{id}`) or **approves** a proposed version (`POST /api/specs/{id}/approve`).
- **Approval**, in this order:
  1. The Markdown is committed as `docs/specs/<goal-slug>.md` on top of the previous approved
     version or the main repository's HEAD, with a temporary index file (`hash-object`,
     `read-tree`, `update-index`, `write-tree`, `commit-tree`; the user's identity, an
     `Agent-Office-Goal` trailer). No working tree is touched. The branch
     `agent-office/spec-<slug>-<goal id>` is moved by compare-and-swap once the transaction below
     has recorded the approval, so a refused approval leaves no commit on it.
  2. In one transaction: the version becomes `approved` (the previous approved one `superseded`)
     and the sub-tasks are applied: new keys are created (dependencies resolved to task ids);
     sub-tasks still `todo` that no worker ever touched are updated, or deleted if the plan
     dropped them; anything already worked on is kept and listed.
  3. Running architects of the goal end (`plan_approved`). A running Tech Lead gets a message with
     what changed; otherwise one is started with the plan in its prompt (if no slot is free, the
     result says so and the user starts it).
- **Sub-task branches start from the spec commit** (or from their dependencies' work, ADR 0032).
- **Escalation:** `send_message(to="architect")` from the Tech Lead relaunches an exited architect
  (same agent and workspace, new session, ADR 0022's path, now also for architects) with the
  approved spec in its prompt; the lead's message is delivered after its first turn. A revision
  goes through the same approval. At most 3 such relaunches per goal; then the lead is told to tell
  the user.
- **Limits:** at most 5 plan versions per goal per 10 minutes (the architect's and the user's
  edits) and 30 in all, since each is stored and announced.
- `GET /api/specs` lists every version; specs are in the `/api/events` snapshot and `spec`
  updates.

## Consequences

- The plan the user approved is the plan that runs: sub-tasks, order and files come from it, not
  from a model re-reading it.
- Every PR of the goal carries the spec commit until the first one merges; GitHub shows it in
  the PR's commits. With squash merges the spec arrives with the first PR; later PRs add the same
  file with the same content, which merges cleanly.
- A revised plan cannot pull work out from under a running worker: started sub-tasks are kept and
  reported as such.
- The architect is optional: "Start lead" skips it, and the lead plans with `create_task`
  (with dependencies and files, ADR 0032) as before.

## Amendment (Phase 19, 2026-11-12): changes by request, light edits, the Architect's exit

Changing a plan meant answering the Architect in the inbox or opening one form with every field of every
sub-task. For small changes that was too much.

- **Request changes**: a box on a proposed or approved version (`POST /api/specs/{id}/changes`, at most
  4000 characters, within the plan rate limit). The text is stored on the version (`specs.change_request`,
  migration 0019) and shown as "Changes requested, waiting for the Architect" until a newer version exists.
  A running Architect gets it as the user's message; an exited one is started again on the same agent and
  workspace, with the plan and the request in its prompt; with no Architect, a new one starts. The
  Architect answers with `submit_plan`. User requests do not count against the escalation relaunch limit.
- **Approve** stays one click, at the top of a proposed plan.
- **Light edits**: each sub-task has Edit (title, description and acceptance criteria, with key, role,
  dependencies and files under "More") and Remove (asked once; dependencies on it are dropped). The summary,
  design and risks have their own Edit. "+ Sub-task" adds one. Each save is a new proposed version, as before.
- **The Architect exits**: besides being stopped on approval, an idle Architect ends with `plan_approved`
  when its goal has an approved plan, no proposed version, no message waiting for it and no unanswered
  question to the user, checked a few seconds after its turn ends so a message on its way wins. This covers
  an Architect started again for an escalation that only answered the lead. `--restore-agents` does not
  resume an Architect whose plan is approved and has no proposed version.

## Amendment (Phase 20, 2026-11-13): a change request withdraws a proposed version

Requesting changes on a **proposed** version now marks it superseded in the same update that stores the
request, and marks the goal's unread "plan ready" notices read. Its approval no longer waits on the user:
the Approvals badge, "Approve plan" and the Architect's "needs you" go away at once, and Approve refuses
it. The goal keeps showing that version, labelled "changes requested" with the request, until the
Architect submits the next one, which is the only version that asks for approval. A request on an
**approved** version leaves it approved (its work goes on). The Architect's prompt still says which
status the version had when the user asked; its idle end and restore treat a newest version with a
request as unanswered, whatever its status.
