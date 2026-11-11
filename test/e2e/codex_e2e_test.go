//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// codexReady skips unless the Codex CLI and a ChatGPT login are there.
func codexReady(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not on PATH")
	}
	path, err := sandbox.DefaultCodexAuthFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.ReadCodexLogin(path); err != nil {
		t.Skipf("no usable Codex login: %v", err)
	}
}

// TestCodexTeamEndToEnd is the Phase 15 DoD with the real CLIs: in a
// sandboxed team, a Claude Code Tech Lead (haiku) assigns a sub-task to a
// Codex worker (the ChatGPT plan's default model), which runs in a
// network-less worker with a placeholder login, commits, reports through the
// team MCP tools, and its branch is imported. Its usage is OpenAI's.
func TestCodexTeamEndToEnd(t *testing.T) {
	codexReady(t)
	d, repo, listAgents := teamDaemon(t, "--max-agents", "3", "--qa=false", "--role-cli", "worker=codex")

	var goal taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E Codex worker: one note file",
		"description": "Create exactly one sub-task and assign it right away: create a file named codex-e2e.txt containing " +
			"the single word codex, and commit it. The sub-task description must name the file and its content, and tell " +
			"the worker to commit it and then call report_done. When the worker has reported, call report_done for the goal."},
		201, &goal)
	var lead teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": goal.ID, "role": "tech-lead"}, 201, &lead)

	deadline := time.Now().Add(15 * time.Minute)
	for {
		var tasks []teamTask
		d.json("GET", "/api/tasks", nil, 200, &tasks)
		l := findAgent(listAgents(), lead.ID)
		done := false
		for _, tk := range tasks {
			if tk.ID == goal.ID && tk.Status == "review" && l.State == "idle" {
				done = true
			}
		}
		if done {
			break
		}
		if l.State == "exited" {
			t.Fatalf("the lead exited (%s)", l.ExitReason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout; agents %+v", listAgents())
		}
		time.Sleep(3 * time.Second)
	}
	var worker teamAgent
	for _, a := range listAgents() {
		if a.Role == "worker" {
			worker = a
		}
	}
	var all []struct {
		ID  string `json:"id"`
		CLI string `json:"cli"`
	}
	d.json("GET", "/api/agents", nil, 200, &all)
	for _, a := range all {
		if want := map[string]string{worker.ID: "codex", lead.ID: "claude"}[a.ID]; want != "" && a.CLI != want {
			t.Errorf("agent %s runs on %q, want %s", a.ID, a.CLI, want)
		}
	}
	if worker.ID == "" {
		t.Fatal("no worker ran")
	}
	if worker.State != "exited" || worker.ExitReason != "reported_done" || worker.LastImport == nil || worker.LastImport.Commits < 1 {
		t.Fatalf("Codex worker %+v", worker)
	}
	out, err := exec.Command("git", "-C", repo, "show", worker.LastImport.Branch+":codex-e2e.txt").Output()
	if err != nil || strings.TrimSpace(string(out)) != "codex" {
		t.Fatalf("codex-e2e.txt on %s: %q %v", worker.LastImport.Branch, out, err)
	}
	var rep struct {
		Vendors []struct {
			Vendor   string `json:"vendor"`
			Messages int    `json:"messages"`
			Tokens   struct {
				Input, Output int64
			} `json:"tokens"`
			UnpricedTokens int64 `json:"unpriced_tokens"`
		} `json:"vendors"`
	}
	d.json("GET", "/api/usage", nil, 200, &rep)
	found := false
	for _, v := range rep.Vendors {
		t.Logf("usage of %s: %d messages, %d input / %d output tokens, %d unpriced", v.Vendor, v.Messages, v.Tokens.Input,
			v.Tokens.Output, v.UnpricedTokens)
		if v.Vendor == "openai" && v.Messages > 0 && v.Tokens.Output > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("no OpenAI usage metered: %+v", rep.Vendors)
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		switch {
		case strings.Contains(line, "api request refused"):
			t.Logf("refused on an API socket: %s", line)
		case strings.Contains(line, "team tool failed"):
			t.Errorf("team tool failed: %s", line)
		case strings.Contains(line, "Codex ready"), strings.Contains(line, "msg=\"team tool\""):
			t.Log(line)
		}
	}
	d.json("PATCH", "/api/tasks/"+goal.ID, map[string]string{"status": "done"}, 200, nil)

	// Restart: a Codex worker idle after a turn comes back after a SIGKILL,
	// continuing its own Codex session (ADR 0035, 0049).
	var solo taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E Codex restore",
		"description": "Remember the codeword PLUM73. Reply with the single word OK and do nothing else."}, 201, &solo)
	var w teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": solo.ID, "role": "worker"}, 201, &w)
	idle := waitAgent(t, listAgents, w.ID, 5*time.Minute, func(a teamAgent) bool { return a.State == "idle" })
	d.signal(syscall.SIGKILL)
	d.start()
	back := waitAgent(t, listAgents, w.ID, 3*time.Minute, func(a teamAgent) bool {
		return a.HasTerminal && a.SessionID != idle.SessionID && a.ResumedFrom != ""
	})
	waitAgent(t, listAgents, w.ID, 3*time.Minute, func(a teamAgent) bool { return a.State == "idle" })
	t.Logf("Codex worker %s restored: session %s resumed from %s", w.ID, back.SessionID, back.ResumedFrom)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tm := attach(t, ctx, d.base, w.ID)
	time.Sleep(2 * time.Second)
	tm.typeLine(ctx, "What was the codeword? Reply with it only.")
	deadline = time.Now().Add(3 * time.Minute)
	for !strings.Contains(tm.text(), "PLUM73") {
		if time.Now().After(deadline) {
			t.Fatalf("the restored Codex worker does not recall its conversation:\n%s", tail(tm.text(), 40))
		}
		time.Sleep(2 * time.Second)
	}
	t.Log("the restored Codex worker recalled the codeword")
	d.do("POST", "/api/agents/"+w.ID+"/stop", nil)
}
