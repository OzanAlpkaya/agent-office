# Phase 9 plan: Memory & persistence

- **Status:** Done on `phase-9/memory-persistence`; merged into `main` (2026-11-08). Written 2026-11-08 with the recommended
  option for every open question and executed straight away, as Ozan asked ("write the Phase 9
  plan using your recommended options, then execute it immediately without asking me anything").
  Every decision is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 9. Progress: [PROGRESS.md](../../PROGRESS.md).
  Phase order (Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13.
- **Prerequisites:** none. A short spike against the real Claude Code 2.1.319 (haiku, a few cents)
  was run before writing this plan (below). `make e2e` at the end spends a few cents more.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

Agents and the team keep their context across sessions and daemon restarts. An agent that the
daemon stopped (a restart, a crash, a clean shutdown) comes back as the same agent, in the same
workspace and sandbox, **with its conversation**, and the user can resume any exited agent the
same way. What happened in earlier sessions stays visible: their terminal output and counters. And
project memory reaches each agent as the notes *relevant to it*, which every agent can also read on
demand, and which the user can export.

## Definition of Done (from docs/plan.md, refined)

- [x] A restarted daemon restores agents, tasks and history: agents interrupted by the restart are
      resumed with their conversation (same agent, workspace, branch and sandbox config), tasks are
      unchanged, and every session's terminal output and counters stay viewable after a restart.
- [x] Agents can read relevant project memory at task start: the memory section of an agent's first
      prompt puts entries scoped to its role or its task's files first and leaves out entries scoped
      to other roles or files; every agent except reviewers can read all accepted entries with
      `list_memory`.
- [x] Memory is inspectable and editable by the user (Phase 12 built the panel): scopes are shown and
      edited there, and the memory exports as Markdown.
- [x] Backlog: the "Resume this session" line is meaningful (Resume in agent-office); the Phase 8
      note is replaced. A sandboxed agent's settings label no longer flips after a restart.

## What exists and what it means

- **Restart today** (ADR 0011): every agent not `exited` is marked `daemon_restart`; its terminal is
  gone (`ErrNoTerminal`), its workspace and branch stay. A clean shutdown marks agents
  `daemon_shutdown`. Tasks persist. Sandboxed agents are imported after a restart (ADR 0014).
  `agents.state` is written on every state change, so after a crash the row still says what the
  agent was doing; after a clean shutdown that is overwritten with `exited`.
- **Relaunch** (ADR 0022, 0031, 0032): workers and architects are started again in their workspace
  with a **new, fresh** session and a prompt (review follow-ups, conflict rounds, architect
  escalation). ADR 0022: "Phase 9 can switch the relaunch to `--resume`."
- **Session files:** host agents write transcripts under the user's `~/.claude/projects/<cwd>/`;
  sandboxed agents under their own config dir `$DATA/agent-office/agents/<id>/config` (persistent,
  owned by the sandbox's subordinate uid, cwd `/workspace`), so both survive a daemon restart and a
  reboot.
- **Spike (2026-11-08, Claude Code 2.1.319, haiku, print and interactive mode):**
  - `claude --resume <old> --fork-session --session-id <new> [-- prompt]` continues the old
    conversation (the model recalled a codeword from the first session) under **our** new id: every
    hook carries `<new>`, `SessionStart` has `source: "fork"`, the transcript is `<new>.jsonl`, the
    old file is untouched.
  - `--session-id` together with `--resume` **without** `--fork-session` is refused ("--session-id can
    only be used with --continue or --resume if --fork-session is also specified").
  - An unknown id prints "No conversation found with session ID: …" and exits 1, with no
    `SessionStart` (only a `SessionEnd` with a random id, which the daemon rejects anyway).
  - Interactive: with a prompt, the prompt is submitted and answered; without one the TUI shows the
    conversation and waits (`SessionStart`, then nothing).
  - The forked transcript **copies the parent's assistant records** (same `message.id`), and its
    `cost-state.totalCostUSD` is **cumulative** (includes the parent's cost). Phase 8's host reader
    would count the parent's messages and cost twice.
- **Memory** (ADR 0034): accepted entries reach every agent's first prompt, newest first, within
  6 000 characters; no per-agent relevance; `list_memory` is the assistant's only.
- **Settings label** (Phase 8 backlog): `Start` stores `--inherit-user-settings` (default true) in
  `agents.inherit_user_settings` for every agent; the sandboxed launch shows `false` in memory. After
  a restart the row's `true` is shown, so the card flips to "user settings on". The setting never
  applied to sandboxed agents (ADR 0003).

## Out of scope

- Switching relaunches (review follow-ups, conflict rounds, architect escalation) to `--resume`:
  their prompts already carry what they need, and a fresh session avoids dragging a long, possibly
  compacted conversation (and its input tokens) into every round. Recorded in ADR 0035 as considered;
  easy to switch later.
- Resuming QA agents and reviewers: their runs and reviews end with them (ADR 0021, 0033), and the
  lead or the poll loop starts new ones.
- Keeping follow-up and conflict rounds across a restart (ADR 0022: "the round lives with the
  running agent"); such workers are not restored and "Send to worker" / "Resolve conflict" start a
  new round, as today.
- Memory proposals from agents other than the assistant; importing memory from a file.
- Full transcripts in the UI (Claude Code's own `/resume` view inside the resumed TUI shows them).

## Approach

1. **Resume (`--fork-session`).** `agent.LaunchSpec` and `agent.ContainerSpec` gain `ResumeFrom`
   (a session UUID): the command line becomes `--resume <old> --fork-session --session-id <new>`.
   `Engine.Resume(ctx, id, prompt)` reuses the relaunch path (ADR 0022) with the agent's latest
   session as `ResumeFrom`: same agent id, workspace, branch, base, config dir (sandbox) and role;
   a new `sessions` row with `resumed_from` (migration 0009); a new session budget (ADR 0027).
   - **Who:** an exited agent whose workspace exists, run in this daemon's sandbox mode, not being
     removed or relaunched; kinds worker, architect, lead and assistant (QA and reviewers are
     refused, see out of scope). Leads, architects and assistants stay one at a time, and a lead's or
     architect's goal must not be `done`. Within `--max-agents`.
   - **Task status** is left as it is: a resume continues a conversation; status changes come from
     the agent's reports and the user, as before.
   - **Messages:** a resumed session receives the messages queued for the conversation it continues
     (delivery's `since` is the resumed session's start, not the new one's), so reports and replies
     that arrived while the daemon was down are delivered. Without a prompt, idle delivery may start
     right after `SessionStart`.
   - **Memory** is not appended again: the conversation already has it.
   - **Failure:** a resumed CLI that exits before `SessionStart` gets exit reason `resume_failed`,
     shown in the UI ("Claude Code could not load the previous conversation"); its terminal shows
     Claude Code's message.
   - **Host usage** (ADR 0026): the resumed session's transcript reader skips message ids the agent's
     earlier sessions already recorded, and Claude Code's own cost for the session is its
     `cost-state` minus the CLI cost of the sessions it continues (never below zero). Sandboxed
     usage is metered per request and needs no change.
   - **API/UI:** `POST /api/agents/{id}/resume` (optional `prompt`), a **Resume** button on exited
     agents' cards and under their terminal, replacing the Phase 8 note; Claude Code's own
     "Resume this session with claude --resume …" line is explained as "use Resume here".
2. **Restore after a restart.** `agents.resume_state` (migration 0009) remembers what an agent was
   doing when the daemon stopped it: set by the clean shutdown from the live state, and by startup
   reconciliation from the row's last state (crash). At startup, with `--restore-agents` (default
   on, `AGENT_OFFICE_RESTORE_AGENTS`), after reconciliation, sandbox imports and GitHub restart
   handling, the agents with a `resume_state` are resumed in this order: architect, lead, assistant,
   workers by start time, within `--max-agents`.
   - Not restored (left exited with a reason in the restore notice): QA and reviewers; agents that
     were still `starting` (no conversation yet); workers whose follow-up or conflict round the
     restart ended; agents of the other sandbox mode; agents whose workspace is gone; agents beyond
     the slot limit (the user resumes them by hand).
   - An agent that was mid-turn (`working`, `waiting_permission`) is resumed with a short prompt from
     agent-office: the daemon restarted and cut its last turn off; nothing else changed; check where
     it was (git status, its task) and continue. An idle one is resumed without a prompt (no tokens
     spent until someone writes to it).
   - `resume_state` is cleared when the agent is restored, resumed, relaunched or removed, so only
     the latest stop counts; one inbox `info` notice from agent-office lists what was restored and
     what was not, and why.
3. **Session history.** Each session's terminal output is saved: the scrollback (256 KiB ring) is
   written to `$DATA/agent-office/terminals/<project>/<agent>/<session>.log` (0600, directory 0700,
   temp file and rename) at most every 5 s while output changes and once at exit, so a crash loses
   at most a few seconds. `terminal.Session` gains a cheap change counter for this.
   - `GET /api/agents/{id}/sessions`: the agent's sessions, newest first, with start, end,
     `resumed_from`, turns, tool calls and whether a saved terminal exists;
     `GET /api/agents/{id}/sessions/{sid}/terminal`: the saved bytes (`application/octet-stream`,
     size-capped). The UI's terminal view shows a session picker; an agent without a live terminal
     (an earlier daemon run) shows its last session's saved output read-only instead of "terminal
     not available".
   - Retention: removing an agent deletes its saved terminals; the daily retention loop (ADR 0028)
     deletes files older than `--event-retention-days`. Never logged; contents are what the user saw
     in the terminal (threat model row).
4. **Relevant memory.** `memory.roles` and `memory.paths` (migration 0010; JSON arrays, empty =
   everyone / every file): an entry scoped to roles applies to agents with one of those role names;
   one scoped to paths applies to agents whose task declares files that overlap them (the overlap
   rule of ADR 0032). The prompt section lists applicable entries, scoped ones first (they are
   specific to this agent), then unscoped ones, each group newest first, within the same 6 000
   characters; it says how many accepted entries were left out and that `list_memory` shows them.
   - `list_memory` joins the tool ceilings of the architect, lead, worker and qa behaviours (ADR 0029)
     and their built-in roles; non-assistants get accepted entries only, each marked
     `applies_to_you`. Reviewers do not get it (they read the most injectable input there is and need
     no conventions beyond their prompt).
   - `propose_memory` (assistant) and the user's API accept optional `roles` and `paths` (validated:
     known role names, clean relative paths as for task files, at most 10 each).
   - `GET /api/memory/export` returns the accepted entries as Markdown (`docs/memory.md`-style, a
     download); the UI has an Export button. The database stays the source (ADR 0034).
5. **Settings label.** `Start` stores the effective value (false for sandboxed agents); migration
   0009 fixes existing sandboxed rows; the card shows "sandbox config" for sandboxed agents instead
   of "user settings on/off".

## Changes

| Area | Files |
|---|---|
| Agent adapter | `internal/agent/claudecode.go` (+ tests): `ResumeFrom` in both specs |
| Terminal | `internal/terminal/session.go` (+ test): change counter, `Snapshot` |
| Engine | `relaunch.go` (resume path), new `resume.go` (Resume, restore, history files), `engine.go` (resume_state on shutdown/reconcile, settings value, exit reason), `usage.go` (forked transcripts), `assistant.go` (memory scope, list_memory), `messages.go` (delivery after SessionStart without a prompt), `workspace.go` (remove deletes history), `team.go` |
| MCP | `internal/mcp/tools.go`: `list_memory` output field, `propose_memory` scope |
| Roles | `internal/role`: ceilings, built-in role files, `docs/roles.md` |
| Store | `migrations/0009_resume.sql`, `0010_memory_scope.sql`, `queries.sql`, `make generate` |
| API | `internal/api/server.go` (+ tests): resume, sessions, saved terminal, memory export |
| CLI | `cmd/agent-office/main.go`: `--restore-agents`, history dir |
| UI | `web/src`: Resume button, session picker and saved terminal, memory scopes and export, settings label |
| e2e | `test/e2e`: restart restores a working agent with its conversation (host and sandbox) |
| Docs | ADR 0035 (resume and restore), ADR 0036 (session history on disk), ADR 0037 (relevant memory); ADR 0011, 0022, 0026, 0034 amended; architecture, threat model, plan.md, PROGRESS.md, README |

## Data / API changes

- Migration 0009: `sessions.resumed_from TEXT NOT NULL DEFAULT ''`, `agents.resume_state TEXT NOT NULL
  DEFAULT ''`; `UPDATE agents SET inherit_user_settings = 0 WHERE sandbox = 'podman'`.
- Migration 0010: `memory.roles`, `memory.paths` (`TEXT NOT NULL DEFAULT '[]'`).
- `POST /api/agents/{id}/resume {prompt?}`, `GET /api/agents/{id}/sessions`,
  `GET /api/agents/{id}/sessions/{sid}/terminal`, `GET /api/memory/export`; `roles`/`paths` on memory
  entries; exit reason `resume_failed`; `AgentInfo.resumable` (with the reason when not).
- `--restore-agents` / `AGENT_OFFICE_RESTORE_AGENTS` (default true).

## Risks & edge cases

- **Undocumented CLI behaviour:** `--fork-session` with `--session-id` is documented in `--help`, the
  copied transcript and cumulative `cost-state` are not; both are checked by tests with fixtures in
  that shape, and the e2e checks them live.
- **Context size:** a resumed long conversation costs its (cached) input on the next turn; idle
  restores spend nothing until prompted. Budgets still apply per session and goal.
- **A resumed worker after a review round** keeps `report_done` without the round's commit check:
  those workers are not restored (they wait for "Send to worker").
- **Two daemons / two modes:** a resume in the other sandbox mode is refused, as for relaunches.
- **Saved terminals hold whatever the agent printed** (prompts, code, possibly a pack secret it
  echoed): user-only files in the data dir, deleted with the agent and by retention; never in logs,
  the repository or the database.
- **Memory scopes that are too narrow** hide a note from an agent's prompt; `list_memory` still shows
  everything, and the prompt says so.
- **Restore storms:** restore is bounded by `--max-agents` and runs once at startup.

## Test plan

- Unit: resume args (`TestCommandResume`, `TestContainerArgsResume`), `Snapshot` change counter,
  memory scope selection and prompt order, scope validation, Markdown export, forked transcript
  usage (fixture: a parent and a fork with a copied message and a cumulative cost-state).
- Engine (fake CLI understands `--resume`/`--fork-session` and an unknown session):
  `TestResumeKeepsAgentAndWorkspace`, `TestResumeRefusals`, `TestResumeFailed`,
  `TestRestoreAfterCrashAndShutdown` (working agent gets the restart prompt, idle one none, QA and
  starting agents are not restored, slot limit, notice), `TestRestoreOff`,
  `TestResumeDeliversQueuedMessages`, `TestSessionHistory` (saved during and after a run, listed and
  read after a restart, deleted with the agent and by retention), `TestHostUsageForkedTranscript`,
  `TestMemoryRelevance`, `TestListMemoryForWorkers`, `TestSandboxedSettingsLabel`; sandbox variant of
  resume in `make sandbox-test`.
- Store: migration 0009/0010 test (existing rows, settings fix).
- API: resume, sessions, saved terminal (unknown session, other agent's session), export.
- UI: Vitest for memory scope helpers; Playwright: Resume button, saved terminal of an earlier run,
  memory scopes and export.
- e2e: host and sandbox: start an agent with a codeword task, SIGKILL the daemon mid-run, restart,
  the agent is restored and its next answer recalls the codeword; usage of the forked session is not
  double-counted.

## Steps (each one or more commits with `make check` green)

1. `docs:` this plan.
2. `fix:` settings label (migration 0009 part, UI label).
3. `feat(agent):` resume flags.
4. `feat(engine):` Resume, `resume_failed`, delivery and usage for forked sessions; API and UI button.
5. `feat(engine):` restore after a restart (`resume_state`, `--restore-agents`, notice).
6. `feat:` session history (terminal snapshots, API, UI).
7. `feat:` relevant memory (migration 0010, prompt selection, `list_memory` for more roles, export, UI).
8. `test(e2e):` restart restores with context; `make sandbox-test`, `make ui-test`, `make e2e`.
9. `docs:` ADRs 0035–0037, amendments, architecture, threat model, plan.md, PROGRESS.md, README.

## Open questions (answered with the recommended option)

1. **Fork or reuse the session id on resume?** Fork (`--fork-session --session-id <new>`): one row
   and one id per CLI process stays true (ADR 0011, 0028), the hook session check is unchanged, and
   the old transcript stays as it was. Reusing the id is refused by the CLI with `--session-id`
   anyway, and without it the daemon would not know the id before the first hook.
2. **Restore automatically or only by hand?** Automatically, default on, opt-out flag: the DoD says
   a restarted daemon restores agents; idle restores cost nothing; mid-turn ones are what the user
   was paying for anyway, and budgets bound them.
3. **Switch relaunches to resume?** No (out of scope above), recorded in ADR 0035.
4. **Persist terminal output, or rebuild it from transcripts?** Persist the scrollback: it is what
   the user saw, works the same for host and sandboxed agents (sandbox transcripts belong to the
   subordinate uid and are 0600), and needs no transcript renderer.
5. **How is memory "relevant"?** Explicit scopes (roles, paths) the user controls, not keyword
   matching or a model: deterministic, testable, visible in the UI.
6. **Who may read memory with a tool?** Every behaviour but reviewers; accepted entries only.
7. **Export format?** Markdown download; no write into the repository by the daemon.

## Changes during the phase

- **`TestGitHubLoop` became flaky (about one run in five) with the new relaunch timing.** Cause: a
  pre-existing race in building a pull request's view from a row read earlier plus reviews read
  later, so an update could show a started follow-up whose round was not counted yet. First fixed
  by reading both in one transaction inside `prView`; the review below narrowed it: `prView` shows
  the caller's row again (it may carry an unsaved error), and only published updates re-read the
  row and its reviews in one transaction. 8 of 8 runs pass (they also did on `main`).
- **Session history shows earlier daemon runs too.** The UI used to render "terminal no longer
  available" for agents of a previous run before the terminal pane was reached; it now always opens
  the terminal pane, which shows the saved sessions.
- **Restore waits for the sandbox restart imports** (`restartImports`), so a restored worker never
  starts before its work is in the main repository.
- **Independent review** (a separate reviewer agent on the diff) found two bugs and two smaller
  issues, all fixed with tests: a failed resume left its session row as the "latest" one, so every
  later resume pointed at a conversation that never existed (the row and its saved terminal are now
  deleted; `TestResumeFailed` resumes again and continues the original); a shutdown during a restore
  could clear the states of agents not yet restored (states are now cleared per agent, and the
  restore stops at a shutdown; `TestRestoreStopsAtShutdown`); the `prView` narrowing above; and a
  history file that the exit's last save could write after the agent's removal (removal waits for
  it).
- **e2e:** the host test's first restart now runs with `--restore-agents=false` (its checks of
  exited agents stay), then resumes an agent that must recall a codeword from its first
  conversation, crashes the daemon and checks the automatic restore. The sandbox test checks that
  the crashed worker is restored after its restart import and recalls the file it created; its
  budget part moved to a later restart.
- **Not run:** `TestGitHubEndToEnd` (needs `AO_E2E_GITHUB_REPO` and pushes to GitHub); the phase
  touches GitHub code only in how a pull request update is read, which `TestGitHubLoop` and the
  other GitHub tests cover against the fake.
