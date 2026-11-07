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
	ConfigDir       = "/config" // CLAUDE_CONFIG_DIR
	HomeDir         = "/tmp/home"
	runDir          = "/run/agent-office"
	HookSocket      = runDir + "/hook.sock"
	EgressSocket    = runDir + "/egress.sock"
	APISocket       = runDir + "/api.sock"
	AgentOfficeBin  = "/opt/agent-office/agent-office"
	SettingsFile    = "/opt/agent-office/settings.json"
	GitHooksDir     = "/opt/agent-office/git-hooks"
	ClaudeBin       = "/opt/claude/claude"
	EgressAddr      = "127.0.0.1:3128" // sandbox-init bridges these to the sockets
	APIAddr         = "127.0.0.1:8080"
	LabelAgent      = "io.agent-office.agent"
	LabelRole       = "io.agent-office.role"
	DefaultMemory   = "2g"
	DefaultPids     = 256
	helperMemory    = "512m"
	helperPids      = 64
	containerPrefix = "ao-"
)

// AgentIDPattern is what agent ids look like (hex from the engine); it keeps
// them safe in container names, labels and paths.
var AgentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// WorkerSpec describes one worker container. Host paths must be absolute.
type WorkerSpec struct {
	AgentID      string
	Image        string
	Workspace    string // the agent's shared clone
	ObjectsDir   string // the main repository's objects, mounted read-only at the same path
	ConfigDir    string // per-agent Claude Code config dir
	HookSocket   string
	EgressSocket string
	APISocket    string
	AgentOffice  string // static agent-office binary
	Claude       string // the resolved, versioned claude binary
	Settings     string // daemon-generated settings file
	GitHooks     string // directory with the commit-msg hook
	Env          []string
	Memory       string // e.g. "2g"; empty: DefaultMemory
	Pids         int    // 0: DefaultPids
	Command      []string
}

// HelperSpec describes a short-lived git helper container (status, bundle,
// "save uncommitted work") with the agent's repository and nothing else.
type HelperSpec struct {
	AgentID    string
	Image      string
	Workspace  string
	ObjectsDir string
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
	"CLAUDE_CODE_OAUTH_TOKEN": true, "AGENT_OFFICE_SOCKET": true, "TERM": true, "COLORTERM": true,
	"AGENT_OFFICE_EGRESS_SOCKET": true, "AGENT_OFFICE_API_SOCKET": true,
}

// credentialEnv are names that carry credentials; they never enter a
// container's environment (and would be visible in podman inspect).
var credentialEnv = map[string]bool{
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "GH_TOKEN": true, "GITHUB_TOKEN": true,
	"SSH_AUTH_SOCK": true, "GIT_ASKPASS": true, "SSH_ASKPASS": true, "AWS_SECRET_ACCESS_KEY": true,
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// WorkerArgs returns the `podman` arguments for a worker. The agent CLI runs
// as a child of `agent-office sandbox-init`, which bridges the loopback
// addresses to the sockets.
func WorkerArgs(s WorkerSpec) ([]string, error) {
	if !AgentIDPattern.MatchString(s.AgentID) {
		return nil, fmt.Errorf("sandbox: invalid agent id %q", s.AgentID)
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
	mounts := []struct {
		src, dst string
		ro       bool
	}{
		{s.Workspace, WorkspaceDir, false},
		{s.ObjectsDir, s.ObjectsDir, true},
		{s.ConfigDir, ConfigDir, false},
		{s.HookSocket, HookSocket, false},
		{s.EgressSocket, EgressSocket, false},
		{s.APISocket, APISocket, false},
		{s.AgentOffice, AgentOfficeBin, true},
		{s.Claude, ClaudeBin, true},
		{s.Settings, SettingsFile, true},
		{s.GitHooks, GitHooksDir, true},
	}
	args := append(baseArgs(s.AgentID, "worker", mem, pids),
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
		"CLAUDE_CONFIG_DIR=" + ConfigDir,
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"HTTPS_PROXY=http://" + EgressAddr, "https_proxy=http://" + EgressAddr,
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		"ANTHROPIC_BASE_URL=http://" + APIAddr,
		"CLAUDE_CODE_OAUTH_TOKEN=" + Placeholder,
		"AGENT_OFFICE_SOCKET=" + HookSocket,
		"AGENT_OFFICE_EGRESS_SOCKET=" + EgressSocket,
		"AGENT_OFFICE_API_SOCKET=" + APISocket,
		"TERM=xterm-256color", "COLORTERM=truecolor",
	}
	extra, err := checkEnv(s.Env)
	if err != nil {
		return nil, err
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
	if !AgentIDPattern.MatchString(s.AgentID) {
		return "", nil, fmt.Errorf("sandbox: invalid agent id %q", s.AgentID)
	}
	if s.Image == "" || len(s.Command) == 0 {
		return "", nil, fmt.Errorf("sandbox: helper needs an image and a command")
	}
	name = helperName(s.AgentID)
	args = append(baseArgs(s.AgentID, "helper", helperMemory, helperPids), "--name", name)
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
	extra, err := checkEnv(s.Env)
	if err != nil {
		return "", nil, err
	}
	for _, kv := range append([]string{"HOME=" + HomeDir}, extra...) {
		args = append(args, "-e", kv)
	}
	args = append(args, s.Image)
	return name, append(args, s.Command...), nil
}

// baseArgs are the isolation flags every agent-office container gets.
func baseArgs(agentID, role, mem string, pids int) []string {
	return []string{"run", "--rm", "--init", "--pull=never",
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
