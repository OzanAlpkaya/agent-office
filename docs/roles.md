# Team roles

A **role** configures one kind of agent (ADR 0029): what it is told, which model it runs, which
team tools and Claude Code tools it has, whether its workspace is read-only, and whom it reports
to. Roles are data: a security, docs or devops role is a YAML file, not code.

A role sits on top of a **behaviour** that agent-office implements. The behaviour fixes the
agent's authority (what its team tools may do, what its report means, its lifetime); the role can
only narrow it. The workflow around the behaviours is described in the architecture
(`docs/architecture.md`, "Team workflow") and ADRs 0029–0034, 0037.

| Behaviour | What it does | Team tools at most | Workspace | Reports to |
|---|---|---|---|---|
| `architect` | agrees the goal's plan with you before work starts; ends when you approve it | `list_tasks`, `send_message`, `message_user`, `submit_plan`, `list_memory` | read-only | `user` |
| `lead` | the Tech Lead: executes the plan, assigns sub-tasks, starts QA, stays until the goal is done | `create_task`, `assign_task`, `list_tasks`, `send_message`, `message_user`, `start_qa`, `report_done`, `request_access`, `list_memory` | read-only | `architect`, `user` |
| `worker` | builds one sub-task on its own branch | `list_tasks`, `send_message`, `message_user`, `report_done`, `report_no_change`, `request_access`, `list_memory` | read-write | `lead` |
| `qa` | merges the reported branches, tests them, reports bugs | `list_tasks`, `send_message`, `report_qa`, `request_access`, `list_memory` | read-write | `lead` |
| `reviewer` | reviews one pull request | `list_tasks`, `submit_review` | read-write | none |
| `assistant` | briefs you across goals and proposes project memory | `get_briefing`, `message_user`, `list_memory`, `propose_memory` | read-only | `user` |

Who may message you is decided by agent-office, not the role: the architect and the assistant;
a Tech Lead whose goal has no architect (or whose role says `reports_to: user`); a worker without
a running lead (ADR 0030).

`list_memory` (Phase 9, ADR 0037) shows every accepted project memory note, marked whether it
applies to the caller's role and task files; only the assistant also sees proposals. A user role
that lists its `tools.team` gets it only if it names it.

## Where roles come from

| Source | Location | Notes |
|---|---|---|
| Built in | the binary (`internal/role/builtin`) | `architect`, `tech-lead`, `worker`, `qa`, `reviewer`, `assistant`, and the examples `docs` and `security` (worker behaviour) |
| Yours | `~/.config/agent-office/roles/<name>/role.yaml` (`AGENT_OFFICE_ROLES_DIR`, `--roles-dir`) | must belong to you; nothing group- or world-writable; no symlinks |

Roles are **never read from the repository** the agents work on. A role of yours with a built-in's
name **replaces** it (and must keep its behaviour): to change the Tech Lead's prompt, copy
`internal/role/builtin/tech-lead/role.yaml` to `~/.config/agent-office/roles/tech-lead/` and edit
it. agent-office starts the roles named `architect`, `tech-lead`, `qa`, `reviewer` and `assistant`
for those jobs; any role with the `worker` behaviour can build a sub-task.

```sh
agent-office role list           # every role, its behaviour, workspace, model and source
agent-office role check <dir>    # validate a role directory and print every problem
```

An invalid role of yours stops the daemon at startup with every problem and its fix.

## Format

```yaml
schema: 1                     # required; the format version
name: devops                  # required; equals the directory name; [a-z][a-z0-9-]{0,31}
description: Owns CI and the release scripts.   # required; one line, <= 300 characters
behaviour: worker             # required; one of the behaviours above
model: sonnet                 # optional; an alias or a model id; empty = --model
reports_to: lead              # optional; must be allowed for the behaviour; default: the first one
workspace: read-write         # optional; read-only | read-write; default: the behaviour's
tools:
  team: [list_tasks, send_message, report_done, request_access]   # optional; a subset of the behaviour's
  deny: [WebFetch, "Bash(curl:*)"]   # optional; Claude Code permission rules the agent cannot use
prompt: |                     # required; <= 12 000 characters
  You are the devops engineer of a small team of AI coding agents run by agent-office. ...
```

- **`tools.team`** narrows the behaviour's team tools. The tool that hands the result back is
  required (`report_done` for workers, `submit_plan` for architects, `report_qa` for QA, ...).
- **`tools.deny`** goes into `permissions.deny` of the agent's generated Claude Code settings.
  Deny rules hold with `--dangerously-skip-permissions` (checked with Claude Code 2.1.319): a denied
  tool is not offered to the model at all. See Claude Code's permission rule syntax.
- **`workspace: read-only`** denies `Edit`, `Write` and `NotebookEdit` and, in sandbox mode,
  mounts the workspace read-only, so Bash cannot change it either. In host mode Bash still asks
  you, as every tool does there.
- **`prompt`** comes first in the agent's first prompt. agent-office then adds what is protocol or
  safety: the goal or task, the plan, how to report, the network rule, how messages are framed, and
  the accepted project memory. A role cannot change how work is reported. The lead's prompt may use
  `{max_sub_tasks}` and `{max_workers}`.

## Models per role

A role's `model`, else `--model`. Per run, `--role-model` (`AGENT_OFFICE_ROLE_MODELS`) sets models
by role name without editing files:

```sh
agent-office serve --repo . --role-model architect=opus,tech-lead=sonnet,worker=sonnet,qa=sonnet,assistant=haiku
```

The built-in roles have no model of their own, so they follow `--model` until you say otherwise.
With a budget (ADR 0027), a full model id without a price stops the daemon at startup, as for
`--model`.

## Starting agents by role

- **Board:** a task's **Start agent** offers every worker role; a goal has **Start architect** (the
  plan first) and **Start Tech Lead** (skip the architect for small goals). **Briefing** in the
  header starts the assistant.
- **Tech Lead:** `create_task(role=...)` or `assign_task(role=...)` picks a worker role for a
  sub-task; `list_tasks` lists them. An architect's plan can name a role per sub-task.
- **API:** `POST /api/agents {"task_id": ..., "role": "docs"}`; `"lead"` is still accepted for
  `tech-lead`. Reviewers and QA are started by agent-office only.
