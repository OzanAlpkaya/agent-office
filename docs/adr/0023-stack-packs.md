# 0023. Stack packs: user-controlled YAML that picks the image, egress, secrets, skills and MCP servers

- **Status:** Accepted
- **Date:** 2026-11-07
- **Amends:** ADR 0016 (the default allowlist becomes the fallback for projects without a pack),
  ADR 0015 (socket delivery, kept as a fallback there, is implemented for pack secrets), ADR 0007
  (one worker image becomes a base image plus pack images)

## Context

Phase 3 gave every sandboxed worker one image with the playground's toolchain (Go, no cgo) and
one allowlist (the Go and npm registries). Real projects need other toolchains (Node, a C
compiler for `go test -race`), other hosts, credentials for private registries or test services,
and project-specific instructions and tools for the agent. The roadmap asks for declarative YAML
packs that configure this per stack.

Forces:

- A pack **widens the sandbox**: it decides which hosts an agent can reach and which secrets it
  holds. Whoever controls the pack controls what can leave the sandbox. Agents are untrusted, and
  the repository they work on is influenced by them (their branches get merged).
- The sandbox depends on parts of the image (git, CA certificates, glibc for the mounted `claude`
  binary, uid 1000). An image that lacks them breaks workers in confusing ways.
- Secrets must not show up in argv, `podman inspect`, logs or the database (ADR 0015, 0016).
- Invalid configuration must fail loudly, with messages a user can act on.

## Options considered

**Where packs come from**

1. **A file in the repository** (`.agent-office.yaml`). Pros: versioned with the code. Cons: an
   agent can propose a change to it in a PR; once merged, the next run's agents get the hosts and
   secrets the agent asked for. A human merge would silently become a sandbox change.
2. **Built into the binary plus a user-owned config directory.** Pros: only the user (or a
   release of agent-office) can change them; the same ownership checks as the token files.
   Cons: not versioned with the project; sharing a pack means copying a directory.

**How a pack is chosen**

1. **Auto-detection** from repository files (`go.mod` → go). Cons: a file appearing in the repo
   changes what agents can reach.
2. **Explicit `--pack <name>`**, one per daemon run (one project). Pros: nothing changes unless
   the user says so. A hint is logged when a built-in pack matches the repository.

**What the toolchain is**

1. **An image reference** (`docker.io/library/golang@sha256:…`). Cons: needs pulling; may lack
   git, certificates or glibc; its contents are not visible in the pack.
2. **A Containerfile in the pack, built `FROM ${BASE_IMAGE}`** (agent-office's base image).
   Pros: the sandbox's requirements always hold; the toolchain is reviewable text; the tag is a
   hash of every build input, so a change forces a rebuild. Cons: build time once per change.

**Where secret values come from and how they enter the worker**

1. **Daemon environment, `podman run -e`.** Cons: shell profiles and history; visible in
   `podman inspect` and the podman process's argv (Spike E).
2. **One user-only file per name, delivered over a one-shot per-agent socket to `sandbox-init`**
   (ADR 0015's option 3). Pros: the existing token-file checks; no copy outside the file; nothing
   in the container's configuration; rotation by replacing the file. Cons: one more socket.

**Default allowlist when a pack is set**

1. **Pack hosts added to the default.** Cons: every project reaches the Go and npm registries.
2. **Pack hosts replace the default.** Pros: least privilege; the default registries existed so
   agents could build, which is now the pack's job. Projects without a pack keep the default.

## Decision

Option 2 in each group.

- **Format** (`schema: 1`): a directory `<name>/pack.yaml` with `name`, `description`,
  `toolchain.containerfile`, `egress.allow`, `env` (names, descriptions, `required`), `skills`
  (directories under `skills/`) and `mcp_servers` (`command` + `args` for stdio inside the
  worker, or an `https` `url` whose host is allowed). Parsed with `go.yaml.in/yaml/v3` in strict
  mode (unknown fields are errors), at most 64 KiB, one document. Every problem is reported at
  once as `<file>: <field>: <problem>; <fix>`. Reference: `docs/packs.md`.
- **Sources:** built-ins embedded from `internal/pack/builtin` (`go`, `node`); user packs in
  `$XDG_CONFIG_HOME/agent-office/packs/<name>/` (`AGENT_OFFICE_PACKS_DIR`). Every file and
  directory of a user pack must be owned by the user, not writable by group or others and not a
  symlink. A user pack may not reuse a built-in's name.
- **Selection:** `--pack` (`AGENT_OFFICE_PACK`); empty means no pack. `--pack` with
  `--sandbox host` is refused: nothing in a pack means anything for an unsandboxed agent.
- **Images:** the Phase 3 Containerfile, minus Go, is the **base image**
  (`localhost/agent-office-worker:<hash>`) and the **default image**. A pack image is
  `localhost/agent-office-pack-<name>:<hash>` over the base Containerfile and every file in the
  pack directory except `pack.yaml` and `skills/`. The pack's Containerfile must start its final
  stage with `FROM ${BASE_IMAGE}`. `agent-office worker-image [--pack <name>]` builds them; the
  daemon never builds on its own. The go pack adds gcc and libc6-dev with `CGO_ENABLED=1`, so
  `go test -race` works in workers; the node pack installs Debian's `nodejs` and `npm`.
- **Egress:** allowlist = pack hosts (or the Phase 3 default without a pack) plus
  `AGENT_OFFICE_EGRESS_ALLOW`. Run-time grants (ADR 0019) are unchanged.
- **Secrets:** the value of `NAME` is `$XDG_CONFIG_HOME/agent-office/secrets/NAME`
  (`AGENT_OFFICE_SECRETS_DIR`), read with `secret.ReadFile` at every launch. Names agent-office or
  the sandbox depend on are reserved (its own variables, `CLAUDE_*`, `ANTHROPIC_*`, proxies,
  `GIT_*`, `SSH_*`, `GH_TOKEN`/`GITHUB_TOKEN` (ADR 0020: agents never hold a GitHub token),
  `HOME`, `PATH`, `LD_*`, ...). The daemon serves the values on the agent's `secrets.sock`
  exactly once, then closes the listener; `sandbox-init` reads them before starting the agent CLI
  and sets them in its environment only. If the socket is missing or the answer is malformed,
  `sandbox-init` fails and the agent does not start. Workers and reviewers get secrets; leads do
  not. A missing required secret fails startup and launch with the command that creates the
  file; a missing optional one is skipped and shown as a warning. Logs and the UI show names
  only.
- **Skills** are copied into the agent's `CLAUDE_CONFIG_DIR/skills/<name>` (Claude Code reads
  personal skills there when `CLAUDE_CONFIG_DIR` is set: code.claude.com/docs/en/claude-directory).
  A `SKILL.md` needs a `description` in its frontmatter.
- **MCP servers** are added to the agent's generated `--mcp-config` next to `agent-office` (a
  reserved name, as are Claude Code's built-in server names); `--strict-mcp-config` stays.
- **Status:** `GET /api/status` shows the pack, its image, hosts, secrets (names and whether the
  file exists), skills and MCP servers.

## Consequences

- A project's sandbox is described by one reviewable directory; what an agent can reach and hold
  is visible in the UI.
- Changing a pack takes effect at the next daemon start (packs are loaded once); secrets are
  re-read at each launch, so rotation needs no restart.
- The default image no longer has Go: Go projects need `--pack go` (logged as a hint).
- A secret given to an agent can be exfiltrated by it through any allowed host; TLS hides the
  contents from the proxy. That is the price of giving the agent the secret, stated in the threat
  model and in `docs/packs.md`. Packs should give agents only test or read-only credentials.
- Pack MCP servers over HTTP see whatever the agent sends them; they are trusted like any
  allowed host.
- Sharing packs between machines is copying a directory; a pack registry, composition and
  repository-suggested packs (shown, never applied without the user) are possible later.
