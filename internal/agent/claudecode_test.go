package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

func TestParseClaudeHookFixtures(t *testing.T) {
	cases := map[string]Event{
		"session_start.json":           {Kind: SessionStarted, Hook: "SessionStart", Detail: "startup"},
		"user_prompt_submit.json":      {Kind: TurnStarted, Hook: "UserPromptSubmit"},
		"pre_tool_use.json":            {Kind: ToolStarted, Hook: "PreToolUse", Tool: "Write"},
		"permission_request.json":      {Kind: PermissionRequested, Hook: "PermissionRequest", Tool: "Write"},
		"notification_permission.json": {Kind: PermissionRequested, Hook: "Notification", Detail: "permission_prompt"},
		"post_tool_use.json":           {Kind: ToolFinished, Hook: "PostToolUse", Tool: "Write"},
		"stop.json":                    {Kind: TurnFinished, Hook: "Stop"},
		"subagent_stop.json":           {Kind: Other, Hook: "SubagentStop", Subagent: true},
		"notification_idle.json":       {Kind: Idle, Hook: "Notification", Detail: "idle_prompt"},
		"session_end.json":             {Kind: SessionEnded, Hook: "SessionEnd", Detail: "prompt_input_exit"},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "claudecode", name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseClaudeHook(raw)
			if err != nil {
				t.Fatal(err)
			}
			want.SessionID = "11111111-2222-4333-8444-555555555555"
			want.TranscriptPath = "/redacted/transcript.jsonl"
			if got != want {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
			// Every fixture puts "SENSITIVE" into free-text fields; none of it may survive.
			if s := fmt.Sprintf("%+v", got); strings.Contains(s, "SENSITIVE") {
				t.Fatalf("sensitive content leaked into event: %s", s)
			}
		})
	}
}

func TestParseClaudeHookRejectsInvalid(t *testing.T) {
	for _, raw := range []string{`not json`, `{}`, `{"hook_event_name":"Stop"}`, `{"session_id":"x"}`,
		`{"hook_event_name":"Stop","session_id":"` + strings.Repeat("a", 65) + `"}`} {
		if _, err := ParseClaudeHook([]byte(raw)); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s: want ErrInvalidPayload, got %v", raw, err)
		}
	}
}

func TestTranscriptPathIsAPathOnly(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.claude/projects/-repo/s.jsonl":   "/home/u/.claude/projects/-repo/s.jsonl",
		"relative/s.jsonl":                         "",
		"/a/s.json":                                "",
		"/a/s\n.jsonl":                             "",
		"/" + strings.Repeat("a", 5000) + ".jsonl": "",
	} {
		if got := transcriptPath(in); got != want {
			t.Errorf("transcriptPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnumDropsFreeText(t *testing.T) {
	ev, err := ParseClaudeHook([]byte(`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"rm -rf / please"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Tool != "" {
		t.Fatalf("free text kept as tool name: %q", ev.Tool)
	}
	ev, _ = ParseClaudeHook([]byte(`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"mcp__team__create_task"}`))
	if ev.Tool != "mcp__team__create_task" {
		t.Fatalf("MCP tool name dropped: %q", ev.Tool)
	}
}

func TestScrubEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", "HOME=/home/u", "CLAUDECODE=1", "CLAUDE_PID=42", "CLAUDE_EFFORT=high",
		"CLAUDE_CODE_MESSAGING_TOKEN=secret", "CLAUDE_CODE_ENTRYPOINT=cli", "AGENT_OFFICE_ADDR=x",
		"TERM=dumb", "CLAUDE_CONFIG_DIR=/home/u/.claude", "ANTHROPIC_API_KEY=k"}
	got := ScrubEnv(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "CLAUDE_CONFIG_DIR=/home/u/.claude", "ANTHROPIC_API_KEY=k"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestCommand(t *testing.T) {
	spec := LaunchSpec{SessionID: "sid", Model: "haiku", WorkDir: "/repo", SettingsPath: "/run/s.json",
		SocketPath: "/run/a.sock", BaseEnv: []string{"PATH=/usr/bin", "CLAUDECODE=1"}}

	cmd, err := ClaudeCode{Bin: "claude"}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"claude", "--session-id", "sid", "--settings", "/run/s.json", "--model", "haiku",
		"--setting-sources", "project,local"}
	if !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("args %v\nwant %v", cmd.Args, wantArgs)
	}
	if cmd.Dir != "/repo" {
		t.Fatalf("dir %q", cmd.Dir)
	}
	if slices.Contains(cmd.Env, "CLAUDECODE=1") || !slices.Contains(cmd.Env, SocketEnv+"=/run/a.sock") {
		t.Fatalf("env %v", cmd.Env)
	}

	spec.InheritUserSettings = true
	cmd, _ = ClaudeCode{Bin: "claude"}.Command(spec)
	if slices.Contains(cmd.Args, "--setting-sources") {
		t.Fatalf("inherit=true must not restrict setting sources: %v", cmd.Args)
	}

	spec.InitialPrompt = "--not-a-flag: do the task"
	cmd, _ = ClaudeCode{Bin: "claude"}.Command(spec)
	if n := len(cmd.Args); cmd.Args[n-2] != "--" || cmd.Args[n-1] != spec.InitialPrompt {
		t.Fatalf("initial prompt must follow --: %v", cmd.Args)
	}

	spec.InitialPrompt = "do it"
	spec.MCPConfigPath = "/run/mcp.json"
	cmd, _ = ClaudeCode{Bin: "claude"}.Command(spec)
	if i := slices.Index(cmd.Args, "--mcp-config"); i < 0 || cmd.Args[i+1] != "/run/mcp.json" ||
		slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Fatalf("inherit=true: team MCP config without --strict-mcp-config: %v", cmd.Args)
	}
	spec.InheritUserSettings = false
	cmd, _ = ClaudeCode{Bin: "claude"}.Command(spec)
	if !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Fatalf("inherit=false must also drop the user's MCP servers: %v", cmd.Args)
	}
	if n := len(cmd.Args); cmd.Args[n-2] != "--" || cmd.Args[n-1] != "do it" {
		t.Fatalf("variadic --mcp-config must not swallow the prompt: %v", cmd.Args)
	}

	if _, err := (ClaudeCode{Bin: "claude"}).Command(LaunchSpec{}); err == nil {
		t.Fatal("empty spec accepted")
	}
}

// A resume continues the old conversation under the daemon's new session id
// (ADR 0035): --fork-session is what lets --session-id go with --resume.
func TestCommandResume(t *testing.T) {
	old := "3cac7f56-8774-4ca6-84d9-5a9d3711db6f"
	spec := LaunchSpec{SessionID: "65426929-28b2-4399-b44a-6792d0f13a42", WorkDir: "/repo", SettingsPath: "/run/s.json",
		SocketPath: "/run/a.sock", InheritUserSettings: true, ResumeFrom: old, InitialPrompt: "continue"}
	cmd, err := ClaudeCode{Bin: "claude"}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--resume", old, "--fork-session", "--session-id", spec.SessionID, "--settings", "/run/s.json",
		"--", "continue"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args %v\nwant %v", cmd.Args, want)
	}
	for _, bad := range []string{"--help", "not-a-uuid", old + " x"} {
		spec.ResumeFrom = bad
		if _, err := (ClaudeCode{Bin: "claude"}).Command(spec); err == nil {
			t.Errorf("ResumeFrom %q accepted", bad)
		}
	}
}

func TestMCPConfig(t *testing.T) {
	raw, err := MCPConfig("/opt/agent office/agent-office", "/run/user/1000/agent-office/agents/a.mcp.sock", nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	s, ok := doc.MCPServers[mcp.ServerName]
	if !ok || len(doc.MCPServers) != 1 || s.Type != "stdio" || s.Command != "/opt/agent office/agent-office" ||
		!slices.Equal(s.Args, []string{"mcp"}) || s.Env[mcp.SocketEnv] != "/run/user/1000/agent-office/agents/a.mcp.sock" {
		t.Fatalf("config %s", raw)
	}
	if _, err := MCPConfig("agent-office", "/a.sock", nil); err == nil {
		t.Fatal("relative command accepted")
	}
}

func TestMCPConfigWithPackServers(t *testing.T) {
	raw, err := MCPConfig("/opt/agent-office/agent-office", "/run/mcp.sock", map[string]MCPServer{
		"tool": {Command: "/usr/local/bin/tool", Args: []string{"mcp"}},
		"docs": {URL: "https://docs.example.com/mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.MCPServers) != 3 || doc.MCPServers["tool"]["type"] != "stdio" || doc.MCPServers["tool"]["command"] != "/usr/local/bin/tool" ||
		doc.MCPServers["docs"]["type"] != "http" || doc.MCPServers["docs"]["url"] != "https://docs.example.com/mcp" ||
		doc.MCPServers[mcp.ServerName]["command"] != "/opt/agent-office/agent-office" {
		t.Fatalf("config %s", raw)
	}
	for name, s := range map[string]MCPServer{mcp.ServerName: {Command: "/bin/x"}, "both": {Command: "/bin/x", URL: "https://a.b"}, "none": {}} {
		if _, err := MCPConfig("/x", "/s", map[string]MCPServer{name: s}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestHookSettings(t *testing.T) {
	raw, err := HookSettings(ShellQuote("/opt/agent office/agent-office")+" hook", []string{"Write"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"SessionStart", "PermissionRequest", "Stop", "SessionEnd"} {
		h := doc.Hooks[e]
		if len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Type != "command" {
			t.Fatalf("%s: %+v", e, h)
		}
		if got := h[0].Hooks[0].Command; got != `'/opt/agent office/agent-office' hook` {
			t.Fatalf("%s command = %q", e, got)
		}
	}
	var perms struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &perms); err != nil || !slices.Equal(perms.Permissions.Allow, []string{"mcp__agent-office"}) {
		t.Fatalf("only the team tools are pre-allowed: %+v %v", perms, err)
	}
	if !slices.Equal(perms.Permissions.Deny, []string{"Write"}) {
		t.Fatalf("a role's deny rules: %+v", perms)
	}
	raw, _ = SandboxSettings("/opt/agent-office/agent-office hook", nil)
	if strings.Contains(string(raw), `"deny"`) {
		t.Fatalf("no deny rules, no deny key: %s", raw)
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}

// A Bash call's run_in_background is the one tool input read (ADR 0064).
func TestParseClaudeHookBackgroundBash(t *testing.T) {
	for _, c := range []struct {
		payload string
		want    bool
	}{
		{`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash","tool_input":{"command":"./check.sh","run_in_background":true}}`, true},
		{`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"}}`, false},
		{`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash","tool_input":{"run_in_background":"yes"}}`, false},
		{`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Task","tool_input":{"run_in_background":true}}`, false},
		{`{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash","tool_input":{"run_in_background":true}}`, false},
		{`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash","tool_input":"odd"}`, false},
	} {
		ev, err := ParseClaudeHook([]byte(c.payload))
		if err != nil || ev.Background != c.want {
			t.Errorf("%s: background %v, err %v; want %v", c.payload, ev.Background, err, c.want)
		}
	}
}
