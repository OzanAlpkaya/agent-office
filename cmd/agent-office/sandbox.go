package main

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// Sandbox modes for --sandbox.
const (
	modePodman = "podman"
	modeHost   = "host"
)

// claudeInfo is the installed Claude Code: the resolved, versioned binary
// (Claude Code updates itself by switching the symlink) and its version.
type claudeInfo struct {
	path, version string
}

func resolveClaude(bin string) (claudeInfo, error) {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return claudeInfo{}, fmt.Errorf("claude binary: %w", err)
	}
	v, err := agent.ParseVersion(claudeVersion(real))
	if err != nil {
		v = ""
	}
	return claudeInfo{path: real, version: v}, nil
}

// versionWarnings reports a Claude Code version the pre-seeded config keys
// were not tested with (ADR 0016).
func versionWarnings(ci claudeInfo, mode string) []string {
	if mode != modePodman {
		return nil
	}
	switch ci.version {
	case agent.TestedClaudeCodeVersion:
		return nil
	case "":
		return []string{"Could not read the Claude Code version; sandboxed agents may stop at a first-run dialog."}
	default:
		return []string{fmt.Sprintf("Claude Code %s is installed, but the pre-seeded config for sandboxed agents was "+
			"tested with %s. If agents stay in \"starting\", a first-run dialog may be waiting in the terminal.",
			ci.version, agent.TestedClaudeCodeVersion)}
	}
}

// sandboxOptions checks everything sandbox mode needs and fails loudly, with
// the fix, when something is missing. There is no silent fallback to host
// mode (ADR 0007).
func sandboxOptions(ctx context.Context, cfg serveConfig, self string, ci claudeInfo, log *slog.Logger) (*engine.SandboxOptions, error) {
	hint := "or run agents unsandboxed with --sandbox host (permission prompts stay on)"
	p := sandbox.Podman{}
	pv, err := p.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("sandbox mode needs rootless podman: %w; %s", err, hint)
	}
	if ok, err := isStatic(self); err != nil || !ok {
		return nil, fmt.Errorf("sandbox mode mounts this binary into workers, so it must be statically linked "+
			"(build it with `make build`): %s (%v)", self, err)
	}
	if ci.version == "" {
		return nil, fmt.Errorf("sandbox mode needs the Claude Code version for the worker config: `%s --version` failed", ci.path)
	}
	image := sandbox.WorkerImage()
	if ok, err := p.ImageExists(ctx, image); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("worker image %s not found: build it with `agent-office worker-image` (or `make worker-image`); %s", image, hint)
	}
	tokenFile, err := sandbox.DefaultTokenFile()
	if err != nil {
		return nil, err
	}
	if _, err := sandbox.ReadTokenFile(tokenFile); err != nil { // checked now, read again at every launch
		return nil, fmt.Errorf("%w\nsandboxed agents authenticate with a token from `claude setup-token`, stored in that "+
			"file (mode 0600, directory 0700; see docs/adr/0015-agent-credential.md); %s", err, hint)
	}
	allow, err := sandbox.ParseAllowlist(append(append([]string{}, sandbox.DefaultEgressAllow...), cfg.egressAllow...))
	if err != nil {
		return nil, fmt.Errorf("AGENT_OFFICE_EGRESS_ALLOW: %w", err)
	}
	hooksDir := filepath.Join(cfg.dataDir, "git-hooks")
	if err := sandbox.WriteGitHooks(hooksDir); err != nil {
		return nil, err
	}
	log.Info("sandbox ready", "podman", pv, "image", image, "egress_allow",
		strings.Join(append(append([]string{}, sandbox.DefaultEgressAllow...), cfg.egressAllow...), ","),
		"token_file", tokenFile, "memory", cfg.sandboxMemory, "pids", cfg.sandboxPids)
	sum := sha256.Sum256([]byte(cfg.dataDir))
	return &engine.SandboxOptions{
		Instance: hex.EncodeToString(sum[:6]), Podman: p, Image: image,
		WorkspaceRoot: filepath.Join(cfg.dataDir, "workspaces"), AgentsRoot: filepath.Join(cfg.dataDir, "agents"),
		GitHooks: hooksDir, AgentOffice: self, Claude: ci.path, ClaudeVersion: ci.version,
		TokenFile: tokenFile, Allow: allow, Memory: cfg.sandboxMemory, Pids: cfg.sandboxPids,
	}, nil
}

// isStatic reports whether the ELF binary at path has no program interpreter
// (i.e. is statically linked).
func isStatic(path string) (bool, error) {
	f, err := elf.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false, nil
		}
	}
	return true, nil
}
