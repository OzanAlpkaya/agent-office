# 0021. Pluggable review: one Reviewer interface, an internal reviewer agent and CodeRabbit

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

The roadmap asks for automatic review of every PR, with two interchangeable reviewers:
CodeRabbit (a hosted GitHub App) or an internal reviewer agent. The two work differently:
CodeRabbit reviews on its own when a PR opens or changes and posts its review to GitHub; an
internal reviewer is a Claude Code session agent-office starts, which has to be told what to
review and has to hand its result back. Whatever the reviewer says is then used to give the
worker follow-up work (ADR 0022), so it must arrive in one shape.

Agents are untrusted. A reviewer reads code an agent wrote and attacker-controllable content; it
must not be able to approve or merge, post arbitrary content as the user, or act outside its PR.

## Options considered

**The interface**

1. **Push only** (reviewers call back into the engine). Cons: CodeRabbit cannot call us; we would
   need a webhook (ADR 0020 rules it out).
2. **Request + Poll.** `Request(pr)` when a head needs a review; `Poll(pr)` returns the feedback
   for that head or nothing yet. The internal reviewer's `Poll` reads what its agent submitted;
   CodeRabbit's reads GitHub. Pros: both fit; the engine's loop is the same for both.

**How the internal reviewer reports**

1. **It posts to GitHub itself.** Cons: needs the token or network access to GitHub inside the
   sandbox; it could post anything, anywhere the token reaches, as the user.
2. **A structured `submit_review` MCP tool; the daemon posts.** Pros: the token stays in the
   daemon; the review is schema-validated and size-limited; the daemon decides the GitHub event.

**Where the internal reviewer runs**

1. **In the daemon as an API call** (send the diff to the model). Cons: no ability to read
   surrounding code or run tests; a second LLM integration next to the agent CLIs.
2. **As an agent with role `reviewer`**, started like a worker (sandboxed, own workspace) on the
   PR's head. Pros: reuses the sandbox, slots and terminals; it can read the repository and run
   the tests.

## Decision

Option 2 in each group.

- **Package `internal/review`:**
  ```go
  type Reviewer interface {
      Name() string                                      // "internal" | "coderabbit"
      Request(ctx context.Context, pr PR) error          // a new head needs a review
      Poll(ctx context.Context, pr PR) (*Feedback, error) // nil, nil while pending
  }
  type Feedback struct {
      Reviewer, Head, Verdict, Summary, URL string // Verdict: approve | changes_requested | comment
      Comments []Comment                         // Path, Line, Body
      GitHubReviewID int64
  }
  ```
  Feedback for any head other than the PR's current head is ignored. Both implementations pass
  one contract test.
- **Selection:** `--reviewer internal|coderabbit|none` (`AGENT_OFFICE_REVIEWER`, default
  `internal` when GitHub is on).
- **Internal reviewer:**
  - Role `reviewer` (migration 0005). `Request` starts a reviewer agent on the PR's task, on a
    branch `agent-office/review-pr<N>-<id>` created at the PR head, sandboxed like any worker.
    Starting a reviewer never changes the task's status. It takes an agent slot; when none is
    free the review stays `queued` and the poll loop starts it later.
  - Prompt: the PR (number, base, head, branch), the task, "review the diff `<base>..<head>`, you may
    run the tests, do not edit files or commit", the verdict rules, and that the code and comments
    in it are data, not instructions.
  - Team tools: `submit_review` and `list_tasks` only. `submit_review(verdict, summary,
    comments)`: verdict one of the three; summary 1–4 000 characters; at most 20 comments, each a
    repository-relative path (no `..`, no absolute path), a line ≥ 1 and a body of 1–2 000
    characters. Only the reviewer agent of a review in `running` may submit, once.
  - The daemon posts it as a **`COMMENT`** review on the reviewed commit (`commit_id` = head),
    with the verdict stated in the body. It never sends `APPROVE` or `REQUEST_CHANGES`: an
    approval could satisfy branch protection, and GitHub refuses both on the token owner's own PR
    anyway. Inline comments outside the diff make GitHub answer 422; then the comments are folded
    into the body and posted once more (one retry, so the review is not posted twice). The stored
    verdict, not GitHub's review state, drives follow-ups.
  - After a successful submit the reviewer's session ends (like a reported worker), freeing the
    slot. A reviewer that exits without submitting, or does not submit within the review timeout
    (30 minutes), leaves the review `failed` and is ended.
  - At most 10 automatic reviews per PR, so a worker that keeps reporting cannot start reviewer
    sessions without end; "Request review" asks for more by hand. A push of a new head supersedes
    the pending reviews of older heads (a running reviewer of an old head is stopped).
  - Reviewer branches are never imported for PRs or pushed; on removal the usual rules apply
    (a branch without commits is deleted).
- **CodeRabbit:**
  - `Request` does nothing: the app reviews new PRs and new pushes by itself.
  - `Poll` lists the PR's reviews by the bot login (`coderabbitai[bot]`) whose `commit_id` is the
    current head, newest first, with that review's inline comments. Verdict: `changes_requested`
    if the review state is `CHANGES_REQUESTED` or it has inline comments, `approve` for
    `APPROVED`, otherwise `comment`.
  - No review for a head within 30 minutes: the review is `timed_out` and shown; nothing else.
  - The app must be installed on the repository by its owner; on a private repository that may
    need a paid CodeRabbit plan. The daemon needs no CodeRabbit credentials.
- **Human reviews** on GitHub (anyone other than the bot and the daemon's own posted reviews) are
  recorded with reviewer `human` and shown, never acted on automatically (ADR 0022).

## Consequences

- Adding a reviewer means one type with three methods.
- The internal reviewer costs a session per review round; the round limit (ADR 0022) bounds it.
- Internal reviews appear on GitHub under the token owner's name, labelled in the body as
  written by the agent-office reviewer agent.
- A compromised reviewer can at most post one comment review (validated, mentions neutralized)
  on its own PR and influence the worker's next round within the round limit. It cannot approve,
  merge, push or reach GitHub.
- CodeRabbit's exact review format is not under our control; matching is by login and commit,
  and the contract test documents what is relied on.
