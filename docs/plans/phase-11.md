# Phase 11 plan: Portfolio polish

- **Status:** Done on `phase-11/portfolio-polish`, waiting for Ozan's review (not merged). Written 2026-11-08 with the recommended
  option for every open question and executed straight away, as Ozan asked ("Write the Phase 11
  plan to docs/plans/phase-11.md using your recommended options, then execute it immediately
  without asking me anything"). Every decision is listed in the end-of-phase summary; nothing is
  merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 11. Progress: [PROGRESS.md](../../PROGRESS.md).
  Phase order (Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13.
- **Prerequisites:** none from Ozan for building, scanning and recording. Publishing is his: pushing
  `main` and the tag, and creating the GitHub release on the website with the prepared notes and
  files (he does not use `gh`, and the project never does).

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A recruiter or engineer who opens the repository understands within 30 seconds what agent-office
is, sees it working, and knows where the interesting decisions are; an engineer who wants to run it
can, from the README alone, on a fresh Linux machine. The public history holds no secrets or
personal data beyond what Ozan chooses to publish, and there is a tagged first release with a
binary. The three Phase 11 backlog items are closed.

## Definition of Done (from docs/plan.md, refined)

- [x] **README rewrite.** In this order: a one-paragraph pitch; a demo GIF of a real team run in the
      Office tab; what it does in a few bullets with screenshots from `docs/screenshots/`; an
      architecture diagram in Mermaid (GitHub renders it); the key design decisions with links to
      their ADRs (at least: sandbox model, token injection, bundle hand-off, roles as data); a
      quickstart; "How this was built"; the rest (docs map, stack, inspiration, license). Plus an
      ADR index (`docs/adr/README.md`) so the 40+ ADRs can be browsed by topic.
- [x] **Demo GIF from a real run.** Recorded by a checked-in Playwright script (`make demo`) that
      starts a real daemon (scratch data dir, sandboxed haiku team, throwaway clone of the
      playground), pairs the browser through `agent-office pair`, gives the Tech Lead a goal and
      records the Office tab until the team has reported; ffmpeg turns the video into a GIF
      (`docs/screenshots/team-run.gif`, under 10 MB). No captured agent data other than what the
      GIF itself shows (fictional playground work, no local paths).
- [x] **A fresh Linux machine can install and run it by following the README:** the documented
      prerequisites alone build it in a clean Debian container (`make build`, `agent-office
      version`); on this machine, a fresh `git clone` follows the quickstart literally (build,
      worker image, serve with a scratch data dir, pair, start a sandboxed agent on a task, see it
      finish), and the README is corrected wherever reality differed.
- [x] **Secret and personal-data scan of the whole git history:** gitleaks (pinned, `make
      secret-scan`) over every commit of every ref, plus a targeted scan for personal data (author
      identities, e-mail addresses, home paths, IPs, session ids, captured payloads, files that
      should have been ignored). Findings are reported to Ozan **before** anything changes history;
      this phase does not rewrite history.
- [x] **Tagged v0.1.0 release with a binary:** an annotated tag `v0.1.0`; `make release` builds a
      static linux/amd64 binary from the tagged, clean tree into a tarball with `SHA256SUMS`;
      release notes in `docs/releases/v0.1.0.md`; step-by-step instructions for Ozan to create the
      release on the GitHub website.
- [x] **Architecture write-up linking the ADRs:** the README's design-decisions section and the ADR
      index; `docs/architecture.md` stays the long form.
- [x] **Backlog: reproducible worker images.** Debian packages in the base image and the built-in
      pack images come from a fixed `snapshot.debian.org` timestamp with exact versions, so the same
      Containerfile builds the same packages (tested: `make worker-image`, `make sandbox-test`).
- [x] **Backlog: `python3` in the worker image** (pinned like the rest), so agents that reach for it
      can use it.
- [x] **Backlog: closing keywords in agents' commit messages** (`fixes #12`) are flagged at push
      time like the existing identity and `.github/` checks: the push waits until the user accepts
      those commits (tested), and the worker prompt asks agents not to write them.

## What exists and what it means

- The README is accurate but reads like a changelog: its status paragraph lists every phase, its
  "Planned features" are mostly built, and there is no pitch, no diagram, no decision map and no
  moving picture. `docs/architecture.md` (50 KB) has a Mermaid component diagram and the threat
  model; it is the long form, not the 30-second view.
- `docs/screenshots/` has five PNGs: four from the mocked daemon of the UI tests (Phase 7, Phase 8)
  and one live team run; none is animated.
- The Playwright setup (`web/playwright.config.ts`, `make ui-test`) runs against a mocked daemon.
  Chromium and Playwright's ffmpeg are installed; system ffmpeg is installed.
- Pairing a browser: `agent-office pair --data-dir <dir>` prints a `#pair=<code>` link; `pair
  --token <name>` prints a bearer token for scripts (ADR 0039). Both work against a scratch daemon.
- `make build` stamps `git describe --tags --always --dirty` into `main.version`; there are no tags
  yet, so binaries say `c792229`-style versions. `/dist/` and `/bin/` are ignored.
- No secret scanner is installed. gitleaks v8.30.1 (2026-02-21) is a Go module, so it can run pinned
  through `go run` like sqlc, without a new dependency in `go.mod`.
- The worker image pins the Debian base image by digest and the Go download by SHA-256, but `apt-get
  install` takes whatever the mirror has that day. The base image itself records its snapshot
  (`http://snapshot.debian.org/archive/debian/20261104T212339Z`).
- The push path (`internal/engine/github.go`) already blocks a push on flagged items (identity
  check, `.github/` changes) until the user accepts exactly those commits; the UI shows them. PR
  text is neutralized (`github.Neutralize`), commit messages are not (ADR 0020, residual).

## Out of scope

- Rewriting git history (only after Ozan has seen the scan findings, and only if he asks).
- Pushing, the tag push, creating the GitHub release, uploading files: Ozan's, on the website.
- Binaries for other platforms or architectures than linux/amd64 (Linux-first; arm64 would be an
  untested build). Container images on a registry, installers, Homebrew/AUR packages.
- CI (GitHub Actions). The project's checks run locally with `make`; a workflow would also be a
  change under `.github/`, which agents' pushes are gated on. Recorded as a follow-up.
- Office v2 (Phase 13) and the Phase 10 and 12 backlog items that were not assigned to Phase 11.

## Approach

1. **Secret and personal-data scan (first, report only).**
   - `make secret-scan` runs `go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --log-opts=--all
     --redact` over the whole history and the working tree (`gitleaks dir`), with a repository
     config `.gitleaks.toml` that extends the default rules and allowlists only findings verified as
     false positives (each with a comment saying why). Output is redacted; nothing found is written
     into the repo.
   - Personal data: unique author/committer identities; every e-mail address, `/home/<user>` path,
     IP address, UUID-shaped session id and phone-number-shaped string in any blob of any commit;
     every path ever added (captured payloads, `.env`, databases, transcripts, tokens); the
     playground and Tailscale names. Each class is reported with counts and redacted examples.
   - The report goes into the end-of-phase summary (and, redacted, into this plan's changes).
2. **Backlog: closing keywords** (`internal/github`, `internal/git`, `internal/engine`).
   - `github.ClosingReferences(msg)` finds GitHub's closing syntax: one of close/closes/closed,
     fix/fixes/fixed, resolve/resolves/resolved (any case), an optional colon, then `#N`,
     `owner/repo#N` or an issue URL.
   - `git.CommitMessages(base, branch)` reads full messages (`git log -z`), parsed strictly; a
     message that cannot be parsed counts as flagged (fail closed).
   - In the push path each commit with a closing reference becomes a `FlaggedCommit` ("commit message
     would close #12 when the pull request is merged"); the existing accept flow applies. The task
     prompt asks agents not to write closing keywords. ADR 0020 amended (the residual is closed).
3. **Backlog: reproducible worker images and `python3`** (`internal/sandbox/Containerfile`, the
   built-in packs). Point apt at `snapshot.debian.org` at the base image's own timestamp
   (`debian` and `debian-security`), install exact versions (`pkg=version`), add `python3`. The pack
   images use the same snapshot. Bumping is one timestamp plus the versions, documented in the
   Containerfile and `docs/packs.md`. New ADR.
4. **Demo recorder** (`web/demo/`, `make demo`). A Playwright script, not a test of `make ui-test`:
   - clones the playground into a scratch dir named `linkshelf`, starts `bin/agent-office serve`
     with a scratch `--data-dir`, `--sandbox podman --pack go --model haiku --qa=false
     --max-agents 3`, waits for health;
   - opens Chromium with video recording at 1280×800, pairs it through the `#pair=` link, creates a
     goal with two small, independent playground backlog items through the API (bearer token from
     `pair --token`), starts a Tech Lead on it, shows the board briefly, then the Office tab until
     the goal is reported, then the board again;
   - writes the raw video and a few PNG stills to a scratch output dir; a shell step trims, speeds up
     and converts it to a GIF with a generated palette. Only the GIF and chosen stills are
     committed, after looking at every frame for local paths or anything personal.
   - `docs/demo.md`: how to record it again, and a short script for showing the project live.
5. **Release** (`make release`, ADR). Requires a clean tree at an exact tag; builds the UI and a
   static `CGO_ENABLED=0 -trimpath` binary stamped with the tag into
   `dist/agent-office_<tag>_linux_amd64.tar.gz` (binary, LICENSE, README, release notes; tar with a
   fixed mtime, owner and order) and `dist/SHA256SUMS`. `docs/releases/v0.1.0.md` holds the notes.
   The tag is annotated and created locally on the phase branch's last commit; the merge keeps it in
   `main`'s history.
6. **README and docs.** Rewrite as in the DoD; ADR index; `docs/demo.md`; architecture status line;
   plan.md and PROGRESS.md. The quickstart is written first, then executed from a fresh clone and
   corrected.
7. **Fresh-clone verification.** A clean `debian:trixie-slim` container with only the documented
   prerequisites (git, make, Go, Node from their official tarballs, checked by SHA-256) runs `make
   build` and `bin/agent-office version`. On this machine a `git clone` of the branch into a scratch
   dir follows the quickstart verbatim with a scratch data dir and a scratch copy of the playground.

## Risks

- **A real secret in history.** The repo is public, so rotation matters more than rewriting: the
  report says which credential it is and that rotating it is the fix; a history rewrite is Ozan's
  call afterwards.
- **The demo run fails or looks dull** (haiku stalls, a run takes 10 minutes). Mitigation: a goal
  known to work in `TestTeamEndToEnd`'s style (two small, independent sub-tasks), `max-agents 3`,
  a timeout, and a speed-up computed from the run's length so the GIF stays about 30-45 seconds.
- **GIF size.** A 1280-wide, many-frame GIF can exceed 20 MB; scale to 960 wide, 8-10 fps, a
  palette per clip, and cut idle stretches.
- **snapshot.debian.org is slow or rate-limited.** Builds take longer; images are built rarely and
  cached by tag. If it is down, a build fails loudly rather than silently installing other versions.
- **The tag moves.** If Ozan asks for changes after reading, the unpushed tag is recreated on the
  new last commit and the release rebuilt; the summary says so.
- **A recruiter's first impression depends on GitHub's rendering** (Mermaid, GIF autoplay, relative
  links): every link is checked against the tree, and the Mermaid source is kept to syntax GitHub
  supports (flowchart, no HTML beyond `<br/>`).

## Steps (each one or more commits with `make check` green)

1. `docs:` this plan.
2. `chore:` `make secret-scan`, `.gitleaks.toml`; run the scans, report.
3. `fix(github):` flag closing keywords in agents' commit messages at push time; prompt line; tests.
4. `build(sandbox):` pin Debian packages to a snapshot, add `python3`; rebuild images; `make
   sandbox-test`.
5. `feat(demo):` the recorder (`web/demo/`, `make demo`) and the GIF conversion.
6. `docs:` the demo GIF and stills from a real run.
7. `docs:` README rewrite, ADR index, `docs/demo.md`, new ADRs, architecture, plan.md.
8. `build:` `make release`, release notes.
9. Fresh-clone verification; README fixes.
10. `make check`, `make ui-test`, `make sandbox-test`, `make e2e` (host, sandbox, team, workflow);
    PROGRESS.md; tag `v0.1.0`; `make release`.

## Open questions (answered with the recommended option)

1. **gitleaks or trufflehog?** gitleaks: a Go module that runs pinned through `go run` (like sqlc),
   so nothing is downloaded outside Go's checksum database and nothing joins `go.mod`; it scans
   every commit of every ref with redacted output. trufflehog verifies live credentials against
   their services, which would send found secrets to third parties.
2. **Rewrite history if something is found?** Not in this phase: Ozan asked for a report first. A
   leaked credential is rotated regardless; a rewrite of a public repository does not unpublish it.
3. **Where does the tag go?** On the phase branch's last commit, created locally. After the
   `--no-ff` merge it is in `main`'s history with the same tree; the binary is built from the tag,
   so it is exactly what the tag holds. Ozan pushes `main` and the tag.
4. **Which platforms?** linux/amd64 only, the tested one.
5. **Demo: architect and QA too, or the Tech Lead and two workers?** Tech Lead and two workers in
   sandbox mode, without QA: the shortest run that shows the whiteboard, typing, messages and
   reports. The architect needs a plan approval (a click), which the recorder could do, but it
   doubles the run time and the GIF length; it is mentioned in the README instead.
6. **`python3` in the image, or tell agents it is missing?** In the base image: agents use it for
   file edits and quick scripts, the Debian package is about 25 MB, and every pack inherits it.
7. **Closing keywords: flag at push time or rewrite the merge message?** Flag at push time. GitHub
   closes issues from commit messages that reach the default branch, whatever the merge message
   says (merge commits and rebase merges keep the agent's commits), so only the push gate covers
   every merge method. It reuses the existing accept flow.
8. **CI?** Not in this phase (see Out of scope).

## Changes during the phase

- **Scan results (reported, history not rewritten).** gitleaks v8.30.1 found no secrets in the 231
  non-merge commits of every ref (243 with merges) or in the working tree. The personal-data scan of
  every blob ever committed and every commit message found:
  - every commit's author and committer is Ozan's personal Gmail address (486 identity fields). It is
    in no file, only in commit metadata. This is the one real finding; rewriting it would change
    every hash of a published history, so it is Ozan's call (a `.mailmap` does not change what
    GitHub shows for existing commits);
  - his GitHub handle and name (LICENSE, module path, playground links), and the Unix user name in
    one spike (`spikes/sandbox-claude/main.go` probes that `/home/<user>` is not visible in the
    sandbox): public anyway;
  - one example Claude Code session id in `docs/usage.md` (a random local identifier, not a
    credential), fixture UUIDs, documentation IPs and canary tokens (`sk-ant-oat01-aoCanary`) in
    tests;
  - no captured payloads, transcripts, `.env` files, databases or logs; fixtures are redacted shapes
    (`SENSITIVE`, `/redacted/...`); the committed screenshots show only fictional or playground work.
- **Closing keywords and NUL bytes.** git prints a commit message only up to a NUL byte, so a keyword
  after a NUL would be invisible to the gate. The bundle import's existing `transfer.fsckObjects`
  already refuses such commits (`nulInCommit`); a test pins that, and the message reader fails
  closed on any record it cannot parse. Problems of one commit are now merged into one flagged entry
  (`addFlag`), so the `.github/` check and the new check do not list the same commit twice.
- **The demo run was faster than planned:** the haiku team finished in 55-61 seconds in both
  recordings, so the GIF is about 35 seconds at about 2× instead of a heavy speed-up. The first
  recording showed a `-dirty` version in the header; the committed GIF is from the second recording,
  made from a clean commit. GIF settings were tuned for size: 800 px, 6 fps, 64 colours, 6.2 MB
  (the first try at 960 px, 10 fps, 128 colours was 20 MB).
- **What the demo shows by itself:** each sandboxed agent's card shows "Network blocked": Claude Code
  tries `downloads.claude.ai` and `github.com` at start-up, and the egress allowlist refuses them.
  Both sub-tasks changed overlapping lines, so the board shows the ADR 0032 conflict between the
  two branches. Both are real behaviour and are explained in `docs/demo.md` rather than hidden.
- **Fresh-clone findings:** Node's official tarball needs `libatomic1`, which a minimal Debian lacks
  (noted in the README). A worker the user starts on a task stays open after `report_done` (only
  lead-managed workers exit, `team.go`); the README's quick start said otherwise and was corrected.
  An unverified README sentence (the repository needs a commit) was removed.
- **`make release` and removals.** A first version of the recipe used variable paths in `rm -rf`,
  which Claude Code's safety check refused to write through the shell; the recipe now removes only
  the fixed `dist/src` and `dist/stage`.
- **GitHub e2e not run:** the push gate is covered by the engine tests against the GitHub fake;
  `TestGitHubEndToEnd` would open a pull request on the playground repository, which was not run in
  Phases 9 and 10 either.
- **Checks at the end:** `make check`, `make ui-test` (41 passed), `make sandbox-test` (and
  `TestWorkerEgress` with `AO_SANDBOX_NET=1`), `make e2e` (TestEndToEnd, TestSandboxEndToEnd,
  TestTeamEndToEnd, TestTeamWorkflowEndToEnd), `make secret-scan`, two `make release` dry runs in a
  scratch clone with the same checksum.
