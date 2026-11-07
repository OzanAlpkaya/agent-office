package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestContainerArgs(t *testing.T) {
	args, err := ContainerArgs(ContainerSpec{Bin: "/opt/claude/claude", SessionID: "sid", Model: "haiku",
		SettingsPath: "/opt/agent-office/settings.json", InitialPrompt: "-rf looks like a flag"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/claude/claude", "--session-id", "sid", "--settings", "/opt/agent-office/settings.json",
		"--model", "haiku", "--dangerously-skip-permissions", "--", "-rf looks like a flag"}
	if !slices.Equal(args, want) {
		t.Fatalf("args %q", args)
	}
	if _, err := ContainerArgs(ContainerSpec{Bin: "/opt/claude/claude"}); err == nil {
		t.Fatal("incomplete spec accepted")
	}
}

// Host agents never run without permission prompts (ADR 0007), whatever the
// spec contains.
func TestHostCommandNeverBypassesPermissions(t *testing.T) {
	for _, spec := range []LaunchSpec{
		{SessionID: "s", SettingsPath: "/s.json", SocketPath: "/a.sock", WorkDir: "/w"},
		{SessionID: "s", SettingsPath: "/s.json", SocketPath: "/a.sock", WorkDir: "/w", Model: "opus",
			InheritUserSettings: true, InitialPrompt: "--dangerously-skip-permissions"},
	} {
		cmd, err := ClaudeCode{Bin: "claude"}.Command(spec)
		if err != nil {
			t.Fatal(err)
		}
		args := cmd.Args
		if i := slices.Index(args, "--"); i >= 0 {
			args = args[:i] // the prompt is data, not a flag
		}
		for _, a := range args {
			if strings.Contains(a, "dangerously") || strings.Contains(a, "bypass") {
				t.Fatalf("host command has %q: %q", a, cmd.Args)
			}
		}
	}
}

func TestSandboxSettingsAndSeed(t *testing.T) {
	b, err := SandboxSettings("/opt/agent-office/agent-office hook")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks                             map[string][]struct{ Hooks []struct{ Command string } }
		Theme                             string
		SkipDangerousModePermissionPrompt bool
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Theme == "" || !s.SkipDangerousModePermissionPrompt || len(s.Hooks) != len(claudeHookEvents) ||
		s.Hooks["SessionStart"][0].Hooks[0].Command != "/opt/agent-office/agent-office hook" {
		t.Fatalf("sandbox settings %s", b)
	}
	host, _ := HookSettings("x hook")
	if strings.Contains(string(host), "skipDangerous") {
		t.Fatal("host settings must not skip the bypass warning")
	}

	seed, err := SeedConfig("2.1.319", "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		HasCompletedOnboarding bool
		LastOnboardingVersion  string
		Projects               map[string]struct{ HasTrustDialogAccepted bool }
	}
	if err := json.Unmarshal(seed, &c); err != nil || !c.HasCompletedOnboarding || c.LastOnboardingVersion != "2.1.319" ||
		!c.Projects["/workspace"].HasTrustDialogAccepted || len(c.Projects) != 1 {
		t.Fatalf("seed %s %v", seed, err)
	}
	if _, err := SeedConfig("", "/workspace"); err == nil {
		t.Fatal("seed without version accepted")
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{"2.1.319 (Claude Code)\n": "2.1.319", "10.0.1": "10.0.1"} {
		if got, err := ParseVersion(in); err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "Claude Code", "v2.1", "2.1.319x"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}
