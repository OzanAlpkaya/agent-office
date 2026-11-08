# 0020. GitHub integration: a token file, daemon pushes over SSH, polled pull requests

- **Status:** Accepted; the commit-message residual closed in Phase 11 (closing keywords flagged at push time)
- **Date:** 2026-11-07

## Context

Phase 5 turns a worker's finished task into a pull request. Constraints from earlier decisions:

- Agents are untrusted (threat model). They have no git credentials and no route to a git remote
  (ADR 0014, 0016); pushing is the daemon's job, from the main repository.
- GitHub API access goes through go-github, never the `gh` CLI (CLAUDE.md). Git operations use
  SSH (host alias `github-personal`).
- Merging stays a human action (roadmap DoD).
- The daemon listens on loopback only (ADR 0002); remote access is Phase 10.

Ozan created a fine-grained token limited to the playground repository and stored it in
`~/.config/agent-office/github-token` (mode 0600).

## Options considered

**Where the token comes from**

1. **`GITHUB_TOKEN` in the daemon's environment.** Cons: ends up in shell profiles and history,
   inherited by every child unless scrubbed, readable in `/proc/<pid>/environ` (the reasons ADR
   0015 rejected this for the OAuth token).
2. **A user-only token file, read for each GitHub operation** (ADR 0015's rules). Pros: one known
   place, rotation by replacing the file, the same checks as the OAuth token.

**How the daemon learns about reviews and merges**

1. **Webhooks.** Cons: need an address GitHub can reach; the daemon is loopback-only.
2. **Polling the REST API.** Pros: works behind NAT, no public endpoint, no webhook secret. Cons:
   latency (seconds to a minute) and API calls; far below the 5 000 requests per hour limit for a
   handful of pull requests.

**Which repository and base**

1. **Configured `owner/name` and base branch.** Cons: one more setting to keep in sync.
2. **Parsed from the main repository's push remote; base = the GitHub repository's default
   branch.** Pros: nothing to configure for the usual setup; the remote host may be an SSH alias.

## Decision

Option 2 in each group.

- **Opt-in:** `--github` (`AGENT_OFFICE_GITHUB=true`). The daemon pushes to a remote, so it never
  does that unless asked. **Sandboxed agents only:** `--github` with `--sandbox host` is refused,
  because host-mode agents run as the user and could read the token file or change the `.git/hooks`
  that the daemon's push runs (found in the end-of-phase review).
- **Token:** `AGENT_OFFICE_GITHUB_TOKEN_FILE` (a path, never the token; default
  `$XDG_CONFIG_HOME/agent-office/github-token`). The reader is the one from ADR 0015, moved to
  `internal/secret`: regular file opened with `O_NOFOLLOW`, owned by the user, mode 0600/0400,
  directory not writable by group or others, at most 4 KiB, one printable-ASCII token. It is read
  **for every GitHub operation** and held as a redacting `secret.Secret` only for that operation.
  go-github's `WithAuthToken` sends it only to the configured API origin.
- **Token permissions** (fine-grained, that repository only): Metadata read, Pull requests
  read/write, Commit statuses read, Checks read where offered. **No Contents write**: pushing goes
  over SSH, and without it the token cannot merge even by mistake. The daemon cannot see a
  token's permissions; a 403 on checks shows "checks unavailable" and nothing fails. Ozan narrowed
  the playground token to Contents read-only after the phase (2026-11-07).
- **Repository:** from `git remote get-url --push <remote>` (`--github-remote`, default `origin`),
  accepted forms `git@<host>:owner/name(.git)`, `ssh://git@<host>/owner/name(.git)` and
  `https://github.com/owner/name(.git)`; owner and name must match GitHub's character rules. At
  startup the daemon reads the repository once (`GET /repos/{owner}/{repo}`) to check the token
  and to learn the default branch, which is every PR's base.
- **Push:** the daemon runs `git push --no-follow-tags <remote> refs/heads/<b>:refs/heads/<b>` in
  the **main repository** (trusted; never in an agent repository), never `--force`. Imports never
  move a branch backwards (ADR 0014), so a follow-up push is always a fast-forward; anything else
  fails and is shown. The push runs in a new session without a controlling terminal (`setsid`)
  and with a 2-minute timeout, so an SSH passphrase prompt fails instead of hanging the daemon.
  The user's own SSH setup (agent, config, alias) is used as is.
- **Pull requests:** created idempotently: first look for an open PR with head `owner:<branch>`;
  only if there is none, `POST /pulls`; a 422 "already exists" leads to the lookup again.
  `POST`s are never retried blindly; `GET`s are retried twice with backoff on network errors and
  5xx. Every call has a 30-second timeout. Logs carry owner/name, PR number, branch, status and
  duration, never the token, headers or bodies.
- **Text sent to GitHub:** agent-written text (titles, summaries, sub-task descriptions, review
  comments) has `@mentions`, issue-closing keywords (`fixes #12`) and Markdown images neutralized with
  a zero-width joiner, so an agent cannot notify people, close issues when a human merges, or make
  GitHub fetch a URL carrying data. Commit messages cannot be changed after the fact (imports never
  rewrite history), so a commit whose message carries a closing reference (`fixes #12`,
  `closes owner/repo#3`, an issue URL) is flagged at push time like the gates below (Phase 11,
  2026-11-08; `github.ClosingReferences`, `TestClosingKeywordsBlockPush`). GitHub closes those
  issues when the commit reaches the default branch by any merge method, so rewriting the merge
  message would not have been enough. The task prompt asks agents not to write them.
- **Push gates:** commits failing ADR 0014's identity check, and any change under `.github/`
  (push-triggered workflows run agent-written code with the repository's secrets), block the push
  until the user accepts exactly those commits (the head, for `.github/`) in the UI.
- **Polling:** one loop in the engine, every 30 seconds while a PR waits for a review or checks,
  every 2 minutes otherwise; on a rate-limit error it waits until the reset time. It reads the
  PR's state, its head's checks and the reviewer's feedback (ADR 0021).
- **Merging:** the `internal/github` client has no merge operation, and no agent holds the
  token. A PR **merged by a human** on GitHub moves its task to `done` (the merge is the human's
  decision; the daemon records it). A closed PR is recorded; nothing else happens.

## Consequences

- Rotating or revoking the token takes effect at the next operation; no restart.
- A PR's base is the repository's default branch even if the agent started from another local
  branch, and local commits not yet on the remote show up in the PR. Agents start from the main
  repository's `HEAD` as before; pushing that branch first is the user's job.
- Up to 30 seconds (2 minutes for idle PRs) pass before a review or merge is noticed.
- If SSH needs a passphrase and no agent holds the key, every push fails with git's error, shown
  in the UI. That is intended: the daemon never asks for credentials.
- A future GitHub App or webhook mode (Phase 10 has an authenticated, reachable daemon) can
  replace polling behind the same client.
