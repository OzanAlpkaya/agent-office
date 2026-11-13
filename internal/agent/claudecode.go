package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// claudeHookEvents are the Claude Code hook events the daemon registers.
var claudeHookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
	"PermissionRequest", "PermissionDenied", "Notification", "Stop", "StopFailure",
	"SubagentStop", "SessionEnd",
}

// hookTimeoutSeconds bounds each hook call. The forwarder uses a shorter
// timeout so it can always exit cleanly first.
const hookTimeoutSeconds = 10

// permissionHookTimeoutSeconds bounds a PermissionRequest hook, which the
// daemon may hold while the Tech Lead decides a sandboxed worker's rm
// prompt (Phase 20, ADR 0065); its forwarder runs with --wait.
const permissionHookTimeoutSeconds = 120

// WaitFlag makes `agent-office hook` wait as long as a held PermissionRequest.
const WaitFlag = "--wait"

// SocketEnv is the environment variable that tells `agent-office hook` which
// per-agent socket to forward to.
const SocketEnv = "AGENT_OFFICE_SOCKET"

// ClaudeCode is the adapter for the Claude Code CLI.
type ClaudeCode struct {
	Bin string // path or name of the claude binary
}

// LaunchSpec describes one agent session.
type LaunchSpec struct {
	SessionID           string   // UUID passed as --session-id
	Model               string   // optional --model
	WorkDir             string   // working directory (the repo)
	SettingsPath        string   // daemon-generated settings file (hooks)
	InheritUserSettings bool     // false: --setting-sources project,local
	SocketPath          string   // per-agent hook socket
	BaseEnv             []string // usually os.Environ(); scrubbed before use
	InitialPrompt       string   // optional first prompt of the interactive session
	MCPConfigPath       string   // optional --mcp-config file with the team MCP server (ADR 0006)
	ResumeFrom          string   // optional: continue this session's conversation under SessionID (ADR 0035)
}

// Command builds the claude command for spec. It does not start it.
func (c ClaudeCode) Command(spec LaunchSpec) (*exec.Cmd, error) {
	if spec.SessionID == "" || spec.SettingsPath == "" || spec.SocketPath == "" || spec.WorkDir == "" {
		return nil, errors.New("agent: LaunchSpec needs SessionID, SettingsPath, SocketPath and WorkDir")
	}
	if spec.ResumeFrom != "" && !uuidRe.MatchString(spec.ResumeFrom) {
		return nil, fmt.Errorf("agent: ResumeFrom must be a session UUID, got %q", spec.ResumeFrom)
	}
	args := append(resumeArgs(spec.ResumeFrom), "--session-id", spec.SessionID, "--settings", spec.SettingsPath)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if !spec.InheritUserSettings {
		args = append(args, "--setting-sources", "project,local")
	}
	if spec.MCPConfigPath != "" {
		args = append(args, "--mcp-config", spec.MCPConfigPath)
		if !spec.InheritUserSettings {
			// The user's own MCP servers belong to their settings too.
			args = append(args, "--strict-mcp-config")
		}
	}
	if spec.InitialPrompt != "" {
		// After "--", so a prompt starting with "-" is never parsed as a flag.
		args = append(args, "--", spec.InitialPrompt)
	}
	cmd := exec.Command(c.Bin, args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(ScrubEnv(spec.BaseEnv),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		SocketEnv+"="+spec.SocketPath,
	)
	return cmd, nil
}

// ScrubEnv removes variables that must not leak from the daemon into an agent:
// those Claude Code sets for its own children (one is a messaging token), the
// daemon's own AGENT_OFFICE_* settings, and TERM (set explicitly).
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_PID", k == "CLAUDE_EFFORT", k == "TERM", k == "COLORTERM",
			strings.HasPrefix(k, "CLAUDE_CODE_"), strings.HasPrefix(k, "AGENT_OFFICE_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}

// HookSettings returns a Claude Code settings document that sends every
// registered event to hookCommand (e.g. "'/usr/bin/agent-office' hook") and
// denies the permission rules in deny (a role's, ADR 0029).
func HookSettings(hookCommand string, deny []string) ([]byte, error) {
	return settings(hookCommand, deny, nil)
}

// TestedClaudeCodeVersion is the Claude Code version the pre-seeded config
// keys (SandboxSettings, SeedConfig) were last tested with: Spike F with
// 2.1.319, re-checked with 2.1.322 on 2026-11-11 and 2.1.323 on 2026-11-13
// (each key still suppresses exactly its own dialog, all of them together
// none). They are undocumented; the daemon warns when the installed version
// differs, unless `agent-office check-claude` passed it on this machine.
const TestedClaudeCodeVersion = "2.1.323"

var versionRe = regexp.MustCompile(`^(\d+\.\d+\.\d+)\b`)

// uuidRe matches the session ids the daemon generates (--session-id).
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseVersion extracts "2.1.319" from `claude --version` output such as
// "2.1.319 (Claude Code)".
func ParseVersion(out string) (string, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("agent: unrecognized claude version %q", strings.TrimSpace(out))
	}
	return m[1], nil
}

// SandboxSettings is HookSettings plus the keys that skip the theme picker and
// the bypass-permissions warning in a fresh config dir (ADR 0016). Only
// sandboxed workers get it.
// Deny rules hold under --dangerously-skip-permissions: checked with 2.1.319,
// a denied tool is not offered to the model at all (Phase 12 plan).
func SandboxSettings(hookCommand string, deny []string) ([]byte, error) {
	return settings(hookCommand, deny, map[string]any{"theme": "dark", "skipDangerousModePermissionPrompt": true})
}

// SeedConfig is the .claude.json written into a worker's fresh
// CLAUDE_CONFIG_DIR: onboarding done for this CLI version and the workspace
// trusted, so no dialog blocks the session (ADR 0016).
func SeedConfig(version, workspace string) ([]byte, error) {
	if _, err := ParseVersion(version); err != nil || !strings.HasPrefix(workspace, "/") {
		return nil, fmt.Errorf("agent: SeedConfig needs a version and an absolute workspace path")
	}
	return json.MarshalIndent(map[string]any{
		"hasCompletedOnboarding": true,
		"lastOnboardingVersion":  version,
		"projects":               map[string]any{workspace: map[string]any{"hasTrustDialogAccepted": true}},
	}, "", "  ")
}

// ContainerSpec describes the agent CLI command inside a sandboxed worker.
// Paths are container paths.
type ContainerSpec struct {
	Bin           string // e.g. /opt/claude/claude
	SessionID     string
	Model         string
	SettingsPath  string
	MCPConfigPath string // optional; the worker then uses only this MCP config
	InitialPrompt string
	ResumeFrom    string // optional: continue this session's conversation under SessionID (ADR 0035)
}

// resumeArgs continues session from's conversation in a new session: with
// --fork-session the CLI takes the --session-id that follows (checked with
// 2.1.319: without it, --session-id and --resume are refused together), so
// hooks keep carrying the id the daemon chose and the old transcript stays as
// it was.
func resumeArgs(from string) []string {
	if from == "" {
		return nil
	}
	return []string{"--resume", from, "--fork-session"}
}

// ContainerArgs returns the CLI command line for a sandboxed worker. It is
// the only place that turns permission prompts off: the agent then runs with
// full permissions, which is acceptable only inside the container (ADR 0007).
func ContainerArgs(s ContainerSpec) ([]string, error) {
	if s.Bin == "" || s.SessionID == "" || s.SettingsPath == "" {
		return nil, errors.New("agent: ContainerSpec needs Bin, SessionID and SettingsPath")
	}
	if s.ResumeFrom != "" && !uuidRe.MatchString(s.ResumeFrom) {
		return nil, fmt.Errorf("agent: ResumeFrom must be a session UUID, got %q", s.ResumeFrom)
	}
	args := append(append([]string{s.Bin}, resumeArgs(s.ResumeFrom)...), "--session-id", s.SessionID, "--settings", s.SettingsPath)
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.MCPConfigPath != "" {
		args = append(args, "--mcp-config", s.MCPConfigPath, "--strict-mcp-config")
	}
	args = append(args, "--dangerously-skip-permissions")
	if s.InitialPrompt != "" {
		args = append(args, "--", s.InitialPrompt)
	}
	return args, nil
}

// MCPServer is an extra MCP server from a stack pack (ADR 0023): a stdio
// command or an HTTP URL. The pack loader has validated it.
type MCPServer struct {
	Command string
	Args    []string
	URL     string
}

// MCPConfig returns a --mcp-config document with the team server, command
// (the agent-office binary, as a path, not a shell string) run as
// `<command> mcp` and bridged to socket (ADR 0006), plus extra servers.
func MCPConfig(command, socket string, extra map[string]MCPServer) ([]byte, error) {
	if !strings.HasPrefix(command, "/") || !strings.HasPrefix(socket, "/") {
		return nil, errors.New("agent: MCPConfig needs absolute command and socket paths")
	}
	servers := map[string]any{
		mcp.ServerName: map[string]any{
			"type":    "stdio",
			"command": command,
			"args":    []string{"mcp"},
			"env":     map[string]string{mcp.SocketEnv: socket},
		},
	}
	for name, s := range extra {
		switch {
		case name == mcp.ServerName:
			return nil, fmt.Errorf("agent: MCP server name %q is reserved", name)
		case (s.Command == "") == (s.URL == ""):
			return nil, fmt.Errorf("agent: MCP server %q needs a command or a URL", name)
		case s.Command != "":
			args := s.Args
			if args == nil {
				args = []string{}
			}
			servers[name] = map[string]any{"type": "stdio", "command": s.Command, "args": args}
		default:
			servers[name] = map[string]any{"type": "http", "url": s.URL}
		}
	}
	return json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
}

func settings(hookCommand string, deny []string, extra map[string]any) ([]byte, error) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Hooks []hook `json:"hooks"`
	}
	hooks := map[string][]matcher{}
	for _, e := range claudeHookEvents {
		h := hook{Type: "command", Command: hookCommand, Timeout: hookTimeoutSeconds}
		if e == "PermissionRequest" {
			h.Command, h.Timeout = hookCommand+" "+WaitFlag, permissionHookTimeoutSeconds
		}
		hooks[e] = []matcher{{Hooks: []hook{h}}}
	}
	// The team tools are pre-allowed: the daemon authorizes every call itself
	// (ADR 0017), so a host-mode permission prompt would add nothing. Every
	// other tool still prompts in host mode.
	perms := map[string]any{"allow": []string{"mcp__" + mcp.ServerName}}
	if len(deny) > 0 {
		perms["deny"] = deny
	}
	doc := map[string]any{"hooks": hooks, "permissions": perms}
	for k, v := range extra {
		doc[k] = v
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ShellQuote quotes s for a POSIX shell (hook commands run through a shell).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// claudeHook holds only the payload fields state tracking needs. Everything
// else (prompt, tool_input, tool_response, message) is never decoded, but for
// one boolean of a Bash call's tool_input, run_in_background (ADR 0064), and
// StopFailure's error and last_assistant_message, reduced to whether the
// connection was lost (ADR 0067).
type claudeHook struct {
	HookEventName    string          `json:"hook_event_name"`
	SessionID        string          `json:"session_id"`
	ToolName         string          `json:"tool_name"`
	Source           string          `json:"source"`
	Reason           string          `json:"reason"`
	NotificationType string          `json:"notification_type"`
	AgentID          string          `json:"agent_id"`
	StopHookActive   bool            `json:"stop_hook_active"`
	TranscriptPath   string          `json:"transcript_path"`
	ToolInput        json.RawMessage `json:"tool_input"`
	Error            string          `json:"error"`                  // StopFailure: an enum
	LastMessage      string          `json:"last_assistant_message"` // StopFailure: the API error's text
}

// lostConnection reports whether a StopFailure is Claude Code's API error for
// a lost connection: its own texts for a cut stream ("Connection lost
// mid-response", measured with 2.1.323), connection errors, and the 502, 503
// or 504 without a body the daemon's API proxy answers when the upstream is
// unreachable. Rate limits, authentication, billing and invalid requests are
// not; neither is any text that is not an API error.
func lostConnection(errKind, msg string) bool {
	if errKind != "server_error" && errKind != "unknown" {
		return false
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if !strings.HasPrefix(strings.TrimSpace(msg), "API Error:") {
		return false
	}
	m := strings.ToLower(msg)
	for _, p := range []string{
		"connection lost", "connection error", "connection refused", "connection reset", "part of the response never arrived",
		"the response stopped arriving", "the response stalled", "went to sleep", "server error mid-response",
		"fetch failed", "econnreset", "econnrefused", "etimedout", "socket hang up", "timed out", "network",
	} {
		if strings.Contains(m, p) {
			return true
		}
	}
	for _, code := range []string{"502", "503", "504"} {
		if strings.Contains(m, code+" status code (no body)") {
			return true
		}
	}
	return false
}

// backgroundBash reports whether a Bash tool_input asks to run in the
// background. Anything unexpected is false.
func backgroundBash(tool string, input json.RawMessage) bool {
	if tool != "Bash" || len(input) == 0 {
		return false
	}
	var in struct {
		RunInBackground bool `json:"run_in_background"`
	}
	return json.Unmarshal(input, &in) == nil && in.RunInBackground
}

// PermissionInput is what a Claude Code PermissionRequest for Bash asks
// about (Phase 20, ADR 0065): the command, the shell's directory and the
// permission mode. The command is agent text: it is read to decide the
// sandbox's rm prompts and shown to the Tech Lead, never stored in events.
type PermissionInput struct {
	Command string
	Cwd     string
	Mode    string // permission_mode, e.g. bypassPermissions
}

// maxPermissionCommand bounds the command read from a payload.
const maxPermissionCommand = 16 << 10

// ClaudePermissionInput reads a PermissionRequest for Bash; ok is false for
// any other payload.
func ClaudePermissionInput(raw []byte) (PermissionInput, bool) {
	var h struct {
		HookEventName  string `json:"hook_event_name"`
		ToolName       string `json:"tool_name"`
		Cwd            string `json:"cwd"`
		PermissionMode string `json:"permission_mode"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &h); err != nil || h.HookEventName != "PermissionRequest" || h.ToolName != "Bash" ||
		h.ToolInput.Command == "" || len(h.ToolInput.Command) > maxPermissionCommand || len(h.Cwd) > 4096 {
		return PermissionInput{}, false
	}
	return PermissionInput{Command: h.ToolInput.Command, Cwd: h.Cwd, Mode: enum(h.PermissionMode)}, true
}

// ErrInvalidPayload is returned for hook payloads that cannot be used.
var ErrInvalidPayload = errors.New("agent: invalid hook payload")

// ParseClaudeHook turns a Claude Code hook payload into an Event.
func ParseClaudeHook(raw []byte) (Event, error) {
	var h claudeHook
	if err := json.Unmarshal(raw, &h); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if h.HookEventName == "" || h.SessionID == "" || len(h.SessionID) > 64 {
		return Event{}, fmt.Errorf("%w: missing hook_event_name or session_id", ErrInvalidPayload)
	}
	ev := Event{
		SessionID:      h.SessionID,
		Hook:           h.HookEventName,
		Tool:           enum(h.ToolName),
		Subagent:       h.AgentID != "",
		StopHookActive: h.StopHookActive,
		TranscriptPath: transcriptPath(h.TranscriptPath),
	}
	switch h.HookEventName {
	case "SessionStart":
		ev.Kind, ev.Detail = SessionStarted, enum(h.Source)
	case "UserPromptSubmit":
		ev.Kind = TurnStarted
	case "PreToolUse":
		ev.Kind = ToolStarted
		ev.Background = backgroundBash(h.ToolName, h.ToolInput)
	case "PostToolUse":
		ev.Kind = ToolFinished
	case "PostToolUseFailure":
		ev.Kind = ToolFailed
	case "PermissionDenied":
		ev.Kind, ev.Detail = ToolFailed, "permission_denied"
	case "PermissionRequest":
		ev.Kind = PermissionRequested
	case "Notification":
		ev.Detail = enum(h.NotificationType)
		switch h.NotificationType {
		case "idle_prompt":
			ev.Kind = Idle
		case "permission_prompt":
			ev.Kind = PermissionRequested
		default:
			ev.Kind = Other
		}
	case "Stop":
		ev.Kind = TurnFinished
	case "StopFailure":
		ev.Kind, ev.Detail = TurnFinished, "failure"
		ev.NetworkLost = lostConnection(h.Error, h.LastMessage)
	case "SessionEnd":
		ev.Kind, ev.Detail = SessionEnded, enum(h.Reason)
	default:
		ev.Kind = Other
	}
	return ev, nil
}

// transcriptPath keeps a plausible transcript path: absolute, a .jsonl file,
// bounded, printable. The engine validates it further before reading it.
func transcriptPath(p string) string {
	if len(p) > 4096 || !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, ".jsonl") {
		return ""
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return p
}

// enum keeps a value only if it looks like an identifier (letters, digits,
// '_', '-', '.', ':'), so free text can never end up in an Event.
func enum(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return s
}
