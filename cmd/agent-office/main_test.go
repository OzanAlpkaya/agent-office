package main

import (
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	pricing "github.com/OzanAlpkaya/agent-office/internal/usage"
)

func TestParseServe(t *testing.T) {
	t.Setenv("AGENT_OFFICE_INHERIT_USER_SETTINGS", "")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != "127.0.0.1:7680" || !c.inheritUserSettings || c.repo != "/tmp" || c.sandbox != modePodman ||
		c.sandboxMemory != "2g" || c.sandboxPids != 256 || len(c.egressAllow) != 0 {
		t.Fatalf("defaults: %+v", c)
	}

	for _, args := range [][]string{
		{}, // no repo
		{"--repo", "/tmp", "--addr", "0.0.0.0:7680"}, // not loopback
		{"--repo", "/tmp", "--addr", "192.168.1.5:7680"},
		{"--repo", "/tmp", "--addr", "nonsense"},
		{"--repo", "/tmp", "--sandbox", "docker"},
		{"--repo", "/tmp", "--sandbox", ""},
		{"--repo", "/tmp", "--egress-allow", "*.example.com"},
		{"--repo", "/tmp", "--egress-allow", "10.0.0.1"},
		{"--repo", "/tmp", "--sandbox-pids", "1"},
		{"--repo", "/tmp", "--reviewer", "gpt"},
		{"--repo", "/tmp", "--review-rounds", "6"},
		{"--repo", "/tmp", "--review-rounds", "-1"},
		{"--repo", "/tmp", "--github", "--sandbox", "host"},
	} {
		if _, err := parseServe(args); err == nil {
			t.Errorf("parseServe(%v) accepted", args)
		}
	}
	for _, n := range []string{"0", "33"} {
		if _, err := parseServe([]string{"--repo", "/tmp", "--max-agents", n}); err == nil {
			t.Errorf("--max-agents %s accepted", n)
		}
	}
	t.Setenv("XDG_DATA_HOME", "/xdg")
	if c, _ := parseServe([]string{"--repo", "/tmp"}); c.dataDir != "/xdg/agent-office" || c.maxAgents != 4 {
		t.Errorf("data dir / max agents defaults: %+v", c)
	}
	if _, err := parseServe([]string{"--repo", "/tmp", "--addr", "[::1]:7680"}); err != nil {
		t.Errorf("IPv6 loopback rejected: %v", err)
	}
}

func TestGitHubFlags(t *testing.T) {
	for _, k := range []string{"AGENT_OFFICE_GITHUB", "AGENT_OFFICE_GITHUB_REMOTE", "AGENT_OFFICE_REVIEWER", "AGENT_OFFICE_REVIEW_ROUNDS"} {
		t.Setenv(k, "")
	}
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil || c.github || c.githubRemote != "origin" || c.reviewer != "internal" || c.reviewRounds != 2 {
		t.Fatalf("defaults (GitHub is opt-in): %+v %v", c, err)
	}
	t.Setenv("AGENT_OFFICE_GITHUB", "true")
	t.Setenv("AGENT_OFFICE_REVIEWER", "coderabbit")
	t.Setenv("AGENT_OFFICE_REVIEW_ROUNDS", "0")
	c, err = parseServe([]string{"--repo", "/tmp", "--github-remote", "upstream"})
	if err != nil || !c.github || c.githubRemote != "upstream" || c.reviewer != "coderabbit" || c.reviewRounds != 0 {
		t.Fatalf("env and flags: %+v %v", c, err)
	}
}

func TestSandboxFlagsFromEnv(t *testing.T) {
	t.Setenv("AGENT_OFFICE_SANDBOX", "host")
	t.Setenv("AGENT_OFFICE_EGRESS_ALLOW", "pypi.org, .pythonhosted.org,")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil || c.sandbox != modeHost || strings.Join(c.egressAllow, "|") != "pypi.org|.pythonhosted.org" {
		t.Fatalf("env not applied: %+v %v", c, err)
	}
}

func TestVersionWarnings(t *testing.T) {
	if w := versionWarnings(claudeInfo{version: agent.TestedClaudeCodeVersion}, modePodman); len(w) != 0 {
		t.Fatalf("tested version warned: %q", w)
	}
	if w := versionWarnings(claudeInfo{version: "9.9.9"}, modePodman); len(w) != 1 || !strings.Contains(w[0], "9.9.9") {
		t.Fatalf("untested version: %q", w)
	}
	if w := versionWarnings(claudeInfo{version: "9.9.9"}, modeHost); len(w) != 0 {
		t.Fatalf("host mode does not use the seeded config: %q", w)
	}
}

func TestIsStatic(t *testing.T) {
	if ok, err := isStatic("/bin/sh"); err != nil || ok {
		t.Fatalf("/bin/sh is dynamically linked: %v %v", ok, err)
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

// Without a daemon the MCP bridge fails fast and non-zero, so Claude Code
// reports the server as failed instead of waiting.
func TestMCPBridgeFailsWhenDaemonDown(t *testing.T) {
	t.Setenv("AGENT_OFFICE_MCP_SOCKET", t.TempDir()+"/missing.sock")
	start := time.Now()
	if code := mcpBridge(); code == 0 {
		t.Fatal("exit code 0 with the daemon down")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v with the daemon down", d)
	}
	t.Setenv("AGENT_OFFICE_MCP_SOCKET", "")
	if code := mcpBridge(); code == 0 {
		t.Fatal("exit code 0 without socket env")
	}
}

func TestObservabilityFlags(t *testing.T) {
	for _, k := range []string{"AGENT_OFFICE_BUDGET_SESSION_USD", "AGENT_OFFICE_BUDGET_GOAL_USD", "AGENT_OFFICE_EVENT_RETENTION_DAYS",
		"AGENT_OFFICE_LOG_FORMAT", "AGENT_OFFICE_PRICES_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil || c.budgetSessionUSD != 0 || c.budgetGoalUSD != 0 || c.eventRetentionDays != 30 || c.logFormat != "text" ||
		c.pricesFile != "/cfg/agent-office/prices.yaml" || c.pricesRequired {
		t.Fatalf("defaults (budgets off, optional price file): %+v %v", c, err)
	}
	t.Setenv("AGENT_OFFICE_BUDGET_GOAL_USD", "2.5")
	t.Setenv("AGENT_OFFICE_LOG_FORMAT", "json")
	c, err = parseServe([]string{"--repo", "/tmp", "--budget-session-usd", "0.5", "--prices", "/p/prices.yaml", "--event-retention-days", "0"})
	if err != nil || c.budgetSessionUSD != 0.5 || c.budgetGoalUSD != 2.5 || c.logFormat != "json" || !c.pricesRequired ||
		c.pricesFile != "/p/prices.yaml" || c.eventRetentionDays != 0 {
		t.Fatalf("flags and env: %+v %v", c, err)
	}
	for _, args := range [][]string{
		{"--budget-session-usd", "-1"},
		{"--budget-goal-usd", "NaN"},
		{"--log-format", "xml"},
		{"--event-retention-days", "-1"},
		{"--budget-goal-usd", "0.0000001"},
	} {
		if _, err := parseServe(append([]string{"--repo", "/tmp"}, args...)); err == nil {
			t.Errorf("parseServe(%v) accepted", args)
		}
	}
	t.Setenv("AGENT_OFFICE_BUDGET_GOAL_USD", "lots")
	if _, err := parseServe([]string{"--repo", "/tmp"}); err == nil {
		t.Error("bad env budget accepted")
	}
}

func TestCheckBudgetModel(t *testing.T) {
	p := pricing.Builtin()
	on := engine.Budgets{GoalMicroUSD: 1_000_000}
	if err := checkBudgetModel(on, "claude-unknown-9", p); err == nil {
		t.Error("unpriced full model id accepted with a budget")
	}
	for _, m := range []string{"claude-haiku-5-5", "claude-haiku-4-5-20251001", "haiku", ""} {
		if err := checkBudgetModel(on, m, p); err != nil {
			t.Errorf("%q: %v", m, err)
		}
	}
	if err := checkBudgetModel(engine.Budgets{}, "claude-unknown-9", p); err != nil {
		t.Errorf("no budget: %v", err)
	}
}
