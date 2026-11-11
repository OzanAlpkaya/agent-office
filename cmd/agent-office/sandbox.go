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

// sandboxOptions checks everything sandbox mode needs for every project and
// fails loudly, with the fix, when something is missing. There is no silent
// fallback to host mode (ADR 0007). The pack of each project (image,
// allowlist, secrets) is added when the project opens (applyPack, ADR 0046).
func sandboxOptions(ctx context.Context, cfg serveConfig, self string, ci claudeInfo, log *slog.Logger) (*engine.SandboxOptions, error) {
	p := sandbox.Podman{}
	pv, err := p.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("sandbox mode needs rootless podman: %w; %s", err, sandboxHint)
	}
	if ok, err := isStatic(self); err != nil || !ok {
		return nil, fmt.Errorf("sandbox mode mounts this binary into workers, so it must be statically linked "+
			"(build it with `make build`): %s (%v)", self, err)
	}
	if ci.version == "" {
		return nil, fmt.Errorf("sandbox mode needs the Claude Code version for the worker config: `%s --version` failed", ci.path)
	}
	tokenFile, err := sandbox.DefaultTokenFile()
	if err != nil {
		return nil, err
	}
	if _, err := sandbox.ReadTokenFile(tokenFile); err != nil { // checked now, read again at every launch
		return nil, fmt.Errorf("%w\nsandboxed agents authenticate with a token from `claude setup-token`, stored in that "+
			"file (mode 0600, directory 0700; see docs/adr/0015-agent-credential.md); %s", err, sandboxHint)
	}
	hooksDir := filepath.Join(cfg.dataDir, "git-hooks")
	if err := sandbox.WriteGitHooks(hooksDir); err != nil {
		return nil, err
	}
	log.Info("sandbox ready", "podman", pv, "token_file", tokenFile, "memory", cfg.sandboxMemory, "pids", cfg.sandboxPids)
	sum := sha256.Sum256([]byte(cfg.dataDir))
	return &engine.SandboxOptions{
		Instance: hex.EncodeToString(sum[:6]), Podman: p,
		WorkspaceRoot: filepath.Join(cfg.dataDir, "workspaces"), AgentsRoot: filepath.Join(cfg.dataDir, "agents"),
		GitHooks: hooksDir, AgentOffice: self, Claude: ci.path, ClaudeVersion: ci.version,
		TokenFile: tokenFile, Memory: cfg.sandboxMemory, Pids: cfg.sandboxPids,
	}, nil
}

const sandboxHint = "or run agents unsandboxed with --sandbox host (permission prompts stay on)"

// applyPack sets a project's stack pack on its copy of the sandbox options:
// the image (which must be built), the allowlist and the secrets dir.
func applyPack(ctx context.Context, sb *engine.SandboxOptions, spec packSpec) ([]string, error) {
	sp, warnings, err := packOptions(spec)
	if err != nil {
		return nil, err
	}
	if ok, err := sb.Podman.ImageExists(ctx, sp.image); err != nil {
		return nil, err
	} else if !ok {
		build := "agent-office worker-image"
		if sp.pack != nil {
			build += " --pack " + sp.pack.Name
		}
		return nil, fmt.Errorf("worker image %s not found: build it with `%s`", sp.image, build)
	}
	allow, err := sandbox.ParseAllowlist(sp.hosts)
	if err != nil {
		return nil, fmt.Errorf("egress allowlist: %w", err)
	}
	sb.Image, sb.Allow, sb.AllowNames, sb.Pack, sb.SecretsDir = sp.image, allow, sp.hosts, sp.pack, spec.secretsDir
	if sp.pack != nil && sp.pack.Memory != "" {
		sb.Memory = sp.pack.Memory // over --sandbox-memory; a project's worker_memory wins over both (ADR 0051)
	}
	if sp.pack != nil && sp.pack.Pids != 0 {
		sb.Pids = sp.pack.Pids // over --sandbox-pids; a project's worker_pids wins over both (Phase 17)
	}
	return warnings, nil
}

// sandboxPack is what the stack pack (or its absence) decides for workers.
type sandboxPack struct {
	pack  *pack.Pack // nil: no pack
	image string
	hosts []string // the egress allowlist
}

// packSpec is what decides a project's pack: the effective settings.
type packSpec struct {
	name       string   // "": no pack
	label      string   // how the setting is named in errors: --pack, or the project setting
	packsDir   string   // user packs
	secretsDir string   // the pack's secret values
	egress     []string // --egress-allow and the project's extra names
	repo       string   // for the hint when no pack is set
}

// packOptions loads and checks the stack pack (ADR 0023): its image, its
// allowlist (the Phase 3 default without a pack; extra names in both cases)
// and its required secrets. Warnings name optional secrets that are not set,
// or a built-in pack that fits the repository when none is set.
func packOptions(spec packSpec) (sandboxPack, []string, error) {
	var warnings []string
	if spec.name == "" {
		sp := sandboxPack{image: sandbox.BaseImage(), hosts: append(append([]string{}, sandbox.DefaultEgressAllow...), spec.egress...)}
		if name, marker := suggestPack(spec.repo); name != "" {
			warnings = append(warnings, fmt.Sprintf("The repository has %s, but no stack pack is set, so sandboxed agents have "+
				"no toolchain for it. Set the project's pack to %s (build its image with `agent-office worker-image --pack %s`).",
				marker, name, name))
		}
		return sp, warnings, nil
	}
	p, err := pack.Find(spec.name, spec.packsDir)
	if err != nil {
		return sandboxPack{}, nil, fmt.Errorf("%s: %w", spec.label, err)
	}
	sp := sandboxPack{pack: p, image: sandbox.BaseImage(), hosts: append(append([]string{}, p.Egress...), spec.egress...)}
	if sp.image, err = p.Image(); err != nil {
		return sandboxPack{}, nil, fmt.Errorf("%s %s: %w", spec.label, p.Name, err)
	}
	// Required secrets are checked now and read again at every launch.
	_, skipped, err := p.ReadSecrets(spec.secretsDir)
	if err != nil {
		return sandboxPack{}, nil, err
	}
	for _, n := range skipped {
		warnings = append(warnings, fmt.Sprintf("Stack pack %s: the optional secret %s is not set (no file %s); agents run without it.",
			p.Name, n, filepath.Join(spec.secretsDir, n)))
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
