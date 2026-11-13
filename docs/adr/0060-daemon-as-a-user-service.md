# 0060. The daemon as a systemd user service

- **Status:** Accepted
- **Date:** 2026-11-12

## Context

Running Bouncibo's goals meant keeping a terminal open for `agent-office serve` for hours. When the daemon
died or the terminal closed, the team stopped. agent-office is Linux-first (CachyOS) and runs as the user,
with rootless Podman. A service must not need sudo, and it must start the daemon the way the user's
shell does: the same `claude`, `podman` and `git`, and the same flags.

## Options considered

1. **A systemd user unit written by `agent-office service install`**: no sudo; restart on failure and the
   journal come for free; `systemctl --user` is the standard. The user manager does not inherit the shell's
   environment, so the unit must carry what serve needs.
2. **Document a hand-written unit**: no code, but every user gets `PATH`, quoting, paths and the kill
   mode wrong in their own way.
3. **A self-daemonizing `serve --background` with a pid file**: works without systemd, but restarts,
   logs and boot start would all be reinvented.

## Decision

Option 1. `service install [--no-start] [serve flags]`:

- parses the flags with serve's own parser, so an invalid flag fails before anything is written; `--dev`
  and stray arguments are refused (serve itself now refuses stray arguments too);
- writes `$XDG_CONFIG_HOME/systemd/user/agent-office.service` (default `~/.config/...`) atomically:
  `ExecStart` is this binary's absolute, symlink-resolved path, `serve` and the given flags as
  `--name=value` in name order, with relative paths made absolute and every word quoted for systemd
  (`%` and `$` doubled, so systemd expands neither); `Environment=PATH=` is the installing shell's `PATH`;
- sets `Restart=on-failure`, `RestartSec=5s`, `KillMode=mixed` (SIGTERM to the daemon only: it stops its
  agents, which `--restore-agents` resumes after a restart, and anything left is killed after
  `TimeoutStopSec=60s`), `SyslogIdentifier=agent-office` and `WantedBy=default.target`;
- does not copy `AGENT_OFFICE_*` variables from the shell: it names those that are set (never their values)
  and says to pass them as flags. The unit says exactly what the service runs;
- refuses while a daemon already answers on the data dir's control socket outside the unit;
- runs `daemon-reload`, `enable`, then `start`, or `restart` when the unit changed while it ran.

`service uninstall` runs `disable --now`, removes the unit and reloads; the data dir stays. `service
status` prints the unit, its command, `is-enabled`, `is-active`, lingering (with `loginctl
enable-linger` as the way to keep it running while logged out) and the `journalctl` command.

## Consequences

- Nobody keeps a terminal open; a crash restarts the daemon, and agents interrupted by the restart are
  resumed with `--restore-agents` (ADR 0035).
- The service uses the real data dir, as serve does. Tests write units to a temp directory with a fake
  `systemctl`, and run `systemd-analyze --user verify` on a rendered unit where it exists.
- Changing a flag means running `install` again. A secret never goes into the unit: tokens stay in their
  files (ADR 0015, 0020).
- Without lingering the user manager stops the service at logout; `status` says so rather than enabling
  lingering itself.
