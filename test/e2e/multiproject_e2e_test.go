//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// e2eProject is one project of the multi-project run.
type e2eProject struct {
	ID, Name, Pack, Repo string
	File, Command        string // what its worker writes and with which toolchain command
	goal                 taskInfo
	lead                 teamAgent
}

func (p *e2eProject) path(rest string) string { return "/api/projects/" + p.ID + rest }

// nodeRepo creates a small Node project the node pack's agents work on.
func nodeRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json": `{"name": "e2e-node-app", "version": "1.0.0", "private": true, "type": "module",
  "scripts": {"test": "node --test"}}
`,
		"sum.js":      "export const sum = (a, b) => a + b;\n",
		"sum.test.js": "import { test } from \"node:test\";\nimport assert from \"node:assert\";\nimport { sum } from \"./sum.js\";\n\ntest(\"sum\", () => assert.equal(sum(1, 2), 3));\n",
		"README.md":   "# e2e node app\n\nA tiny Node project for the agent-office multi-project e2e run.\n",
		".gitignore":  "node_modules/\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// TestMultiProjectEndToEnd is the Phase 14 DoD with real Claude Code: one
// daemon, started without --repo, runs two projects added with
// `agent-office project add`, a Go repository with the go pack and a Node
// repository with the node pack. The daemon-wide cap holds across them;
// a sandboxed Tech Lead and worker run in each at the same time, and each
// worker uses its own pack's toolchain; nothing crosses between the
// repositories. After a SIGKILL both projects come back with their Tech
// Leads restored.
func TestMultiProjectEndToEnd(t *testing.T) {
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
	bin := filepath.Join(tmp, "agent-office")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, pk := range []string{"go", "node"} {
		tag, err := exec.Command(bin, "worker-image", "--pack", pk, "--tag").Output()
		if err != nil {
			t.Fatal(err)
		}
		if exec.Command("podman", "image", "exists", strings.TrimSpace(string(tag))).Run() != nil {
			t.Skipf("the %s pack's image %s is missing: make worker-image PACK=%s", pk, strings.TrimSpace(string(tag)), pk)
		}
	}
	goRepo := filepath.Join(tmp, "go-app")
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", gitOut(t, src, "rev-parse", "--show-toplevel"), goRepo).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	projects := []*e2eProject{
		{Name: "go-app", Pack: "go", Repo: goRepo, File: "multi-go.txt", Command: "go version"},
		{Name: "node-app", Pack: "node", Repo: nodeRepo(t, filepath.Join(tmp, "node-app")), File: "multi-node.txt", Command: "node --version"},
	}

	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--addr", "127.0.0.1:" + port, "--data-dir", data, "--model", model, "--log-level", "debug",
			"--sandbox", "podman", "--max-agents", "4", "--qa=false"}}
	agentsOf := func(p *e2eProject) []teamAgent {
		var list []teamAgent
		d.json("GET", p.path("/agents"), nil, 200, &list)
		return list
	}
	t.Cleanup(func() {
		var ids []string
		if d.cmd != nil {
			for _, p := range projects {
				if p.ID == "" {
					continue
				}
				for _, a := range agentsOf(p) {
					ids = append(ids, a.ID)
					d.do("POST", p.path("/agents/"+a.ID+"/stop"), nil)
				}
			}
		}
		d.signal(syscall.SIGTERM)
		for _, id := range ids {
			_ = exec.Command("podman", "rm", "-f", "ao-"+id).Run()
		}
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 150))
			}
		}
	})
	d.start()

	// 1. Projects come from the CLI, over the running daemon's control socket.
	for _, p := range projects {
		out, err := exec.Command(bin, "project", "add", p.Repo, "--name", p.Name, "--pack", p.Pack, "--data-dir", data).CombinedOutput()
		if err != nil {
			t.Fatalf("project add %s: %v %s", p.Name, err, out)
		}
		t.Logf("%s", out)
	}
	var listed []struct {
		ID, Name, State string
		Error           string
		Effective       struct {
			Pack string `json:"pack"`
		} `json:"effective"`
	}
	d.json("GET", "/api/projects", nil, 200, &listed)
	for _, p := range projects {
		for _, l := range listed {
			if l.Name == p.Name {
				p.ID = l.ID
				if l.State != "open" || l.Effective.Pack != p.Pack {
					t.Fatalf("project %s: %+v", p.Name, l)
				}
			}
		}
		if p.ID == "" {
			t.Fatalf("project %s not listed: %+v", p.Name, listed)
		}
		var st struct {
			Image string `json:"image"`
			Pack  *struct {
				Name string `json:"name"`
			} `json:"pack"`
		}
		d.json("GET", p.path("/status"), nil, 200, &st)
		if st.Pack == nil || st.Pack.Name != p.Pack || !strings.Contains(st.Image, "agent-office-pack-"+p.Pack) {
			t.Fatalf("project %s status: %+v", p.Name, st)
		}
	}
	if out, err := exec.Command(bin, "project", "list", "--data-dir", data).CombinedOutput(); err != nil ||
		!strings.Contains(string(out), "go-app") || !strings.Contains(string(out), "node-app") {
		t.Fatalf("project list: %v %s", err, out)
	}

	// 2. The daemon-wide cap: two idle agents in each project (no task, so no
	// prompt and no tokens) fill --max-agents 4; a fifth start is refused in
	// both projects, and a slot freed in one is usable in the other.
	var plain []string
	for _, p := range projects {
		for range 2 {
			var a teamAgent
			d.json("POST", p.path("/agents"), nil, 201, &a)
			plain = append(plain, a.ID)
		}
	}
	for _, p := range projects {
		code, body := d.do("POST", p.path("/agents"), nil)
		if code != http.StatusConflict || !strings.Contains(string(body), "daemon-wide cap of 4") {
			t.Fatalf("a fifth agent in %s: %d %s", p.Name, code, body)
		}
	}
	d.json("POST", projects[1].path("/agents/"+plain[2]+"/stop"), nil, 200, nil)
	waitAgent(t, func() []teamAgent { return agentsOf(projects[1]) }, plain[2], time.Minute,
		func(a teamAgent) bool { return a.State == "exited" })
	var extra teamAgent
	d.json("POST", projects[0].path("/agents"), nil, 201, &extra)
	plain = append(plain, extra.ID)
	for i, id := range plain {
		p := projects[0]
		if i == 2 || i == 3 {
			p = projects[1]
		}
		d.do("POST", p.path("/agents/"+id+"/stop"), nil)
		waitAgent(t, func() []teamAgent { return agentsOf(p) }, id, time.Minute, func(a teamAgent) bool { return a.State == "exited" })
		d.do("DELETE", p.path("/agents/"+id), nil)
	}

	// 3. A team in each project at the same time.
	for _, p := range projects {
		d.json("POST", p.path("/tasks"), map[string]string{"title": "E2E multi-project: " + p.File,
			"description": "Create exactly one sub-task and assign it right away: create a file named " + p.File +
				" whose only content is the output of the command `" + p.Command + "` run in the repository, and commit it. " +
				"The sub-task description must name the file and the command, and tell the worker to run the command, " +
				"write its output into the file, commit it and then call report_done. When the worker has reported, " +
				"call report_done for the goal."}, 201, &p.goal)
		d.json("POST", p.path("/agents"), map[string]string{"task_id": p.goal.ID, "role": "tech-lead"}, 201, &p.lead)
	}
	var wg sync.WaitGroup
	for _, p := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(12 * time.Minute)
			for {
				var tasks []teamTask
				d.json("GET", p.path("/tasks"), nil, 200, &tasks)
				lead := findAgent(agentsOf(p), p.lead.ID)
				for _, task := range tasks {
					if task.ID == p.goal.ID && task.Status == "review" && lead.State == "idle" {
						return
					}
				}
				if lead.State == "exited" {
					t.Errorf("%s: the lead exited (%s)", p.Name, lead.ExitReason)
					return
				}
				if time.Now().After(deadline) {
					t.Errorf("%s: timeout; tasks %+v, agents %+v", p.Name, tasks, agentsOf(p))
					return
				}
				time.Sleep(3 * time.Second)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	// Each worker used its own pack's toolchain, in its own repository only.
	for i, p := range projects {
		other := projects[1-i]
		workers := 0
		for _, a := range agentsOf(p) {
			if a.Role != "worker" {
				continue
			}
			workers++
			if a.State != "exited" || a.ExitReason != "reported_done" || a.LastImport == nil || a.LastImport.Commits < 1 {
				t.Errorf("%s worker %s: %s (%s), import %+v", p.Name, a.ID, a.State, a.ExitReason, a.LastImport)
				continue
			}
			content, err := exec.Command("git", "-C", p.Repo, "show", a.LastImport.Branch+":"+p.File).Output()
			if err != nil {
				t.Errorf("%s: %s not on %s: %v", p.Name, p.File, a.LastImport.Branch, err)
				continue
			}
			want := map[string]string{"go": "go version go", "node": "v"}[p.Pack]
			if got := strings.TrimSpace(string(content)); !strings.HasPrefix(got, want) {
				t.Errorf("%s: %s holds %q, want the output of %s", p.Name, p.File, got, p.Command)
			} else {
				t.Logf("%s: %s = %q (from its %s pack)", p.Name, p.File, got, p.Pack)
			}
			if exec.Command("git", "-C", other.Repo, "rev-parse", "--verify", "-q", a.LastImport.Branch).Run() == nil {
				t.Errorf("%s's branch %s is in %s's repository", p.Name, a.LastImport.Branch, other.Name)
			}
		}
		if workers != 1 {
			t.Errorf("%s: %d workers", p.Name, workers)
		}
		if st := gitOut(t, p.Repo, "status", "--porcelain"); st != "" {
			t.Errorf("%s: the user's checkout changed: %q", p.Name, st)
		}
	}
	var u struct {
		Projects []struct {
			ProjectID string `json:"project_id"`
			Totals    struct {
				Sessions     int   `json:"sessions"`
				CostMicroUSD int64 `json:"cost_micro_usd"`
			} `json:"totals"`
		} `json:"projects"`
		Totals struct {
			CostMicroUSD int64 `json:"cost_micro_usd"`
		} `json:"totals"`
	}
	d.json("GET", "/api/usage", nil, 200, &u)
	var sum int64
	for _, pu := range u.Projects {
		sum += pu.Totals.CostMicroUSD
		t.Logf("usage of %s: %d sessions, %s", pu.ProjectID, pu.Totals.Sessions, usd(pu.Totals.CostMicroUSD))
		if pu.Totals.Sessions == 0 {
			t.Errorf("project %s has no usage", pu.ProjectID)
		}
	}
	if len(u.Projects) != 2 || sum != u.Totals.CostMicroUSD {
		t.Errorf("usage totals %+v", u)
	}

	// 4. SIGKILL with both Tech Leads running; a restart restores both.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	prev := map[string]string{}
	for _, p := range projects {
		prev[p.ID] = findAgent(agentsOf(p), p.lead.ID).SessionID
	}
	d.signal(syscall.SIGKILL)
	d.start()
	for _, p := range projects {
		var restored teamAgent
		for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
			restored = findAgent(agentsOf(p), p.lead.ID)
			if restored.HasTerminal && restored.SessionID != prev[p.ID] && restored.ResumedFrom != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the lead was not restored: %+v", p.Name, restored)
			}
		}
		w := watchProject(t, ctx, d.base, p.ID)
		w.waitFor(t, p.lead.ID, "idle after the restore", 3*time.Minute, func(h []string) bool {
			return len(h) > 0 && h[len(h)-1] == "idle"
		})
		t.Logf("%s: lead %s restored, session %s resumed from %s", p.Name, p.lead.ID, restored.SessionID, restored.ResumedFrom)
	}
	var inbox []struct {
		Body string `json:"body"`
	}
	for _, p := range projects {
		d.json("GET", p.path("/inbox"), nil, 200, &inbox)
		found := false
		for _, it := range inbox {
			if strings.Contains(it.Body, "restarted") && strings.Contains(it.Body, p.lead.ID) {
				found = true
			}
			for _, o := range projects {
				if o != p && strings.Contains(it.Body, o.lead.ID) {
					t.Errorf("%s's inbox names %s's lead: %s", p.Name, o.Name, it.Body)
				}
			}
		}
		if !found {
			b, _ := json.Marshal(inbox)
			t.Errorf("%s: no restore notice for its lead: %s", p.Name, b)
		}
	}

	// The user marks both goals done: each project's lead ends.
	for _, p := range projects {
		d.json("PATCH", p.path("/tasks/"+p.goal.ID), map[string]string{"status": "done"}, 200, nil)
		a := waitAgent(t, func() []teamAgent { return agentsOf(p) }, p.lead.ID, time.Minute,
			func(a teamAgent) bool { return a.State == "exited" })
		if a.ExitReason != "goal_done" {
			t.Errorf("%s lead exit %q", p.Name, a.ExitReason)
		}
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		switch {
		case strings.Contains(line, "api request refused"), strings.Contains(line, "team tool failed"):
			t.Errorf("%s", line)
		case strings.Contains(line, "project opened"), strings.Contains(line, "stack pack"):
			t.Log(line)
		}
	}
}
