package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The daemon as a systemd user service (ADR 0060): `service install` writes a
// unit that runs `serve` with the given flags, restarts it on failure and
// logs to the journal.

const serviceUnit = "agent-office.service"

// systemctl runs `systemctl --user` (and loginctl for status); a fake in tests.
type systemctl interface {
	run(ctx context.Context, args ...string) (string, error)
	linger(ctx context.Context) (string, error) // loginctl's Linger=yes|no for this user
}

type realSystemctl struct{}

func (realSystemctl) run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil && s != "" {
		return s, fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), s)
	}
	return s, err
}

func (realSystemctl) linger(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", fmt.Sprint(os.Getuid()), "--property=Linger",
		"--value").Output()
	return strings.TrimSpace(string(out)), err
}

// serviceEnv is what the command reads from its surroundings, so tests can
// set it.
type serviceEnv struct {
	ctl       systemctl
	unitDir   string            // the systemd user unit directory
	binary    string            // this binary, absolute and resolved
	path      string            // PATH for the unit
	cwd       string            // relative path flags are made absolute against it
	env       []string          // the environment, for the AGENT_OFFICE_* warning
	daemonUp  func(string) bool // whether a daemon answers on a data dir's control socket
	runtime   func() (string, error)
	userUnits bool // false: no systemd user manager here
}

func defaultServiceEnv() (serviceEnv, error) {
	bin, err := os.Executable()
	if err != nil {
		return serviceEnv{}, err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return serviceEnv{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return serviceEnv{}, err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return serviceEnv{}, err
		}
		cfg = filepath.Join(home, ".config")
	}
	_, lookErr := exec.LookPath("systemctl")
	return serviceEnv{ctl: realSystemctl{}, unitDir: filepath.Join(cfg, "systemd", "user"), binary: bin,
		path: os.Getenv("PATH"), cwd: cwd, env: os.Environ(), runtime: runtimeDir, userUnits: lookErr == nil,
		daemonUp: func(sock string) bool {
			c, err := net.DialTimeout("unix", sock, time.Second)
			if err != nil {
				return false
			}
			c.Close()
			return true
		}}, nil
}

func serviceCommand(args []string, stdout, stderr io.Writer) int {
	env, err := defaultServiceEnv()
	if err != nil {
		fmt.Fprintln(stderr, "agent-office service:", err)
		return 1
	}
	return runService(context.Background(), env, args, stdout, stderr)
}

func serviceUsage(w io.Writer) {
	fmt.Fprint(w, `usage: agent-office service <command>

  install [--no-start] [serve flags]
            write a systemd user unit that runs "agent-office serve" with these flags, enable it and
            (re)start it; it restarts on failure and logs to the journal. Run it again to change the flags.
  uninstall stop and disable the unit and remove it
  status    show the unit, whether it runs, and how to read its logs
`)
}

func runService(ctx context.Context, env serviceEnv, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		serviceUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "install":
		err = serviceInstall(ctx, env, args[1:], stdout, stderr)
	case "uninstall":
		err = serviceUninstall(ctx, env, stdout)
	case "status":
		err = serviceStatus(ctx, env, stdout)
	case "-h", "--help", "help":
		serviceUsage(stdout)
		return 0
	default:
		serviceUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "agent-office service:", err)
		return 1
	}
	return 0
}

// pathFlags are serve flags that name files or directories: made absolute,
// since the unit does not run in the shell's directory.
var pathFlags = map[string]bool{"repo": true, "data-dir": true, "packs-dir": true, "secrets-dir": true,
	"roles-dir": true, "prices": true, "codex-auth": true}

// binFlags name a program: a bare name is looked up in the unit's PATH, a
// path is made absolute.
var binFlags = map[string]bool{"claude-bin": true, "codex-bin": true}

// serveArgs validates the serve flags with serve's own parser and returns
// them as the unit passes them: --name=value, by name, paths
// absolute. It also returns the data dir the daemon will use.
func serveArgs(args []string, cwd string) ([]string, string, error) {
	var out []string
	var dataDir string
	type kv struct{ name, value string }
	var given []kv
	// The values as given: parsing goes on to make some of them absolute
	// against this process's directory.
	cfg, err := parseServeWith(args, func(fs *flag.FlagSet) {
		fs.Visit(func(f *flag.Flag) { given = append(given, kv{f.Name, f.Value.String()}) })
	})
	if err != nil {
		return nil, "", fmt.Errorf("serve flags: %w", err)
	}
	dataDir = cfg.dataDir
	if slices.ContainsFunc(given, func(f kv) bool { return f.name == "dev" }) {
		return nil, "", errors.New("--dev is for development with the Vite server; the service serves the embedded UI")
	}
	for _, f := range given { // in name order (flag.Visit)
		v := f.value
		if (pathFlags[f.name] || (binFlags[f.name] && strings.Contains(v, "/"))) && v != "" && !filepath.IsAbs(v) {
			v = filepath.Join(cwd, v)
		}
		out = append(out, "--"+f.name+"="+v)
	}
	return out, dataDir, nil
}

// systemdQuote quotes one word for a unit file: double quotes, backslash
// escapes, and % and $ doubled so systemd expands neither specifiers nor
// variables.
func systemdQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", fmt.Errorf("a value with a line break cannot go into a unit file: %q", s)
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`, nil
}

// unitFile renders the unit.
func unitFile(binary string, args []string, path string) (string, error) {
	words := append([]string{binary, "serve"}, args...)
	quoted := make([]string, len(words))
	for i, w := range words {
		q, err := systemdQuote(w)
		if err != nil {
			return "", err
		}
		quoted[i] = q
	}
	if strings.ContainsAny(path, "\n\r\x00") {
		return "", fmt.Errorf("PATH has a line break: %q", path)
	}
	// Environment= expands specifiers (%), not variables ($): only % is doubled.
	p := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace("PATH="+absolutePath(path)) + `"`
	var b strings.Builder
	b.WriteString(`# Written by "agent-office service install"; run it again to change the flags (ADR 0060).
[Unit]
Description=agent-office daemon (a team of AI coding agents)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
`)
	fmt.Fprintf(&b, "Environment=%s\n", p)
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(quoted, " "))
	b.WriteString(`Restart=on-failure
RestartSec=5s
# SIGTERM goes to the daemon only: it stops its agents and records them for --restore-agents.
KillMode=mixed
TimeoutStopSec=60s
SyslogIdentifier=agent-office

[Install]
WantedBy=default.target
`)
	return b.String(), nil
}

func serviceInstall(ctx context.Context, env serviceEnv, args []string, stdout, stderr io.Writer) error {
	noStart := false
	var rest []string
	for _, a := range args {
		if a == "--no-start" || a == "-no-start" {
			noStart = true
			continue
		}
		rest = append(rest, a)
	}
	if !env.userUnits {
		return errors.New("systemctl was not found: the service needs systemd's user manager")
	}
	if tmp := os.TempDir(); strings.HasPrefix(env.binary, filepath.Clean(tmp)+string(filepath.Separator)) {
		return fmt.Errorf("%s is a temporary binary (go run?): build it (make build) and install from bin/agent-office", env.binary)
	}
	sargs, dataDir, err := serveArgs(rest, env.cwd)
	if err != nil {
		return err
	}
	unit, err := unitFile(env.binary, sargs, env.path)
	if err != nil {
		return err
	}
	unitPath := filepath.Join(env.unitDir, serviceUnit)
	active := false
	if out, err := env.ctl.run(ctx, "is-active", serviceUnit); err == nil && out == "active" {
		active = true
	}
	if !active && env.runtime != nil && env.daemonUp != nil {
		if rt, err := env.runtime(); err == nil && env.daemonUp(controlSocketPath(rt, dataDir)) {
			return fmt.Errorf("a daemon already runs on %s outside the service: stop it first (Ctrl-C in its terminal)",
				dataDir)
		}
	}
	if err := os.MkdirAll(env.unitDir, 0o755); err != nil {
		return err
	}
	old, _ := os.ReadFile(unitPath)
	tmp := unitPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, unitPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", unitPath)
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			fmt.Fprintln(stdout, "  "+line)
		}
	}
	if names := agentOfficeEnv(env.env); len(names) > 0 {
		fmt.Fprintf(stderr, "note: %s %s set in this shell but not passed to the service; pass them as flags "+
			"(e.g. %s) if the service needs them\n", strings.Join(names, ", "), plural(len(names), "is", "are"),
			flagOfEnv(names[0]))
	}
	if _, err := env.ctl.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if _, err := env.ctl.run(ctx, "enable", serviceUnit); err != nil {
		return err
	}
	switch {
	case noStart:
		fmt.Fprintln(stdout, "enabled; not started (--no-start): systemctl --user start agent-office")
	case active && !bytes.Equal(old, []byte(unit)):
		if _, err := env.ctl.run(ctx, "restart", serviceUnit); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "restarted with the new flags")
	case active:
		fmt.Fprintln(stdout, "already running with these flags")
	default:
		if _, err := env.ctl.run(ctx, "start", serviceUnit); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "started")
	}
	fmt.Fprintln(stdout, "logs: journalctl --user -u agent-office -f")
	return nil
}

func serviceUninstall(ctx context.Context, env serviceEnv, stdout io.Writer) error {
	unitPath := filepath.Join(env.unitDir, serviceUnit)
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "not installed (%s does not exist)\n", unitPath)
		return nil
	}
	if !env.userUnits {
		return errors.New("systemctl was not found: the service needs systemd's user manager")
	}
	if _, err := env.ctl.run(ctx, "disable", "--now", serviceUnit); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	if _, err := env.ctl.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "stopped, disabled and removed %s; the data dir is kept\n", unitPath)
	return nil
}

func serviceStatus(ctx context.Context, env serviceEnv, stdout io.Writer) error {
	unitPath := filepath.Join(env.unitDir, serviceUnit)
	b, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "not installed: agent-office service install [serve flags]\n")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "unit:     %s\n", unitPath)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			fmt.Fprintf(stdout, "command:  %s\n", strings.TrimPrefix(line, "ExecStart="))
		}
	}
	if env.userUnits {
		enabled, _ := env.ctl.run(ctx, "is-enabled", serviceUnit)
		active, _ := env.ctl.run(ctx, "is-active", serviceUnit)
		fmt.Fprintf(stdout, "enabled:  %s\nactive:   %s\n", orUnknown(enabled), orUnknown(active))
		if l, err := env.ctl.linger(ctx); err == nil {
			if l == "yes" {
				fmt.Fprintln(stdout, "linger:   yes (it runs while you are logged out)")
			} else {
				fmt.Fprintln(stdout, "linger:   no (it stops when you log out; loginctl enable-linger keeps it running)")
			}
		}
	}
	fmt.Fprintln(stdout, "logs:     journalctl --user -u agent-office -f")
	return nil
}

// absolutePath keeps PATH's absolute entries: an empty or relative one would
// resolve against the service's working directory (security review).
func absolutePath(path string) string {
	var keep []string
	for _, d := range strings.Split(path, ":") {
		if filepath.IsAbs(d) && !slices.Contains(keep, d) {
			keep = append(keep, d)
		}
	}
	return strings.Join(keep, ":")
}

// agentOfficeEnv lists the AGENT_OFFICE_* variables that are set (names only).
func agentOfficeEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "AGENT_OFFICE_") && v != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// flagOfEnv is the flag an AGENT_OFFICE_* variable stands for, as a hint.
func flagOfEnv(name string) string {
	return "--" + strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, "AGENT_OFFICE_")), "_", "-")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
