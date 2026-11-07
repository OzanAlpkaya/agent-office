//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

type teamTask struct {
	taskInfo
	ParentTaskID string `json:"parent_task_id"`
	CreatedBy    string `json:"created_by"`
}

type teamAgent struct {
	agentInfo
	Role       string      `json:"role"`
	Sandbox    string      `json:"sandbox"`
	LastImport *importInfo `json:"last_import"`
}

type message struct {
	ID          int64  `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Body        string `json:"body"`
	DeliveredAt string `json:"delivered_at"`
	Delivery    string `json:"delivery"`
}

// TestTeamEndToEnd is the Phase 4 DoD with real Claude Code (Phase 4): given a
// goal, a sandboxed lead creates and assigns sub-tasks through the team MCP
// server; sandboxed workers commit, report_done (their branches are imported)
// and end; their reports reach the lead as messages, each delivered once; the
// lead reports the goal. Nothing in it needs a human, so no access request is
// made; the approval path is covered by the engine and API tests.
//
// Like TestSandboxEndToEnd it works on a throwaway clone of AO_E2E_REPO and
// needs podman, the worker image and the OAuth token file.
func TestTeamEndToEnd(t *testing.T) {
	src := os.Getenv("AO_E2E_REPO")
	if src == "" {
		t.Skip("AO_E2E_REPO not set; this test runs real Claude Code sessions")
	}
	for _, bin := range []string{"claude", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	tokenPath := os.Getenv(sandbox.TokenFileEnv)
	if tokenPath == "" {
		var err error
		if tokenPath, err = sandbox.DefaultTokenFile(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sandbox.ReadTokenFile(tokenPath); err != nil {
		t.Skipf("no usable OAuth token file: %v", err)
	}
	model := os.Getenv("AO_E2E_MODEL")
	if model == "" {
		model = "haiku"
	}
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", data).Run() })
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", gitOut(t, src, "rev-parse", "--show-toplevel"), repo).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	bin := filepath.Join(tmp, "agent-office")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--repo", repo, "--addr", "127.0.0.1:" + port, "--data-dir", data,
			"--model", model, "--max-agents", "3", "--log-level", "debug", "--sandbox", "podman"}}
	listAgents := func() []teamAgent {
		var list []teamAgent
		d.json("GET", "/api/agents", nil, 200, &list)
		return list
	}
	t.Cleanup(func() {
		var ids []string
		if d.cmd != nil {
			for _, a := range listAgents() {
				ids = append(ids, a.ID)
				d.do("POST", "/api/agents/"+a.ID+"/stop", nil)
			}
		}
		d.signal(syscall.SIGTERM)
		for _, id := range ids {
			_ = exec.Command("podman", "rm", "-f", "ao-"+id).Run()
		}
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 120))
			}
		}
	})
	d.start()

	var goal taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E team: two note files",
		"description": "Split this goal into exactly two sub-tasks and assign both of them right away:\n" +
			"1. Create a file named team-a.txt containing the single word alpha, and commit it.\n" +
			"2. Create a file named team-b.txt containing the single word beta, and commit it.\n" +
			"Each sub-task description must name the file and its content, and tell the worker to commit it " +
			"and then call report_done. When both workers have reported, call report_done for the goal."}, 201, &goal)
	var lead teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": goal.ID, "role": "lead"}, 201, &lead)
	if lead.Role != "lead" || lead.Sandbox != "podman" {
		t.Fatalf("lead %+v", lead)
	}

	// The goal is done when the lead has reported it and ended.
	deadline := time.Now().Add(12 * time.Minute)
	for {
		var tasks []teamTask
		d.json("GET", "/api/tasks", nil, 200, &tasks)
		var g teamTask
		for _, task := range tasks {
			if task.ID == goal.ID {
				g = task
			}
		}
		agents := listAgents()
		leadNow := agents[slices.IndexFunc(agents, func(a teamAgent) bool { return a.ID == lead.ID })]
		if g.Status == "review" && leadNow.State == "exited" {
			break
		}
		if leadNow.State == "exited" && g.Status != "review" {
			t.Fatalf("the lead exited (%s) without reporting the goal (%s)", leadNow.ExitReason, g.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: goal %s, lead %s; agents %+v", g.Status, leadNow.State, agents)
		}
		time.Sleep(3 * time.Second)
	}

	var tasks []teamTask
	d.json("GET", "/api/tasks", nil, 200, &tasks)
	var subs []teamTask
	for _, task := range tasks {
		if task.ParentTaskID == goal.ID {
			subs = append(subs, task)
			if task.CreatedBy != lead.ID {
				t.Errorf("sub-task %q created by %q, want the lead", task.Title, task.CreatedBy)
			}
		}
	}
	if len(subs) < 2 {
		t.Fatalf("the lead created %d sub-tasks: %+v", len(subs), subs)
	}
	for _, s := range subs {
		t.Logf("sub-task %s %q: %s", s.ID, s.Title, s.Status)
	}
	agents := listAgents()
	files := map[string]string{}
	workers := 0
	for _, a := range agents {
		if a.Role != "worker" {
			continue
		}
		workers++
		if a.State != "exited" || a.ExitReason != "reported_done" {
			t.Errorf("worker %s: %s (%s), want exited after report_done", a.ID, a.State, a.ExitReason)
		}
		t.Logf("worker %s on task %s: %s (%s), import %+v", a.ID, a.TaskID, a.State, a.ExitReason, a.LastImport)
		if a.LastImport == nil || a.LastImport.Commits < 1 {
			t.Errorf("worker %s: import %+v", a.ID, a.LastImport)
			continue
		}
		for _, name := range []string{"team-a.txt", "team-b.txt"} {
			if out, err := exec.Command("git", "-C", repo, "show", a.LastImport.Branch+":"+name).Output(); err == nil {
				files[name] = strings.TrimSpace(string(out))
			}
		}
		if len(a.LastImport.Flagged) != 0 {
			t.Errorf("worker %s: flagged commits %+v", a.ID, a.LastImport.Flagged)
		}
	}
	if workers < 2 {
		t.Fatalf("%d workers ran", workers)
	}
	if files["team-a.txt"] != "alpha" || files["team-b.txt"] != "beta" {
		t.Errorf("files on the imported branches: %v", files)
	}
	for _, s := range subs {
		if s.Status != "review" {
			t.Errorf("sub-task %q is %s, want review", s.Title, s.Status)
		}
	}

	// Each worker's report reached the lead, delivered once (at a Stop or typed
	// while the lead was idle).
	var msgs []message
	d.json("GET", "/api/agents/"+lead.ID+"/messages", nil, 200, &msgs)
	reports := 0
	for _, m := range msgs {
		if m.To != lead.ID || !strings.HasPrefix(m.Body, "Report from worker") {
			continue
		}
		reports++
		t.Logf("report %d from %s: delivered %s via %s", m.ID, m.From, m.DeliveredAt, m.Delivery)
		if m.DeliveredAt == "" || (m.Delivery != "stop_hook" && m.Delivery != "pty") {
			t.Errorf("report %d not delivered: %+v", m.ID, m)
		}
	}
	if reports < 2 {
		t.Errorf("%d reports reached the lead: %+v", reports, msgs)
	}

	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		switch {
		case strings.Contains(line, "api request refused"):
			t.Errorf("API socket refused a request: %s", line)
		case strings.Contains(line, "team tool refused"):
			t.Logf("refused team call (the model corrected itself): %s", line)
		case strings.Contains(line, "team tool failed"):
			t.Errorf("team tool failed: %s", line)
		case strings.Contains(line, "msg=\"team tool\""), strings.Contains(line, "message delivered"),
			strings.Contains(line, "ending agent after its final tool call"), strings.Contains(line, "msg=\"agent started\""):
			t.Log(line)
		}
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the user's checkout changed: %q", st)
	}
}
