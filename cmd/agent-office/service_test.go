package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeCtl struct {
	calls  [][]string
	active bool
}

func (f *fakeCtl) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "is-active":
		if f.active {
			return "active", nil
		}
		return "inactive", exec.ErrNotFound // any error: systemctl exits non-zero when inactive
	case "is-enabled":
		return "enabled", nil
	case "start", "restart":
		f.active = true
	case "disable":
		f.active = false
	}
	return "", nil
}

func (f *fakeCtl) linger(context.Context) (string, error) { return "no", nil }

func (f *fakeCtl) verbs() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func testServiceEnv(t *testing.T) (serviceEnv, *fakeCtl) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctl := &fakeCtl{}
	return serviceEnv{ctl: ctl, unitDir: filepath.Join(t.TempDir(), "systemd", "user"), binary: "/opt/ao/agent-office",
		path: "/usr/local/bin:.::/usr/bin:bin:/opt/$x", cwd: "/home/u/work", env: []string{"HOME=/home/u", "AGENT_OFFICE_MODEL=haiku"},
		userUnits: true, daemonUp: func(string) bool { return false },
		runtime: func() (string, error) { return t.TempDir(), nil }}, ctl
}

func TestServiceInstallWritesUnitAndStarts(t *testing.T) {
	env, ctl := testServiceEnv(t)
	var out, errOut bytes.Buffer
	code := runService(context.Background(), env, []string{"install", "--repo", "proj", "--max-agents", "6",
		"--model", "100%$HOME"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("install: %d %s", code, errOut.String())
	}
	b, err := os.ReadFile(filepath.Join(env.unitDir, serviceUnit))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	for _, want := range []string{
		`ExecStart="/opt/ao/agent-office" "serve" "--max-agents=6" "--model=100%%$$HOME" "--repo=/home/u/work/proj"`,
		`Environment="PATH=/usr/local/bin:/usr/bin:/opt/$x"`, // relative and empty entries dropped; $ kept as is
		"Restart=on-failure", "KillMode=mixed", "SyslogIdentifier=agent-office", "WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if got, want := ctl.verbs(), []string{"is-active agent-office.service", "daemon-reload", "enable agent-office.service",
		"start agent-office.service"}; !slices.Equal(got, want) {
		t.Errorf("systemctl calls %v, want %v", got, want)
	}
	if !strings.Contains(errOut.String(), "AGENT_OFFICE_MODEL") || strings.Contains(errOut.String(), "haiku") {
		t.Errorf("env note: %q (names the variable, never its value)", errOut.String())
	}
	if !strings.Contains(out.String(), "journalctl --user -u agent-office") {
		t.Errorf("no log hint: %s", out.String())
	}

	// Again with the same flags: nothing to restart; with other flags: restarted.
	ctl.calls = nil
	if code := runService(context.Background(), env, []string{"install", "--repo", "proj", "--max-agents", "6",
		"--model", "100%$HOME"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if slices.ContainsFunc(ctl.verbs(), func(s string) bool { return strings.HasPrefix(s, "restart") }) {
		t.Errorf("same unit restarted: %v", ctl.verbs())
	}
	ctl.calls = nil
	if code := runService(context.Background(), env, []string{"install", "--max-agents", "3"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if !slices.Contains(ctl.verbs(), "restart agent-office.service") {
		t.Errorf("changed unit not restarted: %v", ctl.verbs())
	}
}

func TestServiceInstallRefusals(t *testing.T) {
	env, ctl := testServiceEnv(t)
	for _, args := range [][]string{
		{"install", "--max-agents", "99"},
		{"install", "--dev"},
		{"install", "--addr", "0.0.0.0:7680"},
		{"install", "stray"},
		{"install", "--model", "a\nb"},
	} {
		var out, errOut bytes.Buffer
		if code := runService(context.Background(), env, args, &out, &errOut); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(env.unitDir, serviceUnit)); err == nil {
		t.Error("a refused install wrote the unit")
	}
	// A daemon that already runs on the data dir outside the unit.
	env.daemonUp = func(string) bool { return true }
	var out, errOut bytes.Buffer
	if code := runService(context.Background(), env, []string{"install"}, &out, &errOut); code == 0 ||
		!strings.Contains(errOut.String(), "already runs") {
		t.Errorf("install over a running daemon: %d %s", code, errOut.String())
	}
	env.daemonUp = func(string) bool { return false }
	tmpEnv := env
	tmpEnv.binary = filepath.Join(os.TempDir(), "go-build123", "exe", "agent-office")
	if code := runService(context.Background(), tmpEnv, []string{"install"}, &out, &errOut); code == 0 ||
		!strings.Contains(errOut.String(), "temporary binary") {
		t.Errorf("install of a temporary binary: %d %s", code, errOut.String())
	}
	env.userUnits = false
	if code := runService(context.Background(), env, []string{"install"}, &out, &errOut); code == 0 {
		t.Error("install without systemctl accepted")
	}
	_ = ctl
}

func TestServiceNoStartStatusUninstall(t *testing.T) {
	env, ctl := testServiceEnv(t)
	var out, errOut bytes.Buffer
	if code := runService(context.Background(), env, []string{"install", "--no-start"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if slices.Contains(ctl.verbs(), "start agent-office.service") {
		t.Errorf("--no-start started it: %v", ctl.verbs())
	}
	out.Reset()
	if code := runService(context.Background(), env, []string{"status"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	for _, want := range []string{"unit:", "command:", "enabled:  enabled", "active:   inactive", "linger:   no", "journalctl"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	ctl.calls = nil
	if code := runService(context.Background(), env, []string{"uninstall"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if got, want := ctl.verbs(), []string{"disable --now agent-office.service", "daemon-reload"}; !slices.Equal(got, want) {
		t.Errorf("uninstall calls %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(env.unitDir, serviceUnit)); err == nil {
		t.Error("unit still there")
	}
	out.Reset()
	if code := runService(context.Background(), env, []string{"status"}, &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "not installed") {
		t.Errorf("status after uninstall: %s", out.String())
	}
}

// The unit is valid for systemd itself, where systemd-analyze is installed.
func TestServiceUnitVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unit, err := unitFile(bin, []string{"--data-dir=" + t.TempDir(), "--model=50%$x"}, os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, serviceUnit)
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(analyze, "--user", "verify", p).CombinedOutput()
	if err != nil {
		// Without a user manager (CI containers) verify cannot run; anything
		// it says about the unit itself is a failure.
		if strings.Contains(string(out), serviceUnit) {
			t.Fatalf("systemd-analyze verify: %s", out)
		}
		t.Skipf("systemd-analyze verify did not run: %s", strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), serviceUnit) {
		t.Errorf("systemd-analyze verify warns: %s", out)
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{`a b`: `"a b"`, `x"y`: `"x\"y"`, `c:\d`: `"c:\\d"`, `5%`: `"5%%"`, `$P`: `"$$P"`} {
		if got, err := systemdQuote(in); err != nil || got != want {
			t.Errorf("systemdQuote(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
