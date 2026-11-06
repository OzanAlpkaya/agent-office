package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseServe(t *testing.T) {
	t.Setenv("AGENT_OFFICE_INHERIT_USER_SETTINGS", "")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != "127.0.0.1:7680" || !c.inheritUserSettings || c.repo != "/tmp" {
		t.Fatalf("defaults: %+v", c)
	}

	for _, args := range [][]string{
		{}, // no repo
		{"--repo", "/tmp", "--addr", "0.0.0.0:7680"}, // not loopback
		{"--repo", "/tmp", "--addr", "192.168.1.5:7680"},
		{"--repo", "/tmp", "--addr", "nonsense"},
	} {
		if _, err := parseServe(args); err == nil {
			t.Errorf("parseServe(%v) accepted", args)
		}
	}
	if _, err := parseServe([]string{"--repo", "/tmp", "--addr", "[::1]:7680"}); err != nil {
		t.Errorf("IPv6 loopback rejected: %v", err)
	}
}

func TestInheritUserSettingsFromEnv(t *testing.T) {
	t.Setenv("AGENT_OFFICE_INHERIT_USER_SETTINGS", "false")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil || c.inheritUserSettings {
		t.Fatalf("env false not applied: %+v %v", c, err)
	}
	t.Setenv("AGENT_OFFICE_INHERIT_USER_SETTINGS", "maybe")
	if _, err := parseServe([]string{"--repo", "/tmp"}); err == nil || !strings.Contains(err.Error(), "AGENT_OFFICE_INHERIT_USER_SETTINGS") {
		t.Fatalf("invalid bool not reported: %v", err)
	}
}

func TestHookExitsZeroWhenDaemonDown(t *testing.T) {
	t.Setenv("AGENT_OFFICE_SOCKET", t.TempDir()+"/missing.sock")
	start := time.Now()
	if code := hook(); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v with the daemon down", d)
	}
	t.Setenv("AGENT_OFFICE_SOCKET", "")
	if code := hook(); code != 0 {
		t.Fatalf("exit code %d without socket env, want 0", code)
	}
}
