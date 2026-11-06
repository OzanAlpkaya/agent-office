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

	if _, err := (ClaudeCode{Bin: "claude"}).Command(LaunchSpec{}); err == nil {
		t.Fatal("empty spec accepted")
	}
}

func TestHookSettings(t *testing.T) {
	raw, err := HookSettings(ShellQuote("/opt/agent office/agent-office") + " hook")
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
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}
