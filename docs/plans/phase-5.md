# Phase 5 plan: GitHub loop + pluggable code review

- **Status:** Done on `phase-5/github-review`, waiting for Ozan's review; not merged. Written 2026-11-07 with the recommended option
  for every open question and executed straight away, as Ozan asked ("write the Phase 5 plan using
  your recommended options, then execute it immediately without asking me anything"). Every
  decision is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 5. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites (done by Ozan):** the playground is on GitHub at
  `OzanAlpkaya/agent-office-playground` (private, pushed over SSH via the `github-personal` host
  alias); a fine-grained token limited to that repository is in
  `~/.config/agent-office/github-token` (mode 0600).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A finished worker task becomes a pull request on GitHub, and the pull request is reviewed
automatically by a pluggable reviewer: an **internal reviewer agent** (a sandboxed Claude Code
session that reads the branch and submits a structured review) or **CodeRabbit** (a GitHub App
whose review the daemon reads). Review feedback goes back to the **original worker**, which is
relaunched in its own workspace on its own branch, so its follow-up commits update the same pull
request. **Merging stays a human action**: no code path in agent-office merges, and agents never
see the GitHub token.

The model decides what to write in a review and how to address it. Everything that touches
GitHub, spends sessions or moves state (when to push, what to push, which feedback counts, how
many review rounds run on their own) is decided by deterministic Go code in the daemon.

## Out of scope

- Merging, auto-merge, approvals. The internal reviewer posts **comment** reviews only.
- Webhooks: the daemon listens on loopback only, so it polls GitHub instead.
- GitHub Issues as a task source, PRs for goals (a goal has one PR per sub-task), stacked PRs.
- Acting on review comments from people automatically. A human's comments on GitHub are shown
  and can be sent to the worker with one click, but never trigger a session by themselves.
- Setting up CI or CodeRabbit on the playground (a change to that repository's `main`; Ozan's
  call). Checks are read and shown when they exist.
- Resuming a Claude Code session (`--resume`, Phase 9): a follow-up is a new session of the same
  agent.
- Codex.

## Approach

1. **GitHub client (`internal/github`, go-github v93.0.0).** A small wrapper with exactly the
   operations needed: repository default branch, find-or-create PR, get PR, list reviews and
   review comments, post a review, read check runs and the combined status. No merge method
   exists. The **token file** follows ADR 0015: path from `AGENT_OFFICE_GITHUB_TOKEN_FILE`
   (default `$XDG_CONFIG_HOME/agent-office/github-token`), regular file, owned by the user, mode
   0600/0400, directory not writable by others; read for every GitHub operation and never kept in
   memory between operations, so a rotated token takes effect at once. HTTP timeouts on every
   call; GETs retried with backoff on network errors and 5xx; POSTs never retried blindly (PR
   creation is made idempotent by looking up the branch's PR first). The token is only ever sent
   to the configured API origin (go-github's `WithAuthToken`).
2. **Configuration.** GitHub is **opt-in**: `--github` (`AGENT_OFFICE_GITHUB=true`). The
   repository comes from the main repository's push remote (`--github-remote`, default `origin`),
   parsed strictly (`git@<alias>:owner/name.git`, `ssh://…`, `https://github.com/…`); the PR base
   is the GitHub repository's default branch. At startup the daemon checks the token file and the
   repository (one `GET /repos/{owner}/{repo}`) and fails with a clear message otherwise.
3. **Push (the daemon, from the main repository, over SSH).** `git push --no-follow-tags <remote>
   refs/heads/<branch>:refs/heads/<branch>`, never `--force`: imports never move a branch
   backwards (ADR 0014), so a follow-up is always a fast-forward. Pushes run without a
   controlling terminal (`setsid`), so an SSH passphrase prompt fails fast instead of hanging, and
   with a timeout. The agent repository is never pushed from, and agents have no route to GitHub
   (no credentials, `github.com` not on the egress allowlist).
4. **When a PR opens.** Automatically when a **worker** reports its task done with commits
   (`report_done`, after the import), and on demand ("Open PR") for a task in review whose agent
   has commits (e.g. a host-mode worker the user moved to review). Never for leads or reviewers.
   Gates, all deterministic:
   - **Flagged commits** (ADR 0014's author/trailer check) block the push until the user accepts
     those exact commits in the UI ("Accept and push").
   - A **diverged import** (`-rewrite-N` branch) is not pushed automatically; the user opens its
     PR by hand.
   The PR's title is the task title; the body has the task description, the worker's summary and
   an agent-office footer. Agent-written text in the body has `@mentions` and issue-closing
   keywords neutralized, so an agent cannot notify people or close issues on merge.
5. **Pluggable review (`internal/review`).** One interface:
   ```go
   type Reviewer interface {
       Name() string
       Request(ctx context.Context, pr PR) error           // a new head needs a review
       Poll(ctx context.Context, pr PR) (*Feedback, error) // nil while the review is pending
   }
   ```
   `Feedback` is the same for both: reviewer, reviewed head, verdict (`approve` /
   `changes_requested` / `comment`), summary, inline comments (path, line, body), URL.
   - **Internal reviewer:** `Request` starts a sandboxed agent with the new role **`reviewer`**
     on a review branch at the PR head, with a review prompt (diff against the base, run the
     tests if useful, do not edit or commit). Its only team tools are `submit_review` and
     `list_tasks`. `submit_review(verdict, summary, comments[])` is validated by the engine, then
     the **daemon** posts it to GitHub as a `COMMENT` review on the reviewed commit (inline
     comments; on a 422 the comments are folded into the body), stores it, and ends the reviewer.
     When all slots are busy, the review is queued and started when a slot frees.
   - **CodeRabbit:** `Request` does nothing (the app reviews on its own). `Poll` reads the PR's
     reviews by the configured bot login (`coderabbitai[bot]`) for the current head, with their
     inline comments. No review within 30 minutes marks it `timed_out` in the UI.
   `--reviewer internal|coderabbit|none` (`AGENT_OFFICE_REVIEWER`, default `internal`). Both
   implementations pass one shared contract test.
6. **Feedback → follow-up for the original worker.** When feedback is actionable (verdict
   `changes_requested`, or CodeRabbit inline comments), the task goes back to `in_progress` and
   the original agent is **relaunched**: same agent, same workspace, same branch, a new session
   whose prompt carries the review (framed as information from a reviewer, comments quoted, like
   ADR 0018 messages), failing checks by name, and the instruction to commit and `report_done`.
   Its report imports and pushes the new head; the PR updates; the next review round starts. If
   the agent is still running (a top-level worker that kept its session), the feedback is
   delivered as a message instead. If its workspace was removed, the follow-up is refused and
   shown. **At most 2 automatic review rounds per PR** (`--review-rounds`); after that, or for a
   human's GitHub review, the UI offers "Send to worker".
7. **Polling.** One loop in the engine: every 30 s for PRs waiting for a review or checks, every
   2 minutes for other open PRs; it reads PR state, checks and reviewer feedback. A PR **merged
   on GitHub** (a human decision) moves its task to `done`; a closed PR is recorded and nothing
   else happens. Rate-limit errors back the loop off.
8. **Restart.** PR and review rows persist; polling resumes at startup. A reviewer agent that
   died with the daemon leaves its review `failed`; it is requested again once.

## Changes

- `internal/store`: migration `0005_github.sql`: `agents.role` gains `reviewer` (add a new column,
  copy, drop the old one, rename; no table rebuild, so no foreign-key cascades), tables
  `pull_requests` and `reviews`; `queries.sql`; `make generate`; a row-survival test.
- `internal/secret` (new, small): the token-file reader and `Secret` type moved out of
  `internal/sandbox` (which keeps thin wrappers), so the GitHub token gets the same checks.
- `internal/github`: client, remote parsing, text neutralizing, tests against an `httptest` fake
  of the GitHub API. New dependency: `github.com/google/go-github/v93` v93.0.0 (verified in the
  module cache; brings `github.com/google/go-querystring`).
- `internal/git`: `Push` (no force, no tags, no tty, timeout), `RemoteURL`.
- `internal/review`: the interface, `Feedback`, both implementations, the contract test.
- `internal/mcp`: `submit_review` tool and the `reviewer` role's tool set.
- `internal/engine`: reviewer role and start options (base commit, branch), relaunch of an exited
  agent, PR submission after `report_done`, push gates, review requests and queue, follow-ups,
  the poll loop, restart semantics.
- `internal/api`: PR and review endpoints (`GET /api/pull-requests`, `POST
  /api/tasks/{id}/pull-request`, `POST /api/pull-requests/{id}/review`, `…/follow-up`), PRs in the
  snapshot, `pull_request` / `review` updates.
- `cmd/agent-office`: flags and env, startup checks.
- `web/`: PR link, state, checks and review status on task cards; reviewer badge; "Open PR",
  "Accept and push", "Request review", "Send to worker"; review list.
- `test/e2e`: `TestGitHubEndToEnd` (opt-in, pushes to the playground on GitHub).
- Docs: ADRs 0020–0022, architecture and threat model, `.env.example`, `docs/plan.md` scope wording
  (token file instead of a token in the environment), `PROGRESS.md`, this file.

## Data / API changes

- Migration 0005: `agents.role` CHECK becomes `lead / worker / reviewer` (every row keeps its
  role); new tables:
  - `pull_requests`: project, task, agent (the worker), branch, base, number, url, state
    (`pending / open / merged / closed / failed`), head, checks summary, accepted flagged
    commits, automatic review rounds, last error, timestamps; unique per project and branch.
  - `reviews`: pull request, reviewer (`internal / coderabbit / human`), head, status
    (`queued / running / done / failed / timed_out`), verdict, summary, comments (JSON), reviewer
    agent, GitHub review id, follow-up (`'' / started / messaged / skipped / refused`), timestamps.
- `AgentInfo.role` may be `reviewer`; `Task` gains nothing (PRs reference tasks).
- New endpoints above; `GET /api/status` reports GitHub (repository, reviewer) when enabled.

## Risks & edge cases

- **The fine-grained token's permissions are not visible to the daemon.** Needed: Metadata
  (read), Pull requests (read/write), Commit statuses and Checks (read; Checks may not be offered
  to fine-grained tokens, so a 403 there shows "checks unavailable" instead of failing).
  Recommended: **no Contents write**, so the token cannot merge even by mistake (push goes over
  SSH). Recorded in `.env.example` and ADR 0020.
- **The base commit is not on the remote base branch** (unpushed local commits): the PR then
  shows those commits too. Documented; agents start from the main repository's HEAD as before.
- **CodeRabbit on a private repository** may need a paid plan or trial and must be installed by
  Ozan. It is covered by the contract test against a fake GitHub API; a live run is an opt-in e2e
  subtest that is skipped until the app is installed.
- **Review ping-pong** costs sessions: bounded by the automatic round limit; budgets come in
  Phase 8.
- **Prompt injection through reviews**: only the configured reviewer's feedback acts on its own;
  feedback is framed as information and quoted; it cannot change the worker's authority. A
  human's GitHub comments need a click.
- **Haiku's verdict is not deterministic.** The e2e checks the whole chain with whatever the
  reviewer decides; if it does not request changes, the follow-up path is exercised through
  "Send to worker" with a review the test posts. The engine tests cover both branches
  deterministically with fake agents and a fake GitHub.
- **Pushing to the real GitHub repository from tests**: only the opt-in e2e does it, on branches
  named `agent-office/…`; it closes its PRs and deletes its branches afterwards unless
  `AO_E2E_KEEP=1`.

## Test plan

- `internal/github`: every operation against an `httptest` GitHub fake: auth header only to the
  API origin, timeouts, retry of GETs only, find-or-create idempotency (existing PR, 422 "already
  exists"), review post with inline-comment fallback on 422, checks with a 403, rate-limit errors;
  remote URL parsing; mention neutralizing; a test that no request ever hits a merge endpoint.
- `internal/secret`: the existing token-file tests, moved.
- `internal/git`: push to a local bare remote (fast-forward, refuses non-fast-forward, no tags).
- `internal/review`: one contract test run against both implementations (pending → feedback for
  the current head; stale-head reviews ignored), plus CodeRabbit filtering by login and head.
- `internal/mcp`: `submit_review` schema and validation; tool sets per role.
- `internal/store`: migration 0005 keeps every row and role.
- `internal/engine` (fake `claude`, fake GitHub, local bare remote): report_done → push → PR;
  flagged commits block until accepted; diverged imports need a manual PR; reviewer start,
  queueing when slots are full, `submit_review` authorization (only the reviewer of that PR),
  review posted as COMMENT; actionable feedback relaunches the original agent in its workspace
  with a new session and the task back in progress; running worker gets a message; round limit;
  merged PR moves the task to done; restart requeues a dead review.
- `internal/api`: new endpoints, origin guard on POSTs.
- `make sandbox-test`: a sandboxed reviewer calls `submit_review` through the in-container bridge.
- `make e2e`: existing runs plus `TestGitHubEndToEnd` (sandbox mode, haiku, real GitHub): a task
  becomes a PR on the playground; the internal reviewer posts a review; feedback reaches the
  original worker, whose new commit updates the same PR; nothing is merged.

## Steps (each one or more commits with `make check` green)

1. Branch `phase-5/github-review`; this plan.
2. ADRs 0020 (GitHub integration), 0021 (pluggable review, reviewer role), 0022 (review feedback
   and follow-ups).
3. `internal/secret` (moved token reader); migration 0005, queries, generated code, test.
4. `internal/github` and `internal/git` push.
5. `internal/review` with both implementations and the contract test.
6. `internal/mcp`: `submit_review`, reviewer tool set.
7. Engine: start options, reviewer role, relaunch.
8. Engine: PR submission and gates; review requests; feedback, follow-ups, polling, restart.
9. `cmd/agent-office`, API and UI.
10. e2e; docs (architecture, threat model, plan, `.env.example`, `PROGRESS.md`), phase summary.

## Open questions (answered with the recommended option)

1. **Where does the GitHub token come from?** A user-only file, same rules as the OAuth token
   (ADR 0015), path configurable by env; not a `GITHUB_TOKEN` variable, which leaks into child
   processes and shell history. (Ozan already created the file.)
2. **Is GitHub on by default?** No: `--github` turns it on, since it pushes to a remote.
3. **When does a PR open?** Automatically on a worker's `report_done` with commits; by hand for
   anything else in review. Not for a lead's goal: its sub-tasks each have a PR.
4. **Who reviews by default?** The internal reviewer: it works on a private repository with no
   extra setup. CodeRabbit is one flag away once its app is installed.
5. **What may the internal reviewer do on GitHub?** Nothing itself: it calls `submit_review`, and
   the daemon posts a `COMMENT` review. Never `APPROVE` (GitHub refuses it on the token owner's own
   PR anyway, and approvals could satisfy branch protection).
6. **How does feedback reach "the original worker"?** By relaunching that agent in its workspace
   on its branch with a new session (not a new agent, not `--resume`): the PR updates in place,
   attribution stays with the agent, and no undocumented session-resume behaviour is relied on.
7. **Does feedback start sessions on its own?** Yes, for the configured reviewer's actionable
   feedback, up to 2 rounds per PR; then a human decides. Human GitHub reviews never start one on
   their own.
8. **What does a merge do?** A PR merged on GitHub by a human moves its task to `done`; that is
   the human's decision, recorded by the daemon.

## Changes during the phase

- **ADRs as planned:** 0020 (GitHub integration), 0021 (pluggable review), 0022 (follow-ups,
  amends ADR 0017's "follow-up work is a new sub-task").
- **`internal/secret`** holds the token-file reader and `Secret` (moved from `internal/sandbox`,
  which keeps thin wrappers), so both tokens get the same checks.
- **`githubtest`**, an in-memory fake of the GitHub endpoints used, fails any test that sends an
  unknown request (a merge included) or a review event other than `COMMENT`.
- **Migration 0005** replaces `agents.role` by add/copy/drop/rename instead of a table rebuild:
  dropping `agents` would cascade-delete every row that references it. It gained
  `pull_requests.pending_flagged` during the phase (not applied anywhere outside tests yet).
- **People's GitHub reviews** are recorded (reviewer `human`) and offered with "Send to worker";
  they never start anything on their own.
- **A running worker is ended (once idle, the user not typing) and relaunched** with the review.
  The first version sent it the review as a team message; in the real e2e run haiku read that as
  "information from other agents" (ADR 0018's framing), ended its turn and changed nothing. The
  follow-up instruction now always comes from agent-office in a prompt (ADR 0022 updated).
- **Messages are delivered only to the session they were queued for**, found while testing the
  relaunch: a relaunched agent keeps its id and would otherwise get its previous session's
  messages (ADR 0018 says it must not).
- **`Shutdown` refuses new starts and waits for starts in progress**, so a follow-up cannot start
  an agent while the daemon stops (found as a test hang).
- **The exit import runs before `afterExit` closes**, and a relaunch waits for it.
- **End-of-phase review fixes** (an independent reviewer read the diff): `--github` refuses
  host-mode agents; changes under `.github/` block the push until accepted; at most 10 automatic
  reviews per PR; reviews of older heads are superseded; an internal reviewer that never submits
  is ended after 30 minutes; titles and Markdown images in agent text are neutralized; early
  refusals are recorded on the PR row; follow-ups left by a restart are offered again; a failed
  relaunch puts the task back; a recording error after posting never allows a second post. (The
  review's message-race findings went away with the message path.)
- **Not done live:** CodeRabbit. It is covered by the shared contract test and a filter test
  against the fake; a live run needs the CodeRabbit app installed on the private playground
  (possibly a paid plan), which is Ozan's call.
- **Tests added:** `TestSandboxedReviewLoop` (`make sandbox-test`, real Podman, no tokens) and
  `TestGitHubEndToEnd` (`make e2e` with `AO_E2E_GITHUB_REPO`, real Claude Code and GitHub).
