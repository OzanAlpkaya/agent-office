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
	"syscall"
	"testing"
	"time"
)

type importInfo struct {
	Trigger string `json:"trigger"`
	Outcome string `json:"outcome"`
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Commits int    `json:"commits"`
	Flagged []struct {
		Commit, Problem string
	} `json:"flagged"`
}

// TestSandboxEndToEnd runs one real Claude Code agent in a rootless Podman
// worker (Phase 3): it creates and commits a file with permission prompts off,
// the commit is imported into the repository with the user's identity and the
// agent's trailer, and stop/remove leave nothing behind.
//
// It works on a throwaway clone of AO_E2E_REPO, because sandboxed starts add
// a base ref and imported branches to the repository. Requirements beyond the
// host test: podman, the worker image (make worker-image) and the OAuth token
// file (ADR 0015).
func TestSandboxEndToEnd(t *testing.T) {
	src := os.Getenv("AO_E2E_REPO")
	if src == "" {
		t.Skip("AO_E2E_REPO not set; this test runs a real Claude Code session")
	}
	for _, bin := range []string{"claude", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	model := os.Getenv("AO_E2E_MODEL")
	if model == "" {
		model = "haiku"
	}
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	// Agent-owned files belong to a subordinate uid: remove them via podman unshare.
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", data).Run() })
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", gitOut(t, src, "rev-parse", "--show-toplevel"), repo).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	name, email := gitOut(t, repo, "config", "user.name"), gitOut(t, repo, "config", "user.email")

	bin := filepath.Join(tmp, "agent-office")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office")
	build.Env = append(os.Environ(), "CGO_ENABLED=0") // mounted into the worker: must be static
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--repo", repo, "--addr", "127.0.0.1:" + port, "--data-dir", data,
			"--model", model, "--log-level", "debug", "--sandbox", "podman"}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var agentID string
	t.Cleanup(func() {
		if agentID != "" && d.cmd != nil {
			d.do("POST", "/api/agents/"+agentID+"/stop", nil)
		}
		d.signal(syscall.SIGTERM)
		_ = exec.Command("podman", "rm", "-f", "ao-"+agentID).Run()
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 60))
			}
		}
	})
	d.start()
	var status struct{ Sandbox, Image string }
	d.json("GET", "/api/status", nil, 200, &status)
	if status.Sandbox != "podman" {
		t.Fatalf("status %+v", status)
	}
	w := watch(t, ctx, d.base)

	var task taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E sandbox",
		"description": "Create a file named sandbox-e2e.txt containing the single word hi, then commit it " +
			"with the message 'test: sandbox e2e'. Do not do anything else and do not ask questions."}, 201, &task)
	var a struct {
		agentInfo
		Sandbox       string `json:"sandbox"`
		WorkspaceKind string `json:"workspace_kind"`
	}
	d.json("POST", "/api/agents", map[string]string{"task_id": task.ID}, 201, &a)
	agentID = a.ID
	if a.Sandbox != "podman" || a.WorkspaceKind != "clone" {
		t.Fatalf("agent %+v", a)
	}

	// starting -> idle -> working (task prompt, tools without prompts) -> idle (Stop)
	w.waitFor(t, a.ID, "the task turn to finish", 4*time.Minute, func(h []string) bool {
		n := len(h)
		return n >= 2 && h[n-1] == "idle" && strings.Contains(strings.Join(h, ","), "working")
	})
	if h := strings.Join(w.snapshot(a.ID), ","); strings.Contains(h, "waiting_permission") {
		t.Errorf("a permission prompt appeared in bypass mode: %s", h)
	}

	// Import now, while the agent still runs.
	var imp importInfo
	d.json("POST", "/api/agents/"+a.ID+"/import", nil, 200, &imp)
	if imp.Outcome != "created" || imp.Branch != a.Branch || imp.Commits < 1 {
		t.Fatalf("import %+v", imp)
	}
	if len(imp.Flagged) != 0 {
		t.Errorf("flagged commits: %+v", imp.Flagged)
	}
	if got := gitOut(t, repo, "show", a.Branch+":sandbox-e2e.txt"); strings.TrimSpace(got) != "hi" {
		t.Errorf("file on the imported branch: %q", got)
	}
	if got := gitOut(t, repo, "log", "-1", "--format=%an <%ae>", a.Branch); got != name+" <"+email+">" {
		t.Errorf("author %q, want %s <%s>", got, name, email)
	}
	if got := gitOut(t, repo, "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)", a.Branch); !strings.Contains(got, "(agent-office) <noreply@agent-office.invalid>") {
		t.Errorf("trailer %q", got)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the user's checkout changed: %q", st)
	}

	// Stop: the container goes away; removal keeps the branch with commits.
	d.json("POST", "/api/agents/"+a.ID+"/stop", nil, 200, nil)
	if out, _ := exec.Command("podman", "ps", "-aq", "--filter", "label=io.agent-office.agent="+a.ID).Output(); len(strings.TrimSpace(string(out))) != 0 {
		t.Error("worker container still exists after stop")
	}
	code, body := d.do("DELETE", "/api/agents/"+a.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, body)
	}
	var res struct{ Worktree, Branch string }
	_ = json.Unmarshal(body, &res)
	if res.Worktree != "removed" || res.Branch != "kept" {
		t.Errorf("remove result %+v", res)
	}
	if _, err := os.Stat(a.WorktreePath); err == nil {
		t.Error("agent repository still on disk")
	}

	// Nothing refused on the API socket: Claude Code needed only POST /v1/messages.
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		if strings.Contains(line, "api request refused") {
			t.Errorf("API socket refused a request: %s", line)
		}
		if strings.Contains(line, "egress refused") {
			t.Logf("egress denial (expected for the plugin marketplace): %s", line)
		}
	}
	agentID = ""
}
