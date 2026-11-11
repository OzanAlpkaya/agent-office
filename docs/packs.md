# Stack packs

A **stack pack** configures a project's sandboxed agents (ADR 0023): the worker image (toolchain)
they run in, the hosts they may reach, the secrets they get, the Claude Code skills they can use
and extra MCP servers. One pack per daemon run:

```sh
agent-office worker-image --pack go     # once, and after the pack changes (needs network)
agent-office serve --repo ~/src/myproject --pack go
```

Without `--pack`, agents get the **base image** (Debian with git, curl, ripgrep, python3 and CA
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

resources:                        # optional
  memory: 4g                      # each worker's memory limit, 256m to 256g (default: --sandbox-memory, 2g);
                                  # a project's worker_memory setting wins over it (ADR 0051)
  pids: 1024                      # each worker's process and thread limit, 64 to 65536 (default:
                                  # --sandbox-pids, 256); a project's worker_pids wins over it (Phase 17)

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
`BASE_IMAGE`. The final stage must start with `FROM ${BASE_IMAGE}` (workers rely on its git,
certificates and glibc) and must not set an `ENTRYPOINT` (agent-office starts its own init).
Earlier build stages may use any image.

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-client \
 && rm -rf /var/lib/apt/lists/*
```

Every file in the pack directory except `pack.yaml`, `skills/` and `.git/` is the build context
(exec bits are kept), and the image tag (`localhost/agent-office-pack-<name>:<hash>`) is a hash of all of them plus the base
Containerfile. Any change means a new tag; the daemon refuses to start until it is built with
`agent-office worker-image --pack <name>` (or `make worker-image PACK=<name>`). The daemon never
builds images by itself. Workers run as uid 1000 with a read-only root filesystem and `HOME` on a
temporary filesystem (`/tmp/home`), so caches belong under `$HOME`.

### Egress

With a pack, the allowlist is the pack's `egress.allow` (the default registries are not added),
plus `AGENT_OFFICE_EGRESS_ALLOW`. Names are lower case, without scheme, port, IP address or `*`.
Access requests (ADR 0019, 0053) grant extra hosts after a decision: yours, or the Tech Lead's when the
project's `access_decider` lets it. An approval covers the asking agent (in its later sessions too) or
the whole project, and is stored, so it holds after a resume or a restart.

### Resources

`resources.memory` sets each worker's memory limit (`512m`, `4g`; 256m to 256g). When a worker
reaches it, the kernel's out-of-memory killer ends a process in it: if that is the agent, the agent's
card and its Tech Lead say "Out of memory" with the limit; if it is a command the agent ran, the agent
is told why its command was killed (ADR 0051). Toolchains that compile in parallel (Go, Rust) need
more than the 2g default for larger projects.

`resources.pids` sets each worker's process and thread limit (64 to 65536; default `--sandbox-pids`,
256). Test suites that start databases, browsers or many parallel packages reach 256 quickly (PulseGo's
integration suite did; one headless Chromium alone uses about 130). A process start refused at the
limit shows as "Process limit reached" on the agent's card, and the agent is told the limit and where to
raise it. A project's `worker_pids` setting wins over the pack's value, as `worker_memory` does.

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
- **Only workers** get the secrets: leads plan and run no code, and reviewers read the
  agent-written diff, the most injectable input there is. A reviewer's tests that need a secret
  fail; it can still review the code.
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
if it has a `name`, the same name). The files are read and checked when the pack loads, written for
each launch and mounted read-only at the agent's `$CLAUDE_CONFIG_DIR/skills/<name>`, where Claude
Code finds personal skills; the agent cannot change them. At most 1 MiB and 200 files per skill.

### MCP servers

Added to the agent's MCP config next to agent-office's team server (`agent-office` and Claude
Code's built-in names are reserved). Exactly one of:

- `command` (+ `args`): a stdio server started inside the worker; an absolute path in the image.
  Whether Claude Code passes it the agent's full environment (the pack's secrets, the proxy
  variables) is not documented and not yet checked live.
- `url`: an `https://` URL on port 443 whose host is in the pack's `egress.allow`. No credentials
  in the URL; headers and OAuth are not supported yet. HTTP servers through the egress proxy are
  covered by unit tests only, not yet by a live run.

## Built-in packs

| Pack | Toolchain | Hosts | Skill |
|---|---|---|---|
| `go` | Go 1.27.3 (checksum-verified) with gcc, libc headers and make, `CGO_ENABLED=1`, so `go test -race` works | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` (the proxy serves some module zips from there) | `go-sandbox` |
| `node` | Debian's `nodejs` and `npm` | `registry.npmjs.org` | `node-sandbox` |

The images are about 650 MB (go) and 450 MB (node); the base image is about 190 MB.
