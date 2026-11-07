package main

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
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
// mode (ADR 0007). The returned warnings are shown in the UI.
func sandboxOptions(ctx context.Context, cfg serveConfig, self string, ci claudeInfo, log *slog.Logger) (*engine.SandboxOptions, []string, error) {
	hint := "or run agents unsandboxed with --sandbox host (permission prompts stay on)"
	p := sandbox.Podman{}
	pv, err := p.Version(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox mode needs rootless podman: %w; %s", err, hint)
	}
	if ok, err := isStatic(self); err != nil || !ok {
		return nil, nil, fmt.Errorf("sandbox mode mounts this binary into workers, so it must be statically linked "+
			"(build it with `make build`): %s (%v)", self, err)
	}
	if ci.version == "" {
		return nil, nil, fmt.Errorf("sandbox mode needs the Claude Code version for the worker config: `%s --version` failed", ci.path)
	}
	sp, warnings, err := packOptions(cfg)
	if err != nil {
		return nil, nil, err
	}
	if ok, err := p.ImageExists(ctx, sp.image); err != nil {
		return nil, nil, err
	} else if !ok {
		build := "agent-office worker-image"
		if sp.pack != nil {
			build += " --pack " + sp.pack.Name
		}
		return nil, nil, fmt.Errorf("worker image %s not found: build it with `%s`; %s", sp.image, build, hint)
	}
	tokenFile, err := sandbox.DefaultTokenFile()
	if err != nil {
		return nil, nil, err
	}
	if _, err := sandbox.ReadTokenFile(tokenFile); err != nil { // checked now, read again at every launch
		return nil, nil, fmt.Errorf("%w\nsandboxed agents authenticate with a token from `claude setup-token`, stored in that "+
			"file (mode 0600, directory 0700; see docs/adr/0015-agent-credential.md); %s", err, hint)
	}
	allow, err := sandbox.ParseAllowlist(sp.hosts)
	if err != nil {
		return nil, nil, fmt.Errorf("AGENT_OFFICE_EGRESS_ALLOW: %w", err)
	}
	hooksDir := filepath.Join(cfg.dataDir, "git-hooks")
	if err := sandbox.WriteGitHooks(hooksDir); err != nil {
		return nil, nil, err
	}
	log.Info("sandbox ready", "podman", pv, "image", sp.image, "pack", packName(sp.pack), "egress_allow",
		strings.Join(sp.hosts, ","), "token_file", tokenFile, "memory", cfg.sandboxMemory, "pids", cfg.sandboxPids)
	sum := sha256.Sum256([]byte(cfg.dataDir))
	return &engine.SandboxOptions{
		Instance: hex.EncodeToString(sum[:6]), Podman: p, Image: sp.image,
		WorkspaceRoot: filepath.Join(cfg.dataDir, "workspaces"), AgentsRoot: filepath.Join(cfg.dataDir, "agents"),
		GitHooks: hooksDir, AgentOffice: self, Claude: ci.path, ClaudeVersion: ci.version,
		TokenFile: tokenFile, Allow: allow, AllowNames: sp.hosts, Memory: cfg.sandboxMemory, Pids: cfg.sandboxPids,
		Pack: sp.pack, SecretsDir: cfg.secretsDir,
	}, warnings, nil
}

// sandboxPack is what the stack pack (or its absence) decides for workers.
type sandboxPack struct {
	pack  *pack.Pack // nil: no pack
	image string
	hosts []string // the egress allowlist
}

// packOptions loads and checks the stack pack (ADR 0023): its image, its
// allowlist (the Phase 3 default without a pack; --egress-allow extras in
// both cases) and its required secrets. Warnings name optional secrets that
// are not set, or a built-in pack that fits the repository when none is set.
func packOptions(cfg serveConfig) (sandboxPack, []string, error) {
	var warnings []string
	if cfg.pack == "" {
		sp := sandboxPack{image: sandbox.BaseImage(), hosts: append(append([]string{}, sandbox.DefaultEgressAllow...), cfg.egressAllow...)}
		if name, marker := suggestPack(cfg.repo); name != "" {
			warnings = append(warnings, fmt.Sprintf("The repository has %s, but no stack pack is set, so sandboxed agents have "+
				"no toolchain for it. Restart with --pack %s (build its image with `agent-office worker-image --pack %s`).",
				marker, name, name))
		}
		return sp, warnings, nil
	}
	p, err := pack.Find(cfg.pack, cfg.packsDir)
	if err != nil {
		return sandboxPack{}, nil, fmt.Errorf("--pack: %w", err)
	}
	sp := sandboxPack{pack: p, image: sandbox.BaseImage(), hosts: append(append([]string{}, p.Egress...), cfg.egressAllow...)}
	files, err := p.BuildFiles()
	if err != nil {
		return sandboxPack{}, nil, fmt.Errorf("--pack %s: %w", p.Name, err)
	}
	if files != nil {
		sp.image = sandbox.PackImage(p.Name, files)
	}
	// Required secrets are checked now and read again at every launch.
	_, skipped, err := p.ReadSecrets(cfg.secretsDir)
	if err != nil {
		return sandboxPack{}, nil, err
	}
	for _, n := range skipped {
		warnings = append(warnings, fmt.Sprintf("Stack pack %s: the optional secret %s is not set (no file %s); agents run without it.",
			p.Name, n, filepath.Join(cfg.secretsDir, n)))
	}
	return sp, warnings, nil
}

// suggestPack names the built-in pack for a marker file at the repository
// root, if any.
func suggestPack(repo string) (name, marker string) {
	for _, m := range []struct{ file, pack string }{{"go.mod", "go"}, {"package.json", "node"}} {
		if _, err := os.Stat(filepath.Join(repo, m.file)); err == nil {
			return m.pack, m.file
		}
	}
	return "", ""
}

func packName(p *pack.Pack) string {
	if p == nil {
		return ""
	}
	return p.Name
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
