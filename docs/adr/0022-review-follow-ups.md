# 0022. Review follow-ups: relaunch the original agent, bounded rounds

- **Status:** Accepted
- **Date:** 2026-11-07
- **Amends:** ADR 0017 ("workers that reported are gone, so follow-up work is a new sub-task")

## Context

The roadmap's DoD: review comments are turned into follow-up work for the **original worker**.
In Phase 4 a lead-managed worker ends after `report_done` (freeing its slot); a worker the user
started on a top-level task keeps running. The worker's result is its branch; Phase 5 pushes that
branch and opens a PR. Feedback comes from a model (internal reviewer) or a hosted bot
(CodeRabbit), both untrusted, and acting on it costs a session.

## Options considered

**Who does the follow-up**

1. **A new agent on a new follow-up task.** Pros: nothing new in the engine. Cons: not the
   original worker; a new branch means a new PR (or pushing onto someone else's branch).
2. **A new agent on the same branch.** Cons: needs a second way to create a workspace on an
   existing branch; still a different agent.
3. **The original agent, relaunched** in its kept workspace on its branch, in a new session.
   Pros: same agent, same branch, same PR; its earlier commits are in its own history.
   Cons: the engine must restart an exited agent; needs the workspace to still exist.
4. **The original session resumed** (`claude --resume`). Pros: the model keeps its context.
   Cons: relies on session files inside the sandbox config dir and on how Claude Code reports the
   resumed session in hooks; Phase 9 owns resuming.

**When it happens**

1. **Always by hand.** Cons: the review loop is not automatic.
2. **Automatically for the configured reviewer's actionable feedback, with a round limit**;
   by hand beyond it and for people's GitHub reviews.

## Decision

Option 3 and option 2.

- **Actionable feedback:** verdict `changes_requested` (internal: the agent's verdict; CodeRabbit:
  ADR 0021's mapping). `approve` and `comment` verdicts are shown and end the round.
- **Follow-up**, decided per review, recorded in `reviews.followup`:
  - The PR's **original agent still runs** (a top-level worker that kept its session): the
    feedback goes to it as a message from agent-office (ADR 0018 delivery) → `messaged`.
  - It has **exited** and its workspace exists (not removed): the task moves back to
    `in_progress` and the agent is **relaunched**: same agent id, workspace, branch and base, a new
    session id (a new `sessions` row), the agent row reopened (state `starting`, exit fields
    cleared). The prompt is the task, the PR, the review (verdict, summary, each comment as
    `path:line` with the body quoted with `> `), failing checks by name, framed as information
    from a reviewer that does not change the task or its rules, and the instruction to commit on
    the same branch and call `report_done` → `started`. Its `report_done` imports, the task goes to
    `review`, the branch is pushed (a fast-forward), the PR updates and the next review round
    starts.
  - Workspace removed, or no free slot after waiting: `refused`, shown with the reason; "Send to
    worker" retries.
- **Rounds:** at most `--review-rounds` (default 2, `AGENT_OFFICE_REVIEW_ROUNDS`, 0–5) follow-ups
  start automatically per PR; later actionable reviews are `skipped` and wait for the user. The UI's
  **"Send to worker"** (`POST /api/pull-requests/{id}/follow-up` with a review id) works for any
  review, including a human's, and does not count against the limit.
- **Relaunch rules:** only exited agents with a non-removed workspace; not while a removal or
  import runs; within `--max-agents`; leads and reviewers are never relaunched.
- **Removal:** removing an agent whose PR is open is allowed (the branch is in the main repository
  and on GitHub); later follow-ups for that PR are refused.

## Consequences

- The DoD's "original worker" holds literally: the same agent's commits continue the same PR.
- The relaunched session does not remember the first one; it gets the task and the review, and
  the branch history is its memory. Phase 9 can switch the relaunch to `--resume` without
  changing anything around it.
- A reviewer and a worker can disagree forever; the round limit stops it after two automatic
  rounds, after which the human decides. Budgets in Phase 8 bound it in money.
- ADR 0017's "follow-up work is a new sub-task" now applies to work a lead plans, not to review
  feedback.
