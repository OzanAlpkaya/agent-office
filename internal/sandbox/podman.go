package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Paths inside a worker container (ADR 0014, ADR 0016).
const (
	ContainerUser   = "1000:1000" // a subordinate uid on the host (ADR 0014)
	WorkspaceDir    = "/workspace"
	ConfigDir       = "/config"             // CLAUDE_CONFIG_DIR
	SkillsDir       = ConfigDir + "/skills" // personal skills; a pack's are mounted here read-only
	HomeDir         = "/tmp/home"
	TmpDir          = HomeDir + "/tmp" // CLAUDE_CODE_TMPDIR; created by sandbox-init
	runDir          = "/run/agent-office"
	HookSocket      = runDir + "/hook.sock"
	MCPSocket       = runDir + "/mcp.sock" // team MCP server (ADR 0006)
	MCPConfigFile   = "/opt/agent-office/mcp.json"
	EgressSocket    = runDir + "/egress.sock"
	APISocket       = runDir + "/api.sock"
	AgentOfficeBin  = "/opt/agent-office/agent-office"
	SettingsFile    = "/opt/agent-office/settings.json"
	GitHooksDir     = "/opt/agent-office/git-hooks"
	ClaudeBin       = "/opt/claude/claude"
	CodexDir        = "/opt/codex" // codex and codex-code-mode-host (ADR 0049)
	CodexBin        = CodexDir + "/codex"
	CodexConfigFile = ConfigDir + "/config.toml"          // CODEX_HOME is the agent's config dir
	CodexAuthFile   = ConfigDir + "/auth.json"            // the placeholder login
	CodexCAFile     = "/opt/agent-office/loopback-ca.pem" // CODEX_CA_CERTIFICATE
	CodexOrigin     = "https://" + APIAddr                // the API socket's loopback bridge, over TLS
	EgressAddr      = "127.0.0.1:3128"                    // sandbox-init bridges these to the sockets
	APIAddr         = "127.0.0.1:8080"
	LabelAgent      = "io.agent-office.agent"
	LabelRole       = "io.agent-office.role"
	LabelInstance   = "io.agent-office.instance" // which daemon (data dir) owns the container
	DefaultMemory   = "2g"
	DefaultPids     = 256
	helperMemory    = "512m"
	helperPids      = 64
	containerPrefix = "ao-"
)

// InstancePattern is what daemon instance ids look like (a data dir hash).
var InstancePattern = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

// AgentIDPattern is what agent ids look like (hex from the engine); it keeps
// them safe in container names, labels and paths.
var AgentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// WorkerSpec describes one worker container. Host paths must be absolute.
type WorkerSpec struct {
	Instance  string // daemon instance; scopes startup reconciliation
	AgentID   string
	Image     string
	Workspace string // the agent's shared clone
	// ReadOnlyWorkspace mounts the clone read-only: a read-only role (ADR 0029)
	// cannot change it, not even through Bash.
	ReadOnlyWorkspace bool
	ObjectsDir        string // the main repository's objects, mounted read-only at the same path
	ConfigDir         string // per-agent Claude Code config dir
	HookSocket        string
	MCPSocket         string // team MCP socket (ADR 0006)
	MCPConfig         string // daemon-generated --mcp-config file
	EgressSocket      string
	APISocket         string
	SecretsSocket     string // optional: stack pack secrets, served once to sandbox-init (ADR 0023)
	Skills            string // optional: stack pack skills, mounted read-only at ConfigDir/skills (ADR 0023)
	AgentOffice       string // static agent-office binary
	Claude            string // the resolved, versioned claude binary
	Settings          string // daemon-generated settings file
	GitHooks          string // directory with the commit-msg hook (WriteGitHooks)
	Git               GitIdentity
	Env               []string
	Memory            string // e.g. "2g"; empty: DefaultMemory
	Pids              int    // 0: DefaultPids
	Command           []string

	// Codex agents (ADR 0049): CLI is "codex", Codex the host dir with its
	// binaries, and these daemon-written files are mounted read-only into the
	// config dir (CODEX_HOME): config.toml, the placeholder login, and the
	// loopback CA. Claude, Settings and MCPConfig are then unused.
	CLI         string
	Codex       string
	CodexConfig string
	CodexAuth   string
	CodexCA     string
}

// claudeEnv is a Claude Code worker's own environment (ADR 0016).
var claudeEnv = []string{
	// Claude Code's default temp dir is /tmp/claude-<uid>; a same-path mount (the
	// objects dir) under /tmp/claude-1000 would create it root-owned and Claude
	// Code refuses to start. Its own tmpfs dir avoids that.
	"CLAUDE_CODE_TMPDIR=" + TmpDir,
	"CLAUDE_CONFIG_DIR=" + ConfigDir,
	"DISABLE_AUTOUPDATER=1",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	"ANTHROPIC_BASE_URL=http://" + APIAddr,
	"CLAUDE_CODE_OAUTH_TOKEN=" + Placeholder,
}

// HelperSpec describes a short-lived git helper container (status, bundle,
// "save uncommitted work") with the agent's repository and nothing else.
type HelperSpec struct {
	Instance   string
	AgentID    string
	Image      string
	Workspace  string
	ObjectsDir string
	Git        GitIdentity // needed by helpers that commit ("save uncommitted work")
	Env        []string
	Command    []string
}

// ContainerName is the worker container's name for agentID.
func ContainerName(agentID string) string { return containerPrefix + agentID }

// helperName is a unique name for one helper run, so it can be removed by
// name: stopping the podman client does not stop the container.
func helperName(agentID string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return containerPrefix + "helper-" + agentID + "-" + hex.EncodeToString(b)
}

// builtinEnv are names the builder sets itself; callers cannot override them.
var builtinEnv = map[string]bool{
	"HOME": true, "CLAUDE_CONFIG_DIR": true, "DISABLE_AUTOUPDATER": true,
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true, "HTTPS_PROXY": true, "https_proxy": true,
	"HTTP_PROXY": true, "http_proxy": true, "NO_PROXY": true, "no_proxy": true, "ANTHROPIC_BASE_URL": true,
	"CLAUDE_CODE_OAUTH_TOKEN": true, "AGENT_OFFICE_SOCKET": true, "AGENT_OFFICE_MCP_SOCKET": true, "TERM": true, "COLORTERM": true,
	"CLAUDE_CODE_TMPDIR": true, "CODEX_HOME": true, "CODEX_CA_CERTIFICATE": true,
	"AGENT_OFFICE_EGRESS_SOCKET": true, "AGENT_OFFICE_API_SOCKET": true, secretsSocketEnv: true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true, "GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"AGENT_OFFICE_CO_AUTHOR": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG_KEY_0": true, "GIT_CONFIG_VALUE_0": true,
	"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_NOSYSTEM": true,
}

// credentialEnv are names that carry credentials; they never enter a
// container's environment (and would be visible in podman inspect).
var credentialEnv = map[string]bool{
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "GH_TOKEN": true, "GITHUB_TOKEN": true, "OPENAI_API_KEY": true,
	"CODEX_API_KEY": true,
	"SSH_AUTH_SOCK": true, "GIT_ASKPASS": true, "SSH_ASKPASS": true, "AWS_SECRET_ACCESS_KEY": true,
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ReservedEnv reports whether name is a variable agent-office, the sandbox or
// the agent CLI depend on, or one that carries a credential agents must never
// hold (ADR 0020), so a stack pack may not supply it (ADR 0023).
func ReservedEnv(name string) bool {
	if builtinEnv[name] || credentialEnv[name] {
		return true
	}
	// Prefixes: agent-office's own, Claude Code's (several without a CLAUDE
	// prefix), git, SSH, the dynamic loader, Bun (the claude binary's runtime),
	// shells, npm config, locale and XDG paths.
	for _, p := range []string{"AGENT_OFFICE_", "CLAUDE", "ANTHROPIC_", "CODEX_", "OPENAI_", "MCP_", "OTEL_", "DISABLE_", "GIT_", "SSH_",
		"LD_", "BUN_", "BASH_", "CGO_", "NPM_CONFIG_", "npm_config_", "LC_", "XDG_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	switch name {
	case "PATH", "USER", "LOGNAME", "SHELL", "PWD", "TMPDIR", "LANG", "ENV", "PROMPT_COMMAND", "IFS",
		"ALL_PROXY", "all_proxy", "API_TIMEOUT_MS",
		"NODE_OPTIONS", "NODE_PATH", "NODE_EXTRA_CA_CERTS",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "AWS_CA_BUNDLE",
		// The go pack's toolchain settings and module routing (a pack's image sets them).
		"GOFLAGS", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "GONOPROXY", "GOTOOLCHAIN",
		"GOPATH", "GOCACHE", "GOMODCACHE", "GOROOT", "GODEBUG", "GOTMPDIR", "GOENV", "GOINSECURE", "GOVCS":
		return true
	}
	return false
}

// WorkerArgs returns the `podman` arguments for a worker. The agent CLI runs
// as a child of `agent-office sandbox-init`, which bridges the loopback
// addresses to the sockets.
func WorkerArgs(s WorkerSpec) ([]string, error) {
	if !AgentIDPattern.MatchString(s.AgentID) || !InstancePattern.MatchString(s.Instance) {
		return nil, fmt.Errorf("sandbox: invalid agent id %q or instance %q", s.AgentID, s.Instance)
	}
	if s.Image == "" || len(s.Command) == 0 {
		return nil, fmt.Errorf("sandbox: worker needs an image and a command")
	}
	mem, pids := s.Memory, s.Pids
	if mem == "" {
		mem = DefaultMemory
	}
	if pids <= 0 {
		pids = DefaultPids
	}
	if !regexp.MustCompile(`^[0-9]+[kmg]$`).MatchString(mem) {
		return nil, fmt.Errorf("sandbox: invalid memory limit %q", mem)
	}
	if err := checkObjectsPath(s.ObjectsDir); err != nil {
		return nil, err
	}
	codex := s.CLI == "codex"
	if codex && (s.Codex == "" || s.CodexConfig == "" || s.CodexAuth == "" || s.CodexCA == "") {
		return nil, fmt.Errorf("sandbox: a Codex worker needs its binaries, config, placeholder login and CA")
	}
	type mount struct {
		src, dst string
		ro       bool
	}
	mounts := []mount{
		{s.Workspace, WorkspaceDir, s.ReadOnlyWorkspace},
		{s.ObjectsDir, s.ObjectsDir, true},
		{s.ConfigDir, ConfigDir, false},
		{s.HookSocket, HookSocket, false},
		{s.MCPSocket, MCPSocket, false},
		{s.EgressSocket, EgressSocket, false},
		{s.APISocket, APISocket, false},
		{s.AgentOffice, AgentOfficeBin, true},
	}
	if codex {
		mounts = append(mounts, mount{s.Codex, CodexDir, true}, mount{s.CodexConfig, CodexConfigFile, true},
			mount{s.CodexAuth, CodexAuthFile, true}, mount{s.CodexCA, CodexCAFile, true})
	} else {
		mounts = append(mounts, mount{s.Claude, ClaudeBin, true}, mount{s.Settings, SettingsFile, true},
			mount{s.MCPConfig, MCPConfigFile, true})
	}
	mounts = append(mounts, mount{s.GitHooks, GitHooksDir, true})
	if s.SecretsSocket != "" {
		mounts = append(mounts, mount{s.SecretsSocket, SecretsSocket, false})
	}
	if s.Skills != "" {
		mounts = append(mounts, mount{s.Skills, SkillsDir, true})
	}
	args := append(baseArgs(s.Instance, s.AgentID, "worker", mem, pids),
		"--name", ContainerName(s.AgentID),
		"-it", "--detach-keys=", // detach keys would swallow Ctrl-P/Ctrl-Q typed into the agent
		"--stop-timeout", "5")
	for _, m := range mounts {
		v, err := volume(m.src, m.dst, m.ro)
		if err != nil {
			return nil, err
		}
		args = append(args, "-v", v)
	}
	env := []string{
		"HOME=" + HomeDir,
		"HTTPS_PROXY=http://" + EgressAddr, "https_proxy=http://" + EgressAddr,
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		"AGENT_OFFICE_SOCKET=" + HookSocket,
		"AGENT_OFFICE_MCP_SOCKET=" + MCPSocket,
		"AGENT_OFFICE_EGRESS_SOCKET=" + EgressSocket,
		"AGENT_OFFICE_API_SOCKET=" + APISocket,
		"TERM=xterm-256color", "COLORTERM=truecolor",
		// Command-scope config outranks the repository's: the hooks are the
		// mounted, read-only ones (ADR 0014).
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=" + GitHooksDir,
	}
	if codex {
		// The placeholder login and the loopback CA; no credential (ADR 0049).
		env = append(env, "CODEX_HOME="+ConfigDir, "CODEX_CA_CERTIFICATE="+CodexCAFile)
	} else {
		env = append(env, claudeEnv...)
	}
	if s.Git.CoAuthor == "" {
		return nil, fmt.Errorf("sandbox: worker needs a co-author trailer")
	}
	gitEnv, err := s.Git.env()
	if err != nil {
		return nil, err
	}
	extra, err := checkEnv(s.Env)
	if err != nil {
		return nil, err
	}
	env = append(env, gitEnv...)
	if s.SecretsSocket != "" {
		env = append(env, secretsSocketEnv+"="+SecretsSocket)
	}
	for _, kv := range append(env, extra...) {
		args = append(args, "-e", kv)
	}
	args = append(args, s.Image, AgentOfficeBin, "sandbox-init", "--")
	return append(args, s.Command...), nil
}

// HelperArgs returns the container name and the `podman` arguments for a git
// helper container. Run it with Podman.RunContainer, which removes it by
// name if the run fails or is cancelled.
func HelperArgs(s HelperSpec) (name string, args []string, err error) {
	if !AgentIDPattern.MatchString(s.AgentID) || !InstancePattern.MatchString(s.Instance) {
		return "", nil, fmt.Errorf("sandbox: invalid agent id %q or instance %q", s.AgentID, s.Instance)
	}
	if s.Image == "" || len(s.Command) == 0 {
		return "", nil, fmt.Errorf("sandbox: helper needs an image and a command")
	}
	if err := checkObjectsPath(s.ObjectsDir); err != nil {
		return "", nil, err
	}
	name = helperName(s.AgentID)
	args = append(baseArgs(s.Instance, s.AgentID, "helper", helperMemory, helperPids), "--name", name)
	for _, m := range []struct {
		src, dst string
		ro       bool
	}{{s.Workspace, WorkspaceDir, false}, {s.ObjectsDir, s.ObjectsDir, true}} {
		v, err := volume(m.src, m.dst, m.ro)
		if err != nil {
			return "", nil, err
		}
		args = append(args, "-v", v)
	}
	gitEnv, err := s.Git.env()
	if err != nil {
		return "", nil, err
	}
	extra, err := checkEnv(s.Env)
	if err != nil {
		return "", nil, err
	}
	for _, kv := range append(append([]string{"HOME=" + HomeDir}, gitEnv...), extra...) {
		args = append(args, "-e", kv)
	}
	args = append(args, s.Image)
	return name, append(args, s.Command...), nil
}

// baseArgs are the isolation flags every agent-office container gets.
func baseArgs(instance, agentID, role, mem string, pids int) []string {
	return []string{"run", "--rm", "--init", "--pull=never",
		"--label", LabelInstance + "=" + instance,
		"--label", LabelAgent + "=" + agentID, "--label", LabelRole + "=" + role,
		"--network", "none",
		"--userns=host", // explicit: a containers.conf default of keep-id would undo ADR 0014
		"--user", ContainerUser,
		"--read-only", "--cap-drop=all", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(pids), "--memory", mem, "--memory-swap", mem,
		"--log-driver", "none", // terminal output and git output are never stored by podman
		"-w", WorkspaceDir,
	}
}

// containerPaths are used inside every container; the objects dir, mounted at
// its host path, must not overlap them.
var containerPaths = []string{WorkspaceDir, ConfigDir, HomeDir, runDir, "/opt/agent-office", "/opt/claude", CodexDir,
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/proc", "/sys", "/dev"}

func checkObjectsPath(p string) error {
	for _, c := range containerPaths {
		if p == c || strings.HasPrefix(p, c+"/") || strings.HasPrefix(c, p+"/") {
			return fmt.Errorf("sandbox: the repository's objects path %s overlaps %s inside the container; move the repository", p, c)
		}
	}
	return nil
}

func volume(src, dst string, ro bool) (string, error) {
	if !filepath.IsAbs(src) || filepath.Clean(src) != src || strings.ContainsAny(src, ":,\n\x00") {
		return "", fmt.Errorf("sandbox: mount source must be a clean absolute path without ':' or ',': %q", src)
	}
	v := src + ":" + dst
	if ro {
		v += ":ro"
	}
	return v, nil
}

func checkEnv(env []string) ([]string, error) {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok || !envName.MatchString(k):
			return nil, fmt.Errorf("sandbox: invalid env entry %q", k)
		case builtinEnv[k]:
			return nil, fmt.Errorf("sandbox: env %s is set by agent-office", k)
		case credentialEnv[k] || strings.Contains(strings.ToUpper(k), "TOKEN") || strings.Contains(strings.ToUpper(k), "SECRET"):
			return nil, fmt.Errorf("sandbox: env %s looks like a credential; credentials never enter a container", k)
		case strings.ContainsAny(v, "\n\x00"):
			return nil, fmt.Errorf("sandbox: env %s has a newline or NUL", k)
		}
	}
	return env, nil
}
