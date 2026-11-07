# Stack packs

A **stack pack** configures a project's sandboxed agents (ADR 0023): the worker image (toolchain)
they run in, the hosts they may reach, the secrets they get, the Claude Code skills they can use
and extra MCP servers. One pack per daemon run:

```sh
agent-office worker-image --pack go     # once, and after the pack changes (needs network)
agent-office serve --repo ~/src/myproject --pack go
```

Without `--pack`, agents get the **base image** (Debian with git, curl, ripgrep and CA
certificates, no language toolchain) and the default allowlist (`proxy.golang.org`,
`sum.golang.org`, `registry.npmjs.org`). Packs need sandbox mode; `--pack` with `--sandbox host` is
refused.

## Where packs come from

| Source | Location | Notes |
|---|---|---|
| Built in | the binary (`internal/pack/builtin`) | `go`, `node` |
| Yours | `~/.config/agent-office/packs/<name>/` (`AGENT_OFFICE_PACKS_DIR`, `--packs-dir`) | must belong to you; nothing group- or world-writable; no symlinks |

Packs are **never read from the repository** the agents work on: a pack decides what can leave
the sandbox, and agents influence the repository through the branches you merge. A user pack may
not reuse a built-in name; to change a built-in, copy its directory under a new name.

```sh
agent-office pack list            # built-in and your packs, their images, problems
agent-office pack check <dir>     # validate a pack directory and print every problem
```

## Format

A pack is a directory named like the pack, with a `pack.yaml`:

```yaml
schema: 1                         # required; the format version
name: myapp                       # required; equals the directory name; [a-z][a-z0-9-]{0,31}
description: Go and Postgres client tools for myapp.   # required; one line, <= 300 characters

toolchain:                        # optional; without it, agents get the base image
  containerfile: Containerfile    # path inside the pack directory

egress:
  allow:                          # hosts agents may reach over HTTPS (port 443)
    - proxy.golang.org            # an exact name
    - .example.com                # a leading dot: every subdomain (not example.com itself)

env:                              # secrets, by name only; values live in files (below)
  - name: TEST_DATABASE_URL
    description: Postgres for integration tests (a throwaway database)
    required: true                # missing file: the daemon does not start
  - name: NPM_READ_TOKEN          # optional (the default): left out, with a warning

skills:                           # directories under skills/, each with a SKILL.md
  - myapp-conventions

mcp_servers:
  docs:                           # an HTTP server, reached through the egress proxy
    url: https://docs.example.com/mcp
  db:                             # a stdio server; the command must exist in the image
    command: /usr/local/bin/db-mcp
    args: [--read-only]
```

Decoding is strict: an unknown or misspelled field is an error. Every problem is reported at once,
as `<file>: <field>: <problem>; <fix>`, for example:

```
~/.config/agent-office/packs/rust/pack.yaml: invalid stack pack (3 problems, see docs/packs.md):
  - toolchain.containerfile: the last stage must start with `FROM ${BASE_IMAGE}`, not "FROM docker.io/library/rust:1": ...
  - egress.allow[0]: "*.crates.io" is not a host name; use a lower-case name such as example.com, or .example.com for its subdomains ...
  - env[0].name: GITHUB_TOKEN is reserved (...); choose another name
```

### Toolchain

The Containerfile builds **on top of agent-office's base image**, whose tag the daemon passes in
`BASE_IMAGE`. The final stage must start with `FROM ${BASE_IMAGE}`: workers rely on its git,
certificates and glibc. Earlier build stages may use any image.

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-client \
 && rm -rf /var/lib/apt/lists/*
```

Every file in the pack directory except `pack.yaml` and `skills/` is the build context, and the
image tag (`localhost/agent-office-pack-<name>:<hash>`) is a hash of all of them plus the base
Containerfile. Any change means a new tag; the daemon refuses to start until it is built with
`agent-office worker-image --pack <name>` (or `make worker-image PACK=<name>`). The daemon never
builds images by itself. Workers run as uid 1000 with a read-only root filesystem and `HOME` on a
temporary filesystem (`/tmp/home`), so caches belong under `$HOME`.

### Egress

With a pack, the allowlist is the pack's `egress.allow` (the default registries are not added),
plus `AGENT_OFFICE_EGRESS_ALLOW`. Names are lower case, without scheme, port, IP address or `*`.
Access requests (ADR 0019) still grant extra hosts to one running agent, after your approval.

### Secrets

The value of `NAME` is the file `~/.config/agent-office/secrets/NAME` (`AGENT_OFFICE_SECRETS_DIR`,
`--secrets-dir`), checked like the token files: a regular file you own, mode 0600 or 0400, in a
directory only you can write, one line of printable ASCII, at most 4 KiB. Create one without the
value touching your shell history:

```sh
install -d -m 0700 ~/.config/agent-office/secrets
(umask 077; cat > ~/.config/agent-office/secrets/TEST_DATABASE_URL)   # paste, Enter, Ctrl-D
```

- Files are read at every agent launch, so replacing one takes effect for the next agent.
- **Workers and reviewers** get the secrets; **leads do not** (they plan and do not run code).
- The daemon serves the values once, over a per-agent socket, to the container's init, which puts
  them into the agent's environment only: not into argv, `podman inspect`, files, logs or the
  database. The UI shows names and whether each file exists, never values.
- Names that agent-office, the sandbox or Claude Code depend on are reserved (its own
  `AGENT_OFFICE_*`, `CLAUDE*`, `ANTHROPIC_*`, proxy variables, `GIT_*`, `SSH_*`, `LD_*`, `BUN_*`,
  `HOME`, `PATH`, `NODE_OPTIONS`, ...), and so are `GH_TOKEN` / `GITHUB_TOKEN`: agents never hold
  a GitHub token (ADR 0020).

**An agent can leak any secret it holds** through an allowed host (TLS hides the contents from the
proxy). Give agents test, throwaway or read-only credentials only.

### Skills

Each entry names `skills/<name>/` with a `SKILL.md` whose frontmatter has a `description` (and,
if it has a `name`, the same name). The directory is copied into the agent's own Claude Code
config directory (`$CLAUDE_CONFIG_DIR/skills/<name>`), where Claude Code finds personal skills.
At most 1 MiB and 200 files per skill.

### MCP servers

Added to the agent's MCP config next to agent-office's team server (`agent-office` and Claude
Code's built-in names are reserved). Exactly one of:

- `command` (+ `args`): a stdio server started inside the worker; an absolute path in the image.
- `url`: an `https://` URL on port 443 whose host is in the pack's `egress.allow`. No credentials
  in the URL; headers and OAuth are not supported yet. HTTP servers through the egress proxy are
  covered by unit tests only, not yet by a live run.

## Built-in packs

| Pack | Toolchain | Hosts | Skill |
|---|---|---|---|
| `go` | Go 1.27.3 (checksum-verified) with gcc and libc headers, `CGO_ENABLED=1`, so `go test -race` works | `proxy.golang.org`, `sum.golang.org` | `go-sandbox` |
| `node` | Debian's `nodejs` and `npm` | `registry.npmjs.org` | `node-sandbox` |

The images are about 650 MB (go) and 450 MB (node); the base image is about 190 MB.
