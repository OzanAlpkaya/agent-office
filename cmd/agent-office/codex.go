package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// codexInstall is the Codex CLI that sandboxed Codex agents run (ADR 0049):
// the directory with the static binaries the npm package ships.
type codexInstall struct {
	dir, version string
}

// codexTriples are the npm package's native builds for Linux.
var codexTriples = map[string][2]string{
	"amd64": {"@openai/codex-linux-x64", "x86_64-unknown-linux-musl"},
	"arm64": {"@openai/codex-linux-arm64", "aarch64-unknown-linux-musl"},
}

// resolveCodex finds the native Codex binaries for bin: the static `codex`
// itself, or the npm launcher (bin/codex.js) whose platform package holds it.
// Both binaries must be static, since they are mounted into workers whose
// libc may differ.
func resolveCodex(bin string) (codexInstall, error) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return codexInstall{}, err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return codexInstall{}, err
	}
	native := p
	if strings.HasSuffix(p, ".js") {
		t, ok := codexTriples[runtime.GOARCH]
		if !ok {
			return codexInstall{}, fmt.Errorf("no Codex build for %s", runtime.GOARCH)
		}
		root := filepath.Dir(filepath.Dir(p)) // .../@openai/codex
		native = filepath.Join(root, "node_modules", t[0], "vendor", t[1], "bin", "codex")
	}
	dir := filepath.Dir(native)
	for _, name := range []string{"codex", "codex-code-mode-host"} {
		ok, err := isStatic(filepath.Join(dir, name))
		if err != nil {
			return codexInstall{}, fmt.Errorf("Codex binary %s: %w", filepath.Join(dir, name), err)
		}
		if !ok {
			return codexInstall{}, fmt.Errorf("Codex binary %s is not statically linked", filepath.Join(dir, name))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(dir, "codex"), "--version").Output()
	if err != nil {
		return codexInstall{}, fmt.Errorf("`codex --version`: %w", err)
	}
	v, err := agent.ParseCodexVersion(string(out))
	if err != nil {
		return codexInstall{}, err
	}
	return codexInstall{dir: dir, version: v}, nil
}

// codexOptions sets up Codex agents for a sandboxed daemon. Without a Codex
// CLI it returns nil and Codex roles are refused with the fix; an explicit
// --codex-bin that does not work, or Codex roles in the daemon's defaults,
// make it an error. The login is checked now (and read again at every
// request), and a warning names an access token about to expire.
func codexOptions(cfg serveConfig, needed bool, log *slog.Logger) (*engine.CodexOptions, []string, error) {
	ci, err := resolveCodex(cfg.codexBin)
	if err != nil {
		if needed || cfg.codexBinSet {
			return nil, nil, fmt.Errorf("Codex CLI (--codex-bin %s): %w; install it (npm install -g @openai/codex) or run "+
				"those roles on claude", cfg.codexBin, err)
		}
		log.Info("Codex agents are not available", "reason", err)
		return nil, nil, nil
	}
	login, loginErr := sandbox.ReadCodexLogin(cfg.codexAuth)
	if loginErr != nil {
		if needed {
			return nil, nil, loginErr
		}
		log.Info("Codex agents are not available until you sign in", "reason", loginErr)
	}
	var warnings []string
	if ci.version != agent.TestedCodexVersion {
		warnings = append(warnings, fmt.Sprintf("Codex CLI %s is installed, but agent-office's Codex config was tested with %s. "+
			"If Codex agents stay in \"starting\", look at their terminal.", ci.version, agent.TestedCodexVersion))
	}
	if loginErr == nil && !login.Expires.IsZero() && time.Until(login.Expires) < 48*time.Hour {
		warnings = append(warnings, fmt.Sprintf("Your Codex login expires %s; run codex once (or `codex login`) to refresh it, "+
			"agent-office never refreshes it itself.", login.Expires.Local().Format("2006-01-02 15:04")))
	}
	ca, err := sandbox.NewLocalCA()
	if err != nil {
		return nil, nil, err
	}
	log.Info("Codex ready", "codex", ci.dir, "version", ci.version, "login_file", cfg.codexAuth, "login_ok", loginErr == nil)
	return &engine.CodexOptions{Dir: ci.dir, Version: ci.version, CA: ca, Login: loginReader(cfg.codexAuth)}, warnings, nil
}

// loginReader reads the login at every request. A read that fails while the
// login was readable a moment ago (the user's own Codex rewriting the file
// during a refresh) uses the last good one for up to a minute: its token is
// still valid, and an expired one is refused upstream anyway.
func loginReader(path string) func() (sandbox.CodexLogin, error) {
	var mu sync.Mutex
	var last sandbox.CodexLogin
	var lastAt time.Time
	return func() (sandbox.CodexLogin, error) {
		l, err := sandbox.ReadCodexLogin(path)
		mu.Lock()
		defer mu.Unlock()
		if err == nil {
			last, lastAt = l, time.Now()
			return l, nil
		}
		if !lastAt.IsZero() && time.Since(lastAt) < time.Minute {
			return last, nil
		}
		return sandbox.CodexLogin{}, err
	}
}

// usesCodex reports whether the daemon's defaults run a role on Codex.
func usesCodex(clis map[string]string) bool {
	for _, c := range clis {
		if c == agent.CLICodex {
			return true
		}
	}
	return false
}
