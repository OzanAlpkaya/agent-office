package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
}

// Command builds the claude command for spec. It does not start it.
func (c ClaudeCode) Command(spec LaunchSpec) (*exec.Cmd, error) {
	if spec.SessionID == "" || spec.SettingsPath == "" || spec.SocketPath == "" || spec.WorkDir == "" {
		return nil, errors.New("agent: LaunchSpec needs SessionID, SettingsPath, SocketPath and WorkDir")
	}
	args := []string{"--session-id", spec.SessionID, "--settings", spec.SettingsPath}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if !spec.InheritUserSettings {
		args = append(args, "--setting-sources", "project,local")
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
// registered event to hookCommand (e.g. "'/usr/bin/agent-office' hook").
func HookSettings(hookCommand string) ([]byte, error) {
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
		hooks[e] = []matcher{{Hooks: []hook{{Type: "command", Command: hookCommand, Timeout: hookTimeoutSeconds}}}}
	}
	return json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
}

// ShellQuote quotes s for a POSIX shell (hook commands run through a shell).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// claudeHook holds only the payload fields state tracking needs. Everything
// else (prompt, tool_input, tool_response, last_assistant_message, message) is
// never decoded.
type claudeHook struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	ToolName         string `json:"tool_name"`
	Source           string `json:"source"`
	Reason           string `json:"reason"`
	NotificationType string `json:"notification_type"`
	AgentID          string `json:"agent_id"`
	StopHookActive   bool   `json:"stop_hook_active"`
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
	}
	switch h.HookEventName {
	case "SessionStart":
		ev.Kind, ev.Detail = SessionStarted, enum(h.Source)
	case "UserPromptSubmit":
		ev.Kind = TurnStarted
	case "PreToolUse":
		ev.Kind = ToolStarted
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
	case "SessionEnd":
		ev.Kind, ev.Detail = SessionEnded, enum(h.Reason)
	default:
		ev.Kind = Other
	}
	return ev, nil
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
