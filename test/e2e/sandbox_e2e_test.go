//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
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

// TestSandboxEndToEnd runs real Claude Code agents in rootless Podman workers
// (Phase 3). Agent A creates and commits a file with permission prompts off;
// the commit is imported with the user's identity and the agent's trailer.
// The daemon is then SIGKILLed: the restart removes A's orphaned container and
// imports its work. Agent B covers stop. The OAuth token never appears in
// podman inspect, the workers, the daemon log, the data dir or the repository,
// and removal leaves nothing behind.
//
// It works on a throwaway clone of AO_E2E_REPO, because sandboxed starts add
// a base ref and imported branches to the repository. Requirements beyond the
// host test: podman, the stack pack image (make worker-image PACK=go) and the OAuth token
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
	tokenPath := os.Getenv(sandbox.TokenFileEnv)
	if tokenPath == "" {
		var err error
		if tokenPath, err = sandbox.DefaultTokenFile(); err != nil {
			t.Fatal(err)
		}
	}
	secret, err := sandbox.ReadTokenFile(tokenPath)
	if err != nil {
		t.Skipf("no usable OAuth token file: %v", err) // the error never contains the token
	}
	token := secret.Reveal()
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
			"--model", model, "--log-level", "debug", "--sandbox", "podman", "--pack", e2ePack()}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var agentIDs []string
	t.Cleanup(func() {
		if d.cmd != nil {
			for _, id := range agentIDs {
				d.do("POST", "/api/agents/"+id+"/stop", nil)
			}
		}
		d.signal(syscall.SIGTERM)
		for _, id := range agentIDs {
			_ = exec.Command("podman", "rm", "-f", "ao-"+id).Run()
		}
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 60))
			}
		}
	})
	d.start()
	var status struct {
		Sandbox, Image string
		Pack           *struct{ Name string } `json:"pack"`
	}
	d.json("GET", "/api/status", nil, 200, &status)
	if status.Sandbox != "podman" || status.Pack == nil || status.Pack.Name != e2ePack() ||
		!strings.HasPrefix(status.Image, "localhost/agent-office-pack-"+e2ePack()+":") {
		t.Fatalf("status %+v", status)
	}
	w := watch(t, ctx, d.base)

	var task taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E sandbox",
		"description": "Create a file named sandbox-e2e.txt containing the single word hi. Then run " +
			"`go test -race ./...` in the repository and create a file named race.txt containing the single word " +
			"race-ok if it passed or race-failed if it did not. Commit both files with the message " +
			"'test: sandbox e2e'. Do not do anything else and do not ask questions."}, 201, &task)
	var a struct {
		agentInfo
		Sandbox       string `json:"sandbox"`
		WorkspaceKind string `json:"workspace_kind"`
	}
	d.json("POST", "/api/agents", map[string]string{"task_id": task.ID}, 201, &a)
	agentIDs = append(agentIDs, a.ID)
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
	// up_to_date when the agent already called report_done (Phase 4), which imports first.
	if (imp.Outcome != "created" && imp.Outcome != "up_to_date") || imp.Branch != a.Branch || imp.Commits < 1 {
		t.Fatalf("import %+v", imp)
	}
	if len(imp.Flagged) != 0 {
		t.Errorf("flagged commits: %+v", imp.Flagged)
	}
	if got := gitOut(t, repo, "show", a.Branch+":sandbox-e2e.txt"); strings.TrimSpace(got) != "hi" {
		t.Errorf("file on the imported branch: %q", got)
	}
	// The go pack's toolchain, with the real agent: the race detector needs cgo.
	if got := gitOut(t, repo, "show", a.Branch+":race.txt"); e2ePack() == "go" && strings.TrimSpace(got) != "race-ok" {
		t.Errorf("race.txt on the imported branch: %q", got)
	}
	// The pack's skill sits in the agent's config dir. Whether Claude Code
	// listed it is visible only in its own files there (logged, not asserted:
	// the transcript format is not ours).
	if out, err := exec.Command("podman", "exec", "ao-"+a.ID, "sh", "-c",
		"test -s /config/skills/go-sandbox/SKILL.md && echo skill=present; "+
			"grep -rlF go-sandbox /config --exclude-dir=skills 2>/dev/null | head -3").Output(); err == nil {
		if e2ePack() == "go" && !strings.Contains(string(out), "skill=present") {
			t.Errorf("the go pack's skill is not in the agent's config dir: %s", out)
		}
		t.Logf("pack skill in the worker: %s", strings.TrimSpace(string(out)))
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

	// The token reaches the API through the daemon's proxy only.
	inspect, err := exec.Command("podman", "inspect", "ao-"+a.ID).Output()
	if err != nil || !bytes.Contains(inspect, []byte("CLAUDE_CODE_OAUTH_TOKEN="+sandbox.Placeholder)) {
		t.Fatalf("podman inspect without the placeholder env: %v", err)
	}
	noToken(t, "podman inspect", inspect, token)
	inside, err := exec.Command("podman", "exec", "ao-"+a.ID, "sh", "-c",
		`cat /proc/[0-9]*/environ /proc/[0-9]*/cmdline 2>/dev/null; find /workspace /config /tmp /run -type f -exec cat {} + 2>/dev/null; true`).Output()
	if err != nil || !bytes.Contains(inside, []byte("sandbox-init")) || !bytes.Contains(inside, []byte("claude")) {
		t.Fatalf("worker processes not read: %v", err)
	}
	noToken(t, "the worker's processes, env and files (including Claude Code's config and transcripts)", inside, token)

	// Claude Code lists the pack's skill: /skills shows the available skills without a model call
	// (code.claude.com/docs/en/commands). Esc closes the list.
	if e2ePack() == "go" {
		tm := attach(t, ctx, d.base, a.ID)
		time.Sleep(2 * time.Second) // the scrollback replay and redraw after attaching
		from := tm.len()            // only output after /skills counts: the task turn may name the skill
		tm.typeLine(ctx, "/skills")
		after := func() string {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			return ansi.ReplaceAllString(string(tm.buf.Bytes()[from:]), "")
		}
		listed := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !listed; time.Sleep(500 * time.Millisecond) {
			listed = strings.Contains(after(), "go-sandbox")
		}
		tm.send(ctx, "\x1b")
		if !listed {
			t.Errorf("/skills did not list the pack's go-sandbox skill:\n%s", tail(after(), 40))
		} else {
			t.Log("/skills lists the pack's go-sandbox skill")
		}
	}

	// Hard crash: the worker outlives the daemon; the restart removes it and
	// imports the agent's work.
	d.signal(syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	if !containerExists(a.ID) {
		t.Log("the worker container ended with the daemon; the restart has no orphan to remove")
	}
	d.start()
	var after agentInfoImport
	deadline := time.Now().Add(time.Minute)
	for {
		var list []agentInfoImport
		d.json("GET", "/api/agents", nil, 200, &list)
		if len(list) == 1 && list[0].LastImport != nil && list[0].LastImport.Trigger == "restart" {
			after = list[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no restart import: %+v", list)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if after.State != "exited" || after.ExitReason != "daemon_restart" || after.HasTerminal {
		t.Errorf("after restart: %+v", after.agentInfo)
	}
	if after.LastImport.Outcome != "up_to_date" || after.LastImport.Head != imp.Head {
		t.Errorf("restart import %+v, want up_to_date at %s", after.LastImport, imp.Head)
	}
	if containerExists(a.ID) {
		t.Error("orphaned worker container still exists after the restart")
	}

	// Agent B: stop removes its container.
	w = watch(t, ctx, d.base)
	var b agentInfo
	d.json("POST", "/api/agents", map[string]string{}, 201, &b)
	agentIDs = append(agentIDs, b.ID)
	w.waitFor(t, b.ID, "B to become idle", 2*time.Minute, func(h []string) bool { return len(h) > 0 && h[len(h)-1] == "idle" })
	d.json("POST", "/api/agents/"+b.ID+"/stop", nil, 200, nil)
	if containerExists(b.ID) {
		t.Error("worker container still exists after stop")
	}

	// The token is nowhere on disk: the data dir (database, agent repositories,
	// Claude Code config and transcripts), the daemon log and the repository.
	if files := filesWithToken(t, token, data); len(files) != 0 {
		t.Errorf("the token is in %v", files)
	}
	logData, _ := os.ReadFile(d.logPath)
	noToken(t, "the daemon log", logData, token)
	history, err := exec.Command("git", "-C", repo, "log", "--all", "-p", "--format=raw").Output()
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "the repository's history", history, token)
	if files := filesWithToken(t, token, repo); len(files) != 0 {
		t.Errorf("the token is in %v", files)
	}

	// Removal: A keeps its branch (it has commits), B's goes.
	for _, c := range []struct{ id, branch string }{{a.ID, "kept"}, {b.ID, "deleted"}} {
		code, body := d.do("DELETE", "/api/agents/"+c.id, nil)
		if code != http.StatusOK {
			t.Fatalf("remove %s: %d %s", c.id, code, body)
		}
		var res struct{ Worktree, Branch string }
		_ = json.Unmarshal(body, &res)
		if res.Worktree != "removed" || res.Branch != c.branch {
			t.Errorf("remove result %+v, want branch %s", res, c.branch)
		}
	}
	if _, err := os.Stat(a.WorktreePath); err == nil {
		t.Error("agent repository still on disk")
	}

	// Nothing refused on the API socket: Claude Code needed only POST /v1/messages.
	logData, _ = os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		if strings.Contains(line, "api request refused") {
			t.Errorf("API socket refused a request: %s", line)
		}
		if strings.Contains(line, "egress refused") {
			t.Logf("egress denial (expected for the plugin marketplace): %s", line)
		}
	}
	agentIDs = nil
}

type agentInfoImport struct {
	agentInfo
	LastImport *importInfo `json:"last_import"`
}

func containerExists(agentID string) bool {
	out, _ := exec.Command("podman", "ps", "-aq", "--filter", "label=io.agent-office.agent="+agentID).Output()
	return len(strings.TrimSpace(string(out))) != 0
}

// noToken fails if data contains the token; the message names only where.
func noToken(t *testing.T, where string, data []byte, token string) {
	t.Helper()
	if len(data) == 0 {
		t.Errorf("%s: nothing to check", where)
	}
	if bytes.Contains(data, []byte(token)) {
		t.Errorf("the token is in %s", where)
	}
}

// filesWithToken lists the files under dir that contain the token, reading
// agent-owned (subordinate uid) files via podman unshare. The token goes
// through stdin, never argv.
func filesWithToken(t *testing.T, token, dir string) []string {
	t.Helper()
	cmd := exec.Command("podman", "unshare", "grep", "-rlF", "-f", "-", "--", dir)
	cmd.Stdin = strings.NewReader(token + "\n")
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
		t.Fatalf("grep %s: %v", dir, err)
	}
	return strings.Fields(string(out))
}
