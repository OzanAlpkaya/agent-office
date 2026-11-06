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
| `web/` | React + Vite + TypeScript UI, embedded into the binary |
| `docs/plan.md` | Phased roadmap with Definitions of Done |
| `docs/adr/` | Architecture Decision Records |
| `PROGRESS.md` | Phase checklist and progress log |

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
- **After each phase:** update `PROGRESS.md` and explain in plain language what was built and why,
  including trade-offs and alternatives, so it can be discussed in interviews.
- Record every significant decision as an ADR in `docs/adr/`: copy `0000-template.md`, number
  sequentially, cover context, options, decision and consequences.

## Git

- One branch per phase: `phase-N/<short-slug>`. Never commit directly to `main` after the initial commit.
- Small, focused commits with Conventional Commits messages (`feat:`, `fix:`, `refactor:`, `test:`,
  `docs:`, `chore:`).
- Never push to `main` without approval. Never push, merge or open PRs unless explicitly asked.

## Quality

- Tests for core logic (engine, hooks parsing, store, MCP tools). Integration tests at DB/API boundaries.
- Before committing: `gofmt -l .` is empty, `go vet ./...` and `go test ./...` pass (plus the web
  typecheck/lint/build once `web/` exists). Report real output; never claim untested results.

## Security

- No secrets in code, logs, test fixtures or docs. Config comes from environment variables
  documented in `.env.example` (created when the first variable is introduced).
- Captured agent data (hook payloads, session files, transcripts) may contain prompts and local
  paths: keep it in gitignored folders and never paste it into docs; use redacted examples.
- Never modify the user's global `~/.claude/settings.json`; experiments use project-local settings.

## Originality

- Inspired by jsm-hq and AgentSpace. Do not copy their names, text, code or assets.
