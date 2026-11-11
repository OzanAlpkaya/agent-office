package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCodexHook(t *testing.T) {
	sid := "01a1c84a-492a-71a3-83fe-b4c1725fa1ab"
	for payload, want := range map[string]Event{
		`{"hook_event_name":"SessionStart","session_id":"` + sid + `","source":"resume","model":"gpt-6.1-sol"}`: {Kind: SessionStarted, Detail: "resume"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"` + sid + `","prompt":"secret plans"}`:             {Kind: TurnStarted},
		`{"hook_event_name":"PreToolUse","session_id":"` + sid + `","tool_name":"Bash","tool_input":{"command":"rm"}}`: {
			Kind: ToolStarted, Tool: "Bash"},
		`{"hook_event_name":"PostToolUse","session_id":"` + sid + `","tool_name":"Bash"}`:                    {Kind: ToolFinished, Tool: "Bash"},
		`{"hook_event_name":"PermissionRequest","session_id":"` + sid + `"}`:                                 {Kind: PermissionRequested},
		`{"hook_event_name":"Stop","session_id":"` + sid + `","last_assistant_message":"done"}`:              {Kind: TurnFinished},
		`{"hook_event_name":"Interrupt","session_id":"` + sid + `"}`:                                         {Kind: UserInterrupted},
		`{"hook_event_name":"SessionEnd","session_id":"` + sid + `","reason":"other"}`:                       {Kind: SessionEnded, Detail: "other"},
		`{"hook_event_name":"PostModelRequest","session_id":"` + sid + `","token_usage":{"input_tokens":1}}`: {Kind: Other},
	} {
		ev, err := ParseCodexHook([]byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		if ev.Kind != want.Kind || ev.Detail != want.Detail || ev.Tool != want.Tool || ev.SessionID != sid {
			t.Errorf("%s: %+v", payload, ev)
		}
		if strings.Contains(ev.Detail+ev.Tool+ev.Hook, "secret") {
			t.Errorf("free text reached the event: %+v", ev)
		}
	}
	for _, bad := range []string{`{}`, `{"hook_event_name":"Stop"}`, `not json`, `{"hook_event_name":"Stop","session_id":"a b"}`} {
		if _, err := ParseCodexHook([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestCodexPlaceholder(t *testing.T) {
	auth, bearer, err := CodexPlaceholder("11111111-2222-4333-8444-555555555555", "plus")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			Access, Refresh, Account string
			AccessToken              string `json:"access_token"`
			RefreshToken             string `json:"refresh_token"`
			AccountID                string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(auth, &doc); err != nil || doc.AuthMode != "chatgpt" || doc.Tokens.AccessToken != bearer ||
		doc.Tokens.AccountID != "11111111-2222-4333-8444-555555555555" || !strings.Contains(doc.Tokens.RefreshToken, "not-a-credential") {
		t.Fatalf("placeholder %s %v", auth, err)
	}
	parts := strings.Split(bearer, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct{ Exp int64 }
	if json.Unmarshal(claims, &c) != nil || c.Exp < 4_000_000_000 {
		t.Fatalf("the placeholder must never look expired: %s", claims)
	}
	again, bearer2, _ := CodexPlaceholder("11111111-2222-4333-8444-555555555555", "plus")
	if string(again) != string(auth) || bearer2 != bearer {
		t.Fatal("the placeholder is not stable")
	}
	if _, _, err := CodexPlaceholder("", "plus"); err == nil {
		t.Fatal("no account accepted")
	}
}

func codexSpec() CodexConfigSpec {
	return CodexConfigSpec{BackendOrigin: "https://127.0.0.1:8080", HookCommand: "/opt/agent-office/agent-office hook",
		MCPCommand: "/opt/agent-office/agent-office", MCPSocket: "/run/agent-office/mcp.sock", Workspace: "/workspace",
		Extra: map[string]MCPServer{"docs": {Command: "/usr/bin/docs-mcp", Args: []string{"--stdio"}}}}
}

func TestCodexConfig(t *testing.T) {
	b, err := CodexConfig(codexSpec())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`chatgpt_base_url = "https://127.0.0.1:8080/backend-api/"`,
		`base_url = "https://127.0.0.1:8080/backend-api/codex"`, "requires_openai_auth = true", "plugins = false",
		"daemon_auto_start = false", `[projects."/workspace"]`, `trust_level = "trusted"`, "[mcp_servers.agent-office]",
		`AGENT_OFFICE_MCP_SOCKET = "/run/agent-office/mcp.sock"`, `[mcp_servers."docs"]`, "[[hooks.SessionStart]]",
		"[[hooks.Stop.hooks]]", `command = "/opt/agent-office/agent-office hook"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config misses %q:\n%s", want, s)
		}
	}
	del := codexSpec()
	del.Extra = map[string]MCPServer{"docs": {Command: "/usr/bin/docs-mcp", Args: []string{"a\x7fb"}}}
	if b, _ := CodexConfig(del); strings.ContainsRune(string(b), 0x7f) || !strings.Contains(string(b), `"a\u007fb"`) {
		t.Fatalf("DEL is not escaped:\n%s", b)
	}
	bad := codexSpec()
	bad.BackendOrigin = "http://127.0.0.1:8080"
	if _, err := CodexConfig(bad); err == nil {
		t.Fatal("a plain-HTTP backend accepted: Codex's TUI refuses it")
	}
	bad = codexSpec()
	bad.Extra = map[string]MCPServer{"agent-office": {Command: "/x"}}
	if _, err := CodexConfig(bad); err == nil {
		t.Fatal("the reserved server name accepted")
	}
}

// TestCodexConfigLoadsStrictly checks the generated config against the
// installed Codex CLI: `exec --strict-config` refuses unknown keys before it
// does anything else. The backend is the (closed) loopback port and there is
// no login, so nothing leaves the machine. Skipped where Codex is not
// installed.
func TestCodexConfigLoadsStrictly(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	home, work := t.TempDir(), t.TempDir()
	spec := codexSpec()
	spec.BackendOrigin = "https://127.0.0.1:1" // nothing listens there
	b, _ := CodexConfig(spec)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second) // the config is reported at once; then it retries the closed port
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec", "--strict-config", "--skip-git-repo-check", "-C", work, "x")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "Error loading config") || !strings.Contains(string(out), "provider: agent-office") {
		t.Fatalf("codex refused the generated config:\n%s", out)
	}
}

func TestCodexContainerArgs(t *testing.T) {
	args, err := CodexContainerArgs(CodexContainerSpec{Bin: "/opt/codex/codex", Model: "gpt-6.1-sol", InitialPrompt: "-do it"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if got != "/opt/codex/codex --no-alt-screen --no-daemon --dangerously-bypass-hook-trust --dangerously-bypass-approvals-and-sandbox -m gpt-6.1-sol -- -do it" {
		t.Fatalf("args %q", got)
	}
	args, _ = CodexContainerArgs(CodexContainerSpec{Bin: "/opt/codex/codex", ResumeFrom: "01a1c84a-492a-71a3-83fe-b4c1725fa1ab"})
	if !strings.HasPrefix(strings.Join(args, " "), "/opt/codex/codex resume 01a1c84a-492a-71a3-83fe-b4c1725fa1ab --no-alt-screen") {
		t.Fatalf("resume args %q", args)
	}
	if _, err := CodexContainerArgs(CodexContainerSpec{Bin: "/opt/codex/codex", ResumeFrom: "--evil"}); err == nil {
		t.Fatal("a flag as a session id accepted")
	}
	if v, err := ParseCodexVersion("codex-cli 0.167.1\n"); err != nil || v != "0.167.1" {
		t.Fatalf("version %q %v", v, err)
	}
}
