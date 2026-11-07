# Phase 6 plan: Stack packs

- **Status:** In progress on `phase-6/stack-packs`. Written 2026-11-07 with the recommended option
  for every open question and executed straight away, as Ozan asked ("write the Phase 6 plan using
  your recommended options, then execute it immediately without asking me anything"). Every
  decision is listed in the end-of-phase summary; nothing is merged until Ozan has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 6. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Prerequisites:** none beyond Phase 3's (rootless Podman, the OAuth token file). Building pack
  images needs network access, like `make worker-image` today.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

A project's sandboxed agents are configured by one declarative YAML **stack pack**: the worker
image (toolchain) they run in, the hosts they may reach, the secrets they get (by env var name),
the Claude Code skills they can use and extra MCP servers. Packs are validated strictly, with
error messages that name the file, the field and the fix. Two built-in packs, **go** and **node**,
ship in the binary. A project without a pack gets the default image, which no longer carries a
language toolchain.

Packs are configuration written by the user. They widen what a sandboxed agent can do (network,
secrets), so they are read only from places the user controls, never from the repository the
agents work on, and everything they produce is checked by the same deterministic code as today
(allowlist parser, env checks, mount rules).

## Out of scope

- Packs in host mode: host-mode agents are not sandboxed, so a pack's image, egress and secrets
  would mean nothing there. `--pack` with `--sandbox host` is refused.
- More than one pack per project (composition, `extends`), per-task or per-agent packs.
- Auto-detecting the pack from repository files (the daemon only logs a hint).
- Building images automatically from the daemon, pulling prebuilt images from a registry,
  pinning Debian package versions (Phase 11).
- Plugins and marketplaces, agent definitions (`.claude/agents`), hooks in packs, MCP server
  headers or OAuth.
- Codex.

## Approach

1. **Pack format** (`schema: 1`). A pack is a directory `<name>/` with `pack.yaml` and the files
   it references:
   ```yaml
   schema: 1
   name: go                       # must equal the directory name
   description: Go 1.27 with a C toolchain, so go test -race works.
   toolchain:
     containerfile: Containerfile # relative to the pack; built FROM the base image
   egress:
     allow: [proxy.golang.org, sum.golang.org]   # exact names or .suffix
   env:                           # names only; values are secret files (below)
     - name: GOPRIVATE_TOKEN
       description: read token for private modules
       required: false
   skills: [go-testing]           # skills/<name>/SKILL.md inside the pack
   mcp_servers:
     docs: {url: "https://example.com/mcp"}            # https; host must be in egress.allow
     tool: {command: /usr/local/bin/tool, args: [mcp]} # runs inside the worker
   ```
   Every field except `schema` and `name` is optional.
2. **Validation (`internal/pack`).** Strict YAML decoding (unknown fields are errors, one
   document, at most 64 KiB), then semantic checks collected into one error listing every
   problem as `<file>: <field>: <problem>; <fix>`. Checks: name format and directory match,
   schema version, host names through `sandbox.ParseAllowlist`'s rules, env names (format,
   unique, not reserved: agent-office's own, `CLAUDE_*`, `ANTHROPIC_*`, proxy, git, SSH, GitHub
   token and loader variables), skill names and a `SKILL.md` with a `description` in its frontmatter (a `name`, if
   given, must equal the directory), MCP server names (letters, digits, `-`, `_`; `agent-office`
   and Claude Code's built-in names reserved), exactly one of `command`/`url`, absolute command
   paths, `https` URLs whose host is allowed, Containerfile present with its final `FROM
   ${BASE_IMAGE}`, no path leaving the pack directory, no symlinks. Pack files and directories
   must belong to the user and not be writable by group or others.
3. **Where packs come from.** Built-in packs are embedded in the binary (`internal/pack/builtin`).
   User packs live in `$XDG_CONFIG_HOME/agent-office/packs/<name>/` (`AGENT_OFFICE_PACKS_DIR`).
   A user pack with a built-in's name is an error (copy it under a new name instead). The
   repository is never read for packs.
4. **Selection.** `--pack <name>` (`AGENT_OFFICE_PACK`), one per daemon run, which is one project.
   Empty: no pack. The daemon validates the pack at startup and fails with the error list. If no
   pack is set and the repository has `go.mod` or `package.json`, it logs which built-in pack fits.
5. **Images.** The Phase 3 Containerfile becomes the **base image** (Debian slim pinned by
   digest, ca-certificates, git, curl, ripgrep; no language toolchain), which is also the
   **default image** for projects without a pack. A pack's Containerfile starts `FROM
   ${BASE_IMAGE}` and adds its toolchain. Its tag is derived from the base and pack
   Containerfiles and the pack's other build files, so any change means a rebuild.
   `agent-office worker-image [--pack <name>]` builds the base (if missing) and the pack image.
   The daemon never builds by itself; a missing image fails startup with the command to run.
   - **go:** Go 1.27.3 (checksum-verified download, as today) plus `gcc` and `libc6-dev`, with
     `CGO_ENABLED=1`, so `go test -race` works inside workers.
   - **node:** Debian's `nodejs` and `npm` packages (signed by the Debian archive).
6. **Egress.** With a pack, the allowlist is the pack's `egress.allow`; without one, it is the
   Phase 3 default (`proxy.golang.org`, `sum.golang.org`, `registry.npmjs.org`). The
   `AGENT_OFFICE_EGRESS_ALLOW` extras are added in both cases. Grants (ADR 0019) are unchanged.
7. **Secrets (the pack's env var names).** The value of `NAME` is read, at every launch, from
   `$XDG_CONFIG_HOME/agent-office/secrets/NAME` (`AGENT_OFFICE_SECRETS_DIR`) with the token-file
   rules (ADR 0015: user-owned regular file, 0600/0400, private directory, one printable token).
   Delivery is ADR 0015's socket option, now implemented: a per-agent `secrets.sock` serves the
   values **once**, to `sandbox-init`, which puts them into the agent CLI's environment only.
   They never appear in argv, `podman inspect`, the container config, files, logs or the
   database. Workers and reviewers get them; leads do not (they do not run code). A missing
   required secret fails startup and every launch with the command that creates the file; a
   missing optional one is left out and shown as a warning.
8. **Skills.** Each skill directory is copied into the agent's own Claude Code config dir
   (`$CLAUDE_CONFIG_DIR/skills/<name>`) before the container starts, so the agent sees it as a
   personal skill. The built-in packs each ship one small skill on how to build and test with
   that toolchain inside the sandbox.
9. **MCP servers.** Added to the agent's generated `--mcp-config` file next to the team server.
   `--strict-mcp-config` stays, so nothing else is loaded. Stdio servers run inside the worker
   (the command must exist in the image); HTTP servers go through the egress proxy, so their
   host must be in the pack's allowlist.
10. **UI and API.** `GET /api/status` gains the pack: name, description, image, allowed hosts,
    secrets (names, and whether each file is present), skills and MCP server names. The header
    shows a pack badge with the details on hover.
11. **CLI.** `agent-office pack list` (built-in and user packs, with their validation status) and
    `agent-office pack check <dir>` (validate a pack directory and print every problem).

## Changes

- `internal/pack` (new): types, loader (embedded and directory sources), validation, image tag
  and build context, secret file lookup; `builtin/go`, `builtin/node`.
- `internal/sandbox`: the Containerfile becomes the base image; image tags and builds for packs;
  `WorkerSpec` gains a secrets socket mount; `sandbox-init` fetches and applies secrets; a
  one-shot secrets server.
- `internal/agent`: `MCPConfig` takes extra servers.
- `internal/engine`: `SandboxOptions` gains the pack (servers, skills, secret names, secret dir);
  launch copies skills, writes the merged MCP config, serves secrets; `Status` reports the pack.
- `cmd/agent-office`: `--pack`, `--packs-dir`, `--secrets-dir` (and env), the `pack` command,
  `worker-image --pack`, startup checks and hints.
- `web/`: pack badge and details.
- New dependency: `go.yaml.in/yaml/v3` v3.0.5, the YAML organisation's maintained continuation of
  `gopkg.in/yaml.v3` (which is archived). Needed to read YAML; strict mode rejects unknown fields.
- `Makefile`: `worker-image PACK=<name>`; `e2e` passes `--pack go` to sandboxed runs on the
  playground.
- Docs: ADR 0023 (stack packs), `docs/packs.md` (format reference), architecture and threat
  model, `.env.example`, `docs/plan.md`, `PROGRESS.md`, this file.

## Data / API changes

- No migration: the pack belongs to the daemon run, like the sandbox mode and image today.
- `Status.pack` (optional): `{name, description, source, image, egress, secrets: [{name,
  required, present}], skills, mcp_servers}`.

## Risks & edge cases

- **Packs widen the sandbox.** A pack can allow an exfiltration host or hand an agent a secret.
  Mitigation: packs come only from the binary or a user-owned directory, never from the repo;
  secrets need a file the user creates; the UI shows what the pack grants. Residual (stated):
  an agent can leak a secret it was given through an allowed host.
- **The default image loses Go.** Sandboxed agents on a Go project without `--pack go` can no
  longer build. The startup hint says which pack fits; the e2e passes `--pack go`.
- **Skills location under `CLAUDE_CONFIG_DIR`**: the Claude Code docs say every `~/.claude` path
  moves under it (code.claude.com/docs/en/claude-directory), so personal skills are read from
  `$CLAUDE_CONFIG_DIR/skills/`. Checked again live in the e2e.
- **Secrets socket.** It answers once; if anything but `sandbox-init` connected first, the init
  fails and the agent does not start (visible), never silently without secrets.
- **Build time and size.** The go pack adds gcc (about 150 MB); images are built once per
  Containerfile change.
- **Debian `nodejs` is not the newest Node.** Good enough for the example; a user pack can install
  another version.

## Test plan

- `internal/pack`: both built-ins validate; a table of invalid packs (unknown field, bad or
  mismatched name, wrong schema, bad host, wildcard, IP literal, bad, duplicate or reserved env
  name, missing or traversing skill, `SKILL.md` without frontmatter, reserved MCP name, both or
  neither of `command`/`url`, relative command, `http://` URL, URL host not allowed, missing
  Containerfile, wrong final `FROM`, symlink, group-writable file, oversized file, several YAML
  documents), each with the expected message; all problems reported at once; user packs may not
  shadow built-ins; tags change when any build file changes.
- `internal/sandbox`: worker args with and without the secrets socket; secrets server serves once
  and refuses bad names; `sandbox-init` applies secrets and fails if the socket is missing or
  answers badly; secret values never in args.
- `internal/agent`: MCP config with extra servers, reserved name refused.
- `internal/engine`: sandboxed start with a pack (real Podman, `make sandbox-test`): the pack's
  allowlist is used, skills are in the config dir, the MCP config has the servers, a secret
  reaches the agent CLI's environment and nothing else (`podman inspect`, logs, DB, terminal).
- `cmd/agent-office`: `--pack` with host mode refused; unknown pack and invalid pack errors;
  missing required secret.
- `make sandbox-test`: go pack image runs `go test -race`; node pack image runs `node` and `npm`;
  the base image has no Go.
- `make e2e`: existing sandboxed runs with `--pack go` (real Claude Code, haiku).

## Steps (each one or more commits with `make check` green)

1. Branch `phase-6/stack-packs`; this plan.
2. ADR 0023 (stack packs).
3. `internal/pack`: format, loader, validation, built-in packs, tests; new dependency.
4. `internal/sandbox`: base image and pack images; secrets socket and `sandbox-init`.
5. `internal/agent` and `internal/engine`: pack applied at launch; status.
6. `cmd/agent-office`: flags, `pack` command, `worker-image --pack`; UI.
7. `make sandbox-test` with the built pack images; `make e2e`.
8. Docs (`docs/packs.md`, architecture, threat model, plan, `.env.example`, `PROGRESS.md`), summary.

## Open questions (answered with the recommended option)

1. **Where do packs come from?** The binary (built-ins) and a user-owned config directory. Not
   the repository: its content is influenced by agents (their branches get merged), and a pack
   decides what leaves the sandbox.
2. **How is a pack chosen?** Explicitly, `--pack <name>`; one per project. Auto-detection would
   be convenient but silently changes what agents can reach when a file appears in the repo.
3. **What is the toolchain?** A Containerfile in the pack built on top of agent-office's base
   image, so the parts the sandbox relies on (git, certificates, user, labels) are always there.
   Not an arbitrary registry image: it would need pulling, could miss those parts, and its
   contents are not reviewable in the pack.
4. **What do projects without a pack get?** The base image, without a language toolchain, and the
   Phase 3 default allowlist.
5. **How do a pack's env var names become values?** One user-only file per name in a secrets
   directory, read at each launch, delivered over a one-shot socket to `sandbox-init` (ADR
   0015's documented fallback, now implemented for pack secrets). Not from the daemon's
   environment (ADR 0015 rejected that for the OAuth token) and not with `podman -e` (visible in
   `podman inspect`).
6. **Do pack domains add to or replace the default allowlist?** Replace: the default registries
   exist so agents can build, which is now the pack's job, and a Node project should not reach
   the Go proxy. Projects without a pack keep the default.
7. **Which roles get what?** Every sandboxed role gets the image, allowlist, skills and MCP
   servers; secrets go to workers and reviewers only.
8. **Packs in host mode?** Refused, with a clear error.

## Changes during the phase

(appended during the phase)
