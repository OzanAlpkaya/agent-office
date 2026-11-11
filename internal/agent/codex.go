package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Agent CLIs (agents.cli, role files' cli:, ADR 0050).
const (
	CLIClaude = "claude" // Claude Code
	CLICodex  = "codex"  // OpenAI Codex CLI (ADR 0049)
)

// ValidCLI reports whether name is an agent CLI agent-office supports.
func ValidCLI(name string) bool { return name == CLIClaude || name == CLICodex }

// TestedCodexVersion is the Codex CLI version the generated config (hooks,
// provider, features, folder trust) was tested with in Spike G; the daemon
// warns when the installed version differs.
const TestedCodexVersion = "0.167.1"

var codexVersionRe = regexp.MustCompile(`^codex-cli (\d+\.\d+\.\d+)\b`)

// ParseCodexVersion extracts "0.167.1" from `codex --version` output.
func ParseCodexVersion(out string) (string, error) {
	m := codexVersionRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("agent: unrecognized codex version %q", strings.TrimSpace(out))
	}
	return m[1], nil
}

// codexHookEvents are the hooks the generated config registers (Spike G).
var codexHookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
	"Stop", "SessionEnd", "Interrupt"}

type codexHook struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	ToolName       string `json:"tool_name"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// ParseCodexHook turns a Codex hook payload into an Event. Prompts, tool
// inputs and messages in the payload are never read.
func ParseCodexHook(raw []byte) (Event, error) {
	var h codexHook
	if err := json.Unmarshal(raw, &h); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if h.HookEventName == "" || h.SessionID == "" || len(h.SessionID) > 64 || enum(h.SessionID) == "" {
		return Event{}, fmt.Errorf("%w: missing hook_event_name or session_id", ErrInvalidPayload)
	}
	ev := Event{SessionID: h.SessionID, Hook: enum(h.HookEventName), Tool: enum(h.ToolName), StopHookActive: h.StopHookActive}
	switch h.HookEventName {
	case "SessionStart":
		ev.Kind, ev.Detail = SessionStarted, enum(h.Source)
	case "UserPromptSubmit":
		ev.Kind = TurnStarted
	case "PreToolUse":
		ev.Kind = ToolStarted
	case "PostToolUse":
		ev.Kind = ToolFinished
	case "PermissionRequest":
		ev.Kind = PermissionRequested
	case "Stop":
		ev.Kind = TurnFinished
	case "Interrupt":
		ev.Kind = UserInterrupted
	case "SessionEnd":
		ev.Kind, ev.Detail = SessionEnded, enum(h.Reason)
	default:
		ev.Kind = Other
	}
	return ev, nil
}

// CodexPlaceholder returns the auth.json of a sandboxed Codex agent and the
// bearer its requests then carry (ADR 0049): a ChatGPT login whose tokens
// are unsigned JWTs with no credential in them, expiring in 2100 so Codex
// never tries to refresh, and the user's account id and plan (identifiers
// Codex's TUI checks against the accounts list; not credentials). It is the
// same for every agent of one account, so the API socket can recognise it.
func CodexPlaceholder(account, plan string) (authJSON []byte, bearer string, err error) {
	if account == "" || len(account) > 64 || enum(account) == "" || (plan != "" && enum(plan) == "") {
		return nil, "", errors.New("agent: CodexPlaceholder needs an account id and a plan name")
	}
	if plan == "" {
		plan = "plus"
	}
	const iat, exp = 1767225600, 4102444800 // 2026-01-01, 2100-01-01: fixed, so the bearer is stable
	claims := map[string]any{"chatgpt_account_id": account, "chatgpt_plan_type": plan,
		"chatgpt_user_id": "agent-office-placeholder", "user_id": "agent-office-placeholder"}
	jwt := func(c map[string]any) string {
		enc := func(v any) string {
			b, _ := json.Marshal(v)
			return base64.RawURLEncoding.EncodeToString(b)
		}
		return enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + enc(c) + ".agent-office-placeholder"
	}
	access := jwt(map[string]any{"iss": "agent-office", "sub": "agent-office-placeholder", "iat": iat, "exp": exp,
		"https://api.openai.com/auth": claims})
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": jwt(map[string]any{"iss": "agent-office", "sub": "agent-office-placeholder", "aud": []string{"agent-office"},
				"email": "agent@agent-office.invalid", "email_verified": true, "iat": iat, "exp": exp,
				"https://api.openai.com/auth": claims}),
			"access_token":  access,
			"refresh_token": "agent-office-placeholder-not-a-credential",
			"account_id":    account,
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	return b, access, err
}

// CodexConfigSpec describes a sandboxed Codex agent's config.toml.
type CodexConfigSpec struct {
	BackendOrigin string // e.g. https://127.0.0.1:8080: the API socket's loopback bridge
	HookCommand   string // e.g. "/opt/agent-office/agent-office hook"
	MCPCommand    string // the agent-office binary in the container, run as `<cmd> mcp`
	MCPSocket     string // the team MCP socket in the container
	Workspace     string // trusted folder, e.g. /workspace
	Extra         map[string]MCPServer
}

// CodexConfig returns the config.toml of a sandboxed Codex agent: the
// ChatGPT backend at the API socket (ADR 0049), hooks for every state, the
// team MCP server (ADR 0006), the workspace trusted, and everything that
// would need other network access off (plugins, apps, analytics, updates,
// the shared background server).
func CodexConfig(s CodexConfigSpec) ([]byte, error) {
	if !strings.HasPrefix(s.BackendOrigin, "https://") || s.HookCommand == "" || !strings.HasPrefix(s.MCPCommand, "/") ||
		!strings.HasPrefix(s.MCPSocket, "/") || !strings.HasPrefix(s.Workspace, "/") {
		return nil, errors.New("agent: CodexConfig needs an https origin, a hook command and absolute paths")
	}
	var b strings.Builder
	q := func(v string) string { j, _ := json.Marshal(v); return string(j) } // a JSON string is a TOML basic string
	fmt.Fprintf(&b, "# Written by agent-office for one sandboxed agent (ADR 0049).\n")
	fmt.Fprintf(&b, "model_provider = %s\n", q("agent-office"))
	fmt.Fprintf(&b, "chatgpt_base_url = %s\n", q(s.BackendOrigin+"/backend-api/"))
	fmt.Fprintf(&b, "disable_paste_burst = true\n\n")
	fmt.Fprintf(&b, "[model_providers.agent-office]\nname = %s\nbase_url = %s\nwire_api = \"responses\"\nrequires_openai_auth = true\n\n",
		q("agent-office"), q(s.BackendOrigin+"/backend-api/codex"))
	b.WriteString("[features]\nplugins = false\napps = false\nplugin_discovery_prefetch = false\nremote_plugin = false\n" +
		"daemon_auto_start = false\nin_app_updates = false\n\n[analytics]\nenabled = false\n\n")
	fmt.Fprintf(&b, "[projects.%s]\ntrust_level = \"trusted\"\n\n", q(s.Workspace))
	fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = %s\nargs = [\"mcp\"]\nenv = { %s = %s }\n\n", mcp.ServerName, q(s.MCPCommand),
		mcp.SocketEnv, q(s.MCPSocket))
	names := make([]string, 0, len(s.Extra))
	for n := range s.Extra {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		x := s.Extra[n]
		switch {
		case n == mcp.ServerName:
			return nil, fmt.Errorf("agent: MCP server name %q is reserved", n)
		case x.Command != "" && x.URL == "":
			args := make([]string, len(x.Args))
			for i, a := range x.Args {
				args[i] = q(a)
			}
			fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n\n", q(n), q(x.Command), strings.Join(args, ", "))
		case x.URL != "" && x.Command == "":
			fmt.Fprintf(&b, "[mcp_servers.%s]\nurl = %s\n\n", q(n), q(x.URL))
		default:
			return nil, fmt.Errorf("agent: MCP server %q needs a command or a URL", n)
		}
	}
	for _, ev := range codexHookEvents {
		fmt.Fprintf(&b, "[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %s\n\n", ev, ev, q(s.HookCommand))
	}
	return []byte(b.String()), nil
}

// CodexContainerSpec describes the Codex command inside a sandboxed worker.
type CodexContainerSpec struct {
	Bin           string // e.g. /opt/codex/codex
	Model         string // optional -m
	InitialPrompt string
	ResumeFrom    string // optional: Codex's own session id to continue (codex resume <id>)
}

var codexSessionRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CodexContainerArgs returns the Codex command line for a sandboxed worker.
// Like ContainerArgs it turns Codex's approvals and sandbox off: acceptable
// only inside agent-office's container (ADR 0007, 0049). Hook trust is
// bypassed because the hooks are agent-office's own, mounted read-only.
func CodexContainerArgs(s CodexContainerSpec) ([]string, error) {
	if s.Bin == "" {
		return nil, errors.New("agent: CodexContainerSpec needs Bin")
	}
	args := []string{s.Bin}
	if s.ResumeFrom != "" {
		if !codexSessionRe.MatchString(s.ResumeFrom) {
			return nil, fmt.Errorf("agent: ResumeFrom must be a Codex session id, got %q", s.ResumeFrom)
		}
		args = append(args, "resume", s.ResumeFrom)
	}
	args = append(args, "--no-alt-screen", "--no-daemon", "--dangerously-bypass-hook-trust",
		"--dangerously-bypass-approvals-and-sandbox")
	if s.Model != "" {
		args = append(args, "-m", s.Model)
	}
	if s.InitialPrompt != "" {
		args = append(args, "--", s.InitialPrompt)
	}
	return args, nil
}

// CodexHookEvents lists the registered hooks, for tests and docs.
func CodexHookEvents() []string { return slices.Clone(codexHookEvents) }
