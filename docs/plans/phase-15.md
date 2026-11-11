# Phase 15 plan: Codex adapter

- **Status:** Done on `phase-15/codex-adapter`; approved by Ozan and merged into `main` (2026-11-11). Written 2026-11-11 with the recommended option
  for every open question, as Ozan asked ("run it like the earlier phases"), with one exception he
  set: if the spike shows that the real credential would have to be inside the container, the phase
  stops and he decides.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 15. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites:** Codex CLI installed (0.167.1, signed in with Ozan's ChatGPT plan, no API key);
  `make e2e` spends ChatGPT plan usage for the Codex agents and a few cents of haiku.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A team can mix agent CLIs: a Claude Code Tech Lead with Codex workers, or a Codex reviewer of a
Claude worker's branch. A Codex agent gets what a Claude Code agent gets: a PTY and a live terminal,
states from hooks, the team MCP tools, its usage, the network-less sandbox with the egress allowlist,
and a credential that never enters its container.

## What exists and what it means

- **The adapter is a concrete type.** `internal/agent` has `ClaudeCode` (command line, settings,
  hook parsing, sandbox seed config, container args); the engine holds one `agent.ClaudeCode` and
  calls it directly. The hook pipeline (`agent-office hook` → per-agent socket → state machine) is
  CLI-neutral already; `agent.ParseClaudeHook` is not.
- **The credential design (ADR 0015, 0016)** puts a placeholder token in the container and points
  the CLI's API base URL at a loopback bridge to a per-agent API socket, where the daemon adds the
  real token and meters usage. Whether Codex can do the same with a ChatGPT-plan login is the open
  question: its auth is an OAuth login in `~/.codex/auth.json` (access, refresh and id tokens), not
  an API key, and the agent-office side must never read or print it during development.
- **What the installed CLI shows** (from `codex --help` and the binary, no session run yet): a
  static-pie musl binary (it can be mounted read-only into a worker like Claude Code's), `-c key=value`
  config overrides, `CODEX_HOME`, model providers with `base_url` and `requires_openai_auth`,
  `chatgpt_base_url`, `CODEX_REFRESH_TOKEN_URL_OVERRIDE`, `--dangerously-bypass-approvals-and-sandbox`
  and `--dangerously-bypass-hook-trust` "for environments that are externally sandboxed", `resume`
  and `fork`, and hook events incl. `PostModelRequest` with `token_usage` (Phase 0 research).
- **Reviewers start for a pull request** (ADR 0021): a Codex reviewer needs the GitHub loop or its
  fake (`githubtest`).

## Out of scope

- An OpenAI API key login (Ozan's plan is ChatGPT); the adapter keeps room for it.
- Codex's own sandbox (`workspace-write`, approvals): agent-office's container is the sandbox, as
  for Claude Code; host-mode Codex agents keep Codex's approval prompts on.
- Codex Cloud, `codex review`, the app-server protocol as the main interface (a PTY with hooks
  stays the model), plugins.
- Prices for OpenAI models: none are verified for this phase, and ChatGPT plan use is not billed per
  token; Codex usage is shown in tokens, and a price appears only for models in the user's
  `prices.yaml` (ADR 0026: unpriced tokens are shown as such).

## Sub-phases

### 15.0 Spike G: Codex CLI (`spikes/codex/`, throwaway)

Run against the installed CLI, with the user's own `~/.codex` for login only (never read, copied or
printed by agent-office; Codex's own token refresh must not be disturbed), overrides through `-c`
so the user's config is not changed:
1. **Hooks:** every event in an interactive PTY session and in `exec`; payload fields
   (`session_id`, `cwd`, `transcript_path`, `model`, tool names, permission requests, stop);
   hook trust; which events map to starting / idle / working / waiting_permission / exited.
2. **Usage:** `PostModelRequest.token_usage`, rollout files, `exec --json`; what the Responses API
   stream itself carries (for a meter on the API socket).
3. **Resume:** `codex resume <id>` / `fork`; whether a session id can be chosen or must be learned
   from the first hook; what a resumed session's hooks carry.
4. **Sandbox and credential:** Codex in the network-less worker with a placeholder login, its
   ChatGPT backend base URL pointed at a loopback bridge, the daemon side adding the real
   credential (and refreshing it) outside the container; egress it needs besides the model API.
5. **Team MCP:** the `agent-office mcp` stdio bridge as a Codex MCP server.

**Gate:** if (4) shows that the real access or refresh token would have to be inside the container,
stop and ask Ozan. Otherwise the findings go into `docs/research/codex-cli.md` and an ADR.

### 15.1 The agent interface and the Codex adapter

- `agent.Adapter` with what the engine needs per CLI: command line (host and container), hook
  parsing into the common events, MCP config, sandbox seed/config files, resume arguments, the
  API upstream and how usage is read. `ClaudeCode` and `Codex` implement it; the engine picks the
  adapter per agent.
- Agents record their CLI (`agents.cli`, migration 0014; existing rows `claude`).

### 15.2 Roles and projects choose the CLI and model

- `cli: claude | codex` in a role file (default `claude`); `--role-cli role=cli` as a daemon default,
  `role_clis` in project settings (ADR 0046's override rule); models stay per role and project.
- The UI shows each agent's CLI and the role picker the role's CLI.

### 15.3 Codex in the sandbox, MCP and usage

- The Codex binary mounted read-only; a per-agent `CODEX_HOME` with a placeholder login and
  config (hooks, MCP server, provider base URL); the credential injected on the API socket.
- Usage per vendor: the API socket's meter and/or hooks, stored with the CLI; the Usage tab and
  `/api/usage` show tokens per vendor and model.

### 15.4 Reviewer, restart, e2e and docs

- A Codex reviewer through the existing review loop (engine test with the GitHub fake; live in e2e).
- Restore and resume for Codex agents as the spike allows; tested, or the limits documented.
- `TestCodexTeamEndToEnd`: a sandboxed team with a Claude Tech Lead and a Codex worker; a Codex
  reviewer of a Claude worker's branch.
- ADRs (Codex adapter and credential; CLI per role), `docs/research/codex-cli.md`, README,
  architecture and threat model, PROGRESS.

## Risks

- **The ChatGPT login may not be injectable** (refresh tokens, account headers, JWT claims checked
  locally). That is the gate above.
- **Token refresh outside the CLI** could race the user's own Codex CLI for the same login
  (refresh-token rotation). The spike checks it; a dedicated login for agent-office may be needed.
- **Codex changes fast** (0.167.1 today); like ADR 0016 for Claude Code, keys are tested per version
  and the daemon warns on another one.
- **Hooks may not cover every state** (e.g. a permission prompt in the TUI); the state machine keeps
  its input-aware fallbacks.

## Answers to open questions

| Question | Answer |
|---|---|
| Real credential in the container? | Ozan decides, if the spike shows it is needed. |
| Codex's own sandbox? | Off inside agent-office's container (bypass flags); host mode keeps Codex's prompts. |
| How do roles pick the CLI? | `cli:` in the role, `--role-cli` and project `role_clis` override, like models. |
| OpenAI prices? | None built in; tokens per vendor; prices only from the user's `prices.yaml`. |
| Codex reviewer without GitHub? | Engine test with the GitHub fake; live with the GitHub e2e or the fake. |

## Changes during the phase

- **Spike G answered the gate: no stop.** With a placeholder ChatGPT login and its backend base URLs
  at a proxy, every Codex request carries the placeholder and the daemon side adds the real access
  token; the container never holds it. Two findings shaped the design: the TUI requires HTTPS for the
  ChatGPT base URL (TLS ends on the daemon side of the API socket, with an in-memory loopback CA the
  container trusts through `CODEX_CA_CERTIFICATE`), and it checks the login's account id, so the
  placeholder carries the user's real **account id and plan** (identifiers, not credentials; listed
  in ADR 0049). The refresh token is never read; agent-office never refreshes the login.
- **Codex runs sandboxed only.** Host mode was dropped from the scope (decision): the credential
  could not be kept from a host process.
- **Sessions.** Codex picks its own session id (learned from the first SessionStart, kept in
  `sessions.cli_session_id`, migration 0014) and resumes under the same id; SessionStart comes only
  with the first prompt, so prompt-less Codex agents are marked idle once their TUI is up (found by
  the e2e restore step).
- **Messages** reach Codex agents typed in when idle; the Stop-hook reply is Claude Code's.
- **Usage:** ChatGPT's backend sends the Responses stream without a Content-Type, so the first e2e run
  metered nothing; untyped streams are metered now.
- **The adapter "interface"** is per-CLI functions in `internal/agent` (hook parsing, config, command
  line) chosen by the agent's recorded CLI, not one Go interface: the two CLIs differ in most of the
  launch (files, mounts, session ids, delivery), so a shared interface would have been as large as
  the two adapters.
- **Codex reviewer:** proven with the GitHub e2e on the playground repository (`AO_E2E_REVIEWER_CLI`,
  new) and a sandbox test with the GitHub fake; the e2e pushed a branch and opened a pull request
  there and closed it afterwards, as the Phase 5 runs did.
- **Independent code review** (5 fixes): a prompt-less Codex resume that exits before its first
  prompt was recorded as a failed resume (and the label named Claude Code); the Codex session id is
  recorded before it is used; a login read during the user's own refresh uses the last good login; a
  log field reported the CA instead of the login; Start records the CLI with its default.
- **Independent security review** (no path for the real token into the container; 4 fixes): the
  socket drops cookies, blanks the user's name, email and picture in account answers, and refuses
  encoded paths and protocol upgrades; the session id is learned only from a startup or resume
  SessionStart; DEL is escaped in the generated TOML; `CODEX_`/`OPENAI_` variables are reserved from
  packs. Residuals are listed in ADR 0049.
- **Ozan's review (2026-11-11)**: all decisions approved, including the real account id and plan type
  in the placeholder login; tested by hand in a real browser on the real daemon (a Claude Tech Lead
  with a Codex worker, the role picker, the Codex terminal with no login dialog, usage per vendor, a
  SIGKILL restart that recalled PLUM73, only placeholders in the container's `auth.json`). Before the
  merge: a goal whose Tech Lead (or architect) cannot start because another goal's runs now says so
  on its card, with what to do and a button to open the running one (the reason was only in the
  disabled button's tooltip, which browsers do not show; Vitest `goalBusy` and Playwright "a second
  goal says why its Tech Lead cannot start yet"). The Codex terminal's "1 warning" is Codex's notice
  that `--dangerously-bypass-hook-trust` is on: expected, since agent-office passes it on purpose (its
  hooks are its own, mounted read-only); it does not matter. "Several goals at once in one project"
  is in the backlog.
