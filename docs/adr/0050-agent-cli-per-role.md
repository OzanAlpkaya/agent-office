# 0050. The agent CLI is chosen per role, like the model

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

With Codex next to Claude Code (ADR 0049), a team may mix them: a Claude Tech Lead with Codex
workers, or a Codex reviewer of Claude workers' pull requests. Something has to say which agent
runs on which CLI, and with which model.

## Options considered

1. **Per agent start** (a CLI picker on every Start). Fine for the user's own starts, but the Tech
   Lead's `assign_task` and the review loop start agents too.
2. **Per project** (one CLI for the whole team). Does not allow a mixed team.
3. **Per role, with the override rules of models** (ADR 0029, 0046): a role file's `cli:`,
   `--role-cli role=cli` as the daemon default, a project's `role_clis` over it.

## Decision

Option 3. `cli: claude | codex` in role files (default `claude`); `--role-cli` / `AGENT_OFFICE_ROLE_CLIS`;
`role_clis` in project settings (and `project add|set --role-cli`). An agent records its CLI when
it starts (`agents.cli`); relaunches, resumes and restores keep it, whatever the role says later.

- **Models:** a role's model wins as before; a Codex role without one uses `--codex-model` (or a
  project's `codex_model`), else Codex's own default. `--model` stays the Claude Code default, so a
  Codex agent is never started with a Claude model name.
- **Deny rules** in a role (`tools.deny`) are Claude Code permission rules; a role that has them
  cannot run on Codex (refused at load or override). A read-only role on Codex relies on the
  read-only workspace mount, as Bash does for Claude Code.
- **Authority is unchanged:** the CLI does not change a role's behaviour, team tools or reports;
  the team MCP server is the same for both.
- **Usage per vendor:** each session's CLI names the vendor (anthropic, openai); the usage report adds
  totals per vendor, and Codex tokens stay unpriced unless the user's price table has the model.

## Consequences

- A mixed team needs no code: `--role-cli worker=codex` or one line in a project's settings.
- The UI shows a codex tag on agents and in the role picker, and a table per vendor in Usage.
- Host-mode daemons refuse Codex roles at start (in their defaults) or at the agent's start.
