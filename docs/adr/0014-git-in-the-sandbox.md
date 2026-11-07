# 0014. Git in the sandbox: a shared clone per agent, work handed back as a bundle

- **Status:** Accepted
- **Date:** 2026-11-07
- **Amends:** ADR 0010 (worktree layout) for sandboxed agents; host mode keeps ADR 0010 as is

## Context

Sandboxed workers (ADR 0007) need a working git repository: agents read history, diff and commit.
ADR 0010 gives each agent a linked worktree. A worktree's `.git` is a file pointing at
`<repo>/.git/worktrees/<id>`. Its refs, objects and config live in the main repository's `.git`.
Inside a container, git only works if that directory is mounted **writable**.

Spike F (`spikes/sandbox-claude`) showed why that is not acceptable. Git config the agent can write
(`core.fsmonitor`, `core.hooksPath`, `diff.external`, filters, ...) runs commands for whoever runs
git in that repository. When the host ran `git status` in a repository whose config the agent had
changed, the agent's command **executed on the host**. With the main `.git` mounted writable, the
agent could also move any branch, delete tags and rewrite the user's own config and hooks.

Requirements:
- Agents must not be able to push (no credentials, no network to a git remote; ADR 0016). Pushing
  stays with the daemon (Phase 5).
- Agents must not touch other branches or the user's repository state.
- Commits carry Ozan's identity as author plus a `Co-Authored-By` trailer naming the agent
  (decided by Ozan, 2026-11-07).
- Agent work is never lost by accident (ADR 0010's removal rules still apply).

## Options considered

1. **Linked worktree, main `.git` mounted writable.** Cons: host code execution via git config;
   full write access to every ref. Rejected.
2. **Linked worktree, main `.git` read-only or as an overlay (`:O`).** Cons: read-only makes
   commits impossible. With an overlay, commits are thrown away when the container ends.
3. **A full, independent copy per agent** (`clone --no-local`). Pros: nothing shared. Cons:
   duplicates the object store per agent (disk, time). A *hardlinked* local clone is worse: the
   agent could modify a hardlinked object file and corrupt the main repository.
4. **A shared clone per agent, work handed back as a bundle.** The agent's repository borrows the
   main repository's objects through `objects/info/alternates`, mounted **read-only**. New commits
   go into the agent's own object store. The daemon brings them back by having git inside the
   sandbox write a bundle, which the host verifies and fetches. Pros: cheap; the agent owns only its
   own repository; a bundle is inert data. Spike F showed that `git bundle verify` and `git fetch`
   from a bundle ran nothing, even with a malicious config in the source repository. Cons: one more
   moving part; the main repository must keep the base commit reachable.
5. **No git inside the container; the daemon commits on the agent's behalf.** Cons: agents rely on
   `git diff`, `log` and `commit`; without them they work worse and fight the environment.

## Decision

Option 4 for sandboxed agents.

**Creating the agent repository** (on the host, before the agent starts; the only time the host
runs git in it):
- `git init --template= <path>`: the empty template, so the user's hook templates are not copied in.
- `objects/info/alternates` points at the main repository's object directory (`git rev-parse
  --git-common-dir`/`objects`, absolute path).
- `refs/heads/<branch>` is set to the base commit and `HEAD` points at it. The working tree is
  checked out. There is **no remote and no other ref**: the agent sees one branch.
- In the main repository the daemon creates `refs/agent-office/base/<agent-id>` at the base commit,
  so a `git gc` there never prunes objects the agent repository borrows. It is deleted when the
  agent is removed.
- Location: `$XDG_DATA_HOME/agent-office/workspaces/<project>/<agent-id>` (ADR 0010's layout with
  `workspaces` instead of `worktrees`), mounted at `/workspace`. The main object directory is
  mounted read-only **at the same absolute path**, because alternates are absolute. That reveals one
  host path name to the agent, which is accepted.

**The host never runs git inside an agent repository once the agent has started.** `internal/git`
enforces this: paths under the workspaces root are untrusted, and every command whose working
directory or arguments point there is refused, except the creation step above.

**Everything else runs in the sandbox**, in a short-lived helper container with the same mounts,
image and restrictions as the agent (`--network none`, read-only rootfs, no capabilities):
- **Status** (dirty or clean, for ADR 0010's removal rules): `git status --porcelain=v1 -z`. The
  output is untrusted data: size-capped and parsed strictly.
- **Hand-off:** `git bundle create - <base>..<branch>`, streamed to a daemon-owned file with a size
  cap.

**Importing** (on the host, in the main repository, which is trusted):
- `git bundle verify`, then check that the bundle contains exactly `refs/heads/<branch>`.
- `git -c transfer.fsckObjects=true fetch --no-tags <bundle> +refs/heads/<branch>:refs/heads/<branch>`.
  Only that agent's branch is ever updated. A forced update is allowed because the agent may amend
  or rebase its own branch.
- Import happens when the agent exits and on request from the UI. Phase 5 pushes from the main
  repository, never from the agent repository.

**Ownership: agent repositories belong to a subordinate uid, not to the user** (revised
2026-11-07, Spike F addendum `spikes/sandbox-claude/subuid.sh`):

- The daemon is not the only program that runs git on the host. The shell prompt, editors (VS
  Code refreshes repositories in the background), file managers and IDE plugins run git in any
  folder they open. The rule "the host never runs git in agent repositories" cannot bind them.
- Git refuses to work in a repository owned by another user ("detected dubious ownership",
  `safe.directory`), and it does so **before reading the repository's config**. Agent workers
  therefore run **without `--userns=keep-id`**, as container uid/gid `1000:1000`. On the host, that
  is a subordinate id (`100999` with this machine's `/etc/subuid` range `100000:65536`). After
  `CreateSharedClone`, the daemon hands the repository to that id with
  `podman unshare chown -R 1000:1000`.
- Measured: with the subordinate owner, host `git status`, `git log` and git inside `.git` were all
  refused, and the planted `core.fsmonitor` never ran. The **control**, the same repository owned
  by the user, ran the trap on a plain `git status`. Starship 1.27 did not run it even in the
  user-owned control, so it was not a vector in this setup. VS Code's git integration and
  Dolphin's git plugin use the git CLI as far as known; not tested here, and Dolphin's plugins
  are not installed on this machine.
- Options considered: keep `keep-id` and rely on the daemon guard plus warnings (protects nothing
  outside the daemon); a deny-list in the user's global git config (git has none, and repository
  config overrides global values); `--userns=auto`, with a separate id range per container
  (stronger isolation between agents too, but needs extra subordinate-id setup; possible later).
- Costs, all measured in the addendum:
  - The host user cannot delete or modify agent files. Removal and "discard" use
    `podman unshare rm -rf`, only on validated paths under the workspaces root.
  - Files the agent creates are readable by the host user (container umask 022). Claude Code's
    own 0600 files (e.g. `.claude.json`) are not. Token usage comes from the API socket
    (ADR 0016), so the daemon does not need them.
  - Uncommitted work in a kept repository can no longer be committed by hand there. The UI offers
    **"Save uncommitted work"**: a helper container commits everything as a WIP commit (with the
    trailer), and the daemon imports the branch.
  - Mounted sockets must be usable by the subordinate id. Each socket stays mode 0600 and is
    chowned to it with `podman unshare chown`. A socket owned by the user was not writable
    for the agent. Its directory stays 0700 and owned by the user, so other host users still
    cannot reach it. The daemon-generated settings file is mode 0644 in a 0700 directory, and
    the per-agent config dir is chowned like the repository.
  - The main repository's objects must be readable by "others" (the agent's id is not the
    user). This holds with the usual umask 022 (`-r--r--r--` objects, `drwxr-xr-x` dirs). The
    first helper call (status) doubles as a readiness check and fails with a clear message
    otherwise.
  - All agents share the same subordinate id. That is no regression (with `keep-id` they shared
    the user's id), and each container only mounts its own paths.
- Spike E's note that `keep-id` keeps worktree files owned by the user, so that host git keeps
  working, no longer applies: host git in agent repositories is exactly what must not work.
- **This does not make agent repositories safe to open.** Git refuses, but VS Code offers to mark
  such a repository as safe (its "unsafe repository" handling; not tested here), which adds a `safe.directory` entry and switches the protection off
  for that path. Other tools may read files without git (language servers, `.envrc`, project
  scripts). The rule for the user: **do not `cd` into or open agent repositories. Use the branch
  in the main repository.** The daemon puts a `README.txt` saying so in the workspaces root, the UI
  shows it next to every workspace path, and the architecture document states it.

**Commit identity:**
- Author and committer come from the user's `user.name` / `user.email`, read by the daemon in the
  main repository and passed as `GIT_AUTHOR_*` / `GIT_COMMITTER_*` env vars.
- A `commit-msg` hook, mounted read-only and enabled through command-scope config
  (`GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_n` / `GIT_CONFIG_VALUE_n`), appends
  `Co-Authored-By: <agent-name> (agent-office) <noreply@agent-office.invalid>`. `.invalid` is a
  reserved TLD (RFC 2606), so the address can never belong to anyone.
- The agent can bypass both (`--no-verify`, `-c`), so the daemon **checks every imported commit**
  for the expected author and trailer and flags mismatches in the UI. Phase 5 refuses to push a
  branch with flagged commits until the user accepts it.

## Consequences

- Removal follows ADR 0010:
  - a clean repository without commits is deleted;
  - one with commits is deleted after a successful import (the branch is kept in the main repo);
  - a dirty one is kept until the user discards it.
- The daemon state (DB) records which kind of workspace an agent has (`worktree` in host mode,
  `clone` in sandbox mode). Migration 0002 adds this.
- **Do not `cd` into or open agent repositories on the host.** Subordinate-uid ownership stops git
  itself, but not tools that read files directly or that offer to mark the repository as safe. The UI
  points to the branch in the main repository instead.
- Submodules and Git LFS are not supported in sandboxed workspaces yet: LFS needs network access,
  and submodules need their own object stores.
- If the main repository itself uses alternates, the chain must be mounted too. This is not
  handled yet; the daemon refuses such repositories with a clear error.
