# agent-office: project rules

agent-office is a local tool that runs a team of AI coding agents (Claude Code, Codex, ...) on a
git repository. A lead agent plans and assigns tasks; worker agents each get their own git worktree
and sandbox; a web UI shows a task board, live terminals and, later, a pixel-art office.

These rules apply to every session in this repo.

## Repository layout

| Path | Purpose |
|---|---|
| `cmd/agent-office/` | Daemon entry point (single binary) |
| `internal/agent/` | Agent adapters (Claude Code, Codex, ...) |
| `internal/engine/` | Orchestration core: agent/task lifecycle, worktrees, message routing |
| `internal/hooks/` | Agent lifecycle events (Claude Code hooks) → agent state |
| `internal/mcp/` | Team MCP server used by agents to coordinate |
| `internal/store/` | SQLite persistence (modernc.org/sqlite + sqlc) |
| `internal/github/` | GitHub integration via go-github |
| `internal/sandbox/` | Agent isolation (rootless Podman, egress allowlist) |
| `internal/api/` | HTTP + WebSocket API for the web UI |
| `internal/terminal/` | PTY sessions: scrollback, fan-out to viewers, input/resize |
| `internal/git/` | git CLI wrapper: worktrees, branches, branch-name slugs |
| `web/` | React + Vite + TypeScript UI, embedded into the binary |
| `test/e2e/` | Opt-in end-to-end test (`e2e` build tag, real Claude Code, costs tokens) |
| `docs/plan.md` | Phased roadmap with Definitions of Done |
| `docs/plans/` | Approved phase plans (`phase-N.md`), with answers and changes during the phase |
| `docs/adr/` | Architecture Decision Records |
| `docs/architecture.md` | Components, flows, domain model, threat model |
| `docs/research/` | Verified library/API notes and external tool research |
| `docs/releases/` | Release notes per tag (`make release` packs them; ADR 0042) |
| `docs/screenshots/` | README images and the demo GIF (`make demo`, `docs/demo.md`) |
| `spikes/` | Throwaway experiments, each its own Go module; never imported by `internal/` |
| `PROGRESS.md` | Phase checklist and progress log |

## Playground repo

`~/projects/agent-office-playground` (outside this repo) is a small Go project ("linkshelf") used
for end-to-end tests and demos: agents work on it in Phase 1, worktrees in Phase 2, GitHub in
Phase 5. Keep it small and realistic; its backlog is in its `TODO.md`.

## Stack

- **Daemon (Go):** `net/http`, `github.com/coder/websocket`, `github.com/creack/pty`,
  `github.com/modelcontextprotocol/go-sdk`, `modernc.org/sqlite` + `sqlc`,
  `github.com/google/go-github`, `log/slog`.
- **Web UI:** React + Vite + TypeScript, xterm.js for terminals, PixiJS later for the office view.
  Built assets are embedded into the Go binary with `go:embed`.
- Before using a library API, check its current documentation; do not guess signatures.
- New dependencies need a reason; mention them in the commit message and the phase summary.

## Platform and isolation

- Linux-first (developed on CachyOS). Other platforms are not a goal for now.
- Worker isolation via **rootless Podman**. Also evaluate Claude Code's built-in sandbox and record
  the comparison in an ADR.
- Agents run with full permissions **only inside the sandbox**, never on the host.

## GitHub

- **Never use the `gh` CLI.** GitHub API access goes through go-github with a token read from the
  environment.
- Git operations use SSH (remote host alias `github-personal`).

## Language

- Everything in the repo is in English: code, comments, docs, commit messages.

## Workflow

- Work phase by phase from `docs/plan.md`.
- **Before each phase:** propose a short plan (goal, files, approach, risks) and wait for approval.
  Once approved, save it as `docs/plans/phase-N.md` (as approved, plus the answers to its open
  questions) as the first commit of the phase branch, and append changes made during the phase.
- **During a phase:** once the plan is approved, execute all of its steps without stopping to ask
  whether to continue. Make the decisions yourself, record significant ones in ADRs, and list every
  decision in the end-of-phase summary for review. Stop only when the phase is done, when something
  needs Ozan (sudo, a token, a manual UI check), or when a decision is irreversible or would change
  the approved plan's scope.
- **After each phase:** update `PROGRESS.md` and explain in plain language what was built and why,
  including trade-offs and alternatives, so it can be discussed in interviews.
- Record every significant decision as an ADR in `docs/adr/`: copy `0000-template.md`, number
  sequentially, cover context, options, decision and consequences.

## Git

- One branch per phase: `phase-N/<short-slug>`. Never commit directly to `main` after the initial commit.
- Small, focused commits with Conventional Commits messages (`feat:`, `fix:`, `refactor:`, `test:`,
  `docs:`, `chore:`).
- Never push to `main` without approval. Never push, merge or open PRs unless explicitly asked.
- Merging a phase branch: only after Ozan approves, with `git merge --no-ff` to keep the commit
  history (no squash; this overrides the global default). Ozan pushes himself.
- Stage explicit paths, never `git add -A`: Claude Code writes temp files under `.claude/`.
- **No tags and no releases** (no `git tag`, no `make release`, no release notes for a new version)
  until Ozan says the project is finished.

## Quality

- Tests for core logic (engine, hooks parsing, store, MCP tools). Integration tests at DB/API boundaries.
- Before committing: `make check` passes (gofmt, vet, web typecheck, `go test -race`, build).
  Report real output; never claim untested results.
- **Never run the daemon, smoke tests or ad-hoc commands against the real data dir**
  (`~/.local/share/agent-office`) during development. Always pass `--data-dir` or
  `AGENT_OFFICE_DATA_DIR` pointing at a scratch dir outside this repo (Claude Code loads
  `CLAUDE.md` from parent folders). `make serve` (`DEV_DATA_DIR`, default
  `~/.cache/agent-office-dev/data`), `make test` and the e2e test do this by default; ad-hoc runs
  use the session scratchpad.
- `make e2e` (needs `AO_E2E_REPO`) spends real tokens: run it at the end of a phase or when hooks,
  engine or worktree behaviour change, not on every commit.
- SQL changes go into a new numbered migration plus `queries.sql`, then `make generate`.
  Migrations are forward-only.

## Security

- No secrets in code, logs, test fixtures or docs. Config comes from environment variables
  documented in `.env.example` (created when the first variable is introduced).
- Captured agent data (hook payloads, session files, transcripts) may contain prompts and local
  paths: keep it in gitignored folders and never paste it into docs; use redacted examples.
- Never modify the user's global `~/.claude/settings.json`; experiments use project-local settings.

## Originality

- Do not copy code, names or assets from other projects.
