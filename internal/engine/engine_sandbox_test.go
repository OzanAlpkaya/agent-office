//go:build sandbox

// Sandboxed agents against real rootless Podman and the worker image:
// make sandbox-test (needs make worker-image).
package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// fakeClaudeScript stands in for Claude Code inside the worker: it reports
// what it sees, sends hooks through the real in-container forwarder and
// commits on request.
const fakeClaudeScript = `#!/bin/sh
sid=; prev=; last=; bypass=off
for a in "$@"; do
  [ "$prev" = --session-id ] && sid=$a
  [ "$a" = --dangerously-skip-permissions ] && bypass=on
  prev=$a; last=$a
done
send() { printf '{"hook_event_name":"%s","session_id":"%s"}' "$1" "$sid" | /opt/agent-office/agent-office hook >/dev/null 2>&1; }
IGNORE_TERM
echo "bypass=$bypass"
case "$last" in *"Task: Sayfalama ekle"*) echo prompt=ok;; esac
grep -q '"hasTrustDialogAccepted": true' /config/.claude.json && echo seeded=yes
echo "token=$CLAUDE_CODE_OAUTH_TOKEN"
echo "api=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/v1/models)"
send SessionStart
while IFS= read -r line; do
  case "$line" in
    commit*) cd /workspace && echo hi > agent.txt && git add agent.txt && git commit -qm "feat: agent work" \
      && git log -1 --format='log=%an|%(trailers:key=Co-authored-by,valueonly)' | sed '/^$/d' ;;
    exit*) send SessionEnd; exit 0 ;;
  esac
done
`

func sandboxFixture(t *testing.T, ignoreTerm bool) *fixture {
	t.Helper()
	p := sandbox.Podman{}
	if ok, err := p.ImageExists(context.Background(), sandbox.WorkerImage()); err != nil || !ok {
		t.Skipf("worker image %s missing: run make worker-image (%v)", sandbox.WorkerImage(), err)
	}
	self, _ := filepath.Abs("../../bin/agent-office")
	if out, err := exec.Command("file", self).Output(); err != nil || !strings.Contains(string(out), "statically linked") {
		t.Skip("needs a static bin/agent-office: run make build")
	}
	f := newFixture(t)
	gitIn(t, f.repo, "config", "user.name", "Ozan Test")
	gitIn(t, f.repo, "config", "user.email", "ozan@example.com")

	dir := t.TempDir()
	script := strings.Replace(fakeClaudeScript, "IGNORE_TERM", map[bool]string{true: "trap '' TERM HUP", false: ""}[ignoreTerm], 1)
	claude := filepath.Join(dir, "claude")
	tokenDir := filepath.Join(dir, "cfg")
	hooksDir := filepath.Join(dir, "git-hooks")
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, "oauth-token"), []byte("fake-token-for-engine-tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.WriteGitHooks(hooksDir); err != nil {
		t.Fatal(err)
	}
	allow, _ := sandbox.ParseAllowlist(sandbox.DefaultEgressAllow)
	s := &SandboxOptions{
		Instance: "0123456789ab", Podman: p, Image: sandbox.WorkerImage(),
		WorkspaceRoot: filepath.Join(dir, "workspaces"), AgentsRoot: filepath.Join(dir, "agents"),
		GitHooks: hooksDir, AgentOffice: self, Claude: claude, ClaudeVersion: agent.TestedClaudeCodeVersion,
		TokenFile: filepath.Join(tokenDir, "oauth-token"), Allow: allow,
	}
	f.opts.Sandbox = s
	f.opts.StopTimeout = time.Second
	// Registered before the engine, so it runs after Shutdown: agent-owned files need podman unshare.
	t.Cleanup(func() {
		for _, root := range []string{s.WorkspaceRoot, s.AgentsRoot} {
			entries, _ := os.ReadDir(root)
			for _, en := range entries {
				if err := p.RemoveAll(context.Background(), root, filepath.Join(root, en.Name())); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			}
		}
	})
	return f
}

// updateLog keeps every update, so a wait also matches updates that arrived
// while the test was waiting for something else.
type updateLog struct {
	mu   sync.Mutex
	all  []Update
	more chan struct{}
}

func record(t *testing.T, e *Engine) *updateLog {
	t.Helper()
	updates, cancel := e.Subscribe()
	t.Cleanup(cancel)
	l := &updateLog{more: make(chan struct{}, 1)}
	go func() {
		for u := range updates {
			l.mu.Lock()
			l.all = append(l.all, u)
			l.mu.Unlock()
			select {
			case l.more <- struct{}{}:
			default:
			}
		}
	}()
	return l
}

func (l *updateLog) wait(t *testing.T, d time.Duration, match func(Update) bool) Update {
	t.Helper()
	timeout := time.After(d)
	for {
		l.mu.Lock()
		for _, u := range l.all {
			if match(u) {
				l.mu.Unlock()
				return u
			}
		}
		l.mu.Unlock()
		select {
		case <-l.more:
		case <-timeout:
			t.Fatal("timeout waiting for update")
		}
	}
}

func agentState(id string, st hooks.State) func(Update) bool {
	return func(u Update) bool { return u.Type == "agent" && u.Agent.ID == id && u.Agent.State == string(st) }
}

func screen(t *testing.T, e *Engine, id, want string) string {
	t.Helper()
	var last string
	for i := 0; i < 100; i++ {
		term, err := e.Terminal(id)
		if err != nil {
			t.Fatal(err)
		}
		snap, sub, _ := term.Attach()
		if sub != nil {
			sub.Close()
		}
		if last = strings.ReplaceAll(string(snap), "\r\n", "\n"); strings.Contains(last, want) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("terminal never showed %q:\n%s", want, last)
	return ""
}

func containerExists(t *testing.T, name string) bool {
	t.Helper()
	list, err := (sandbox.Podman{}).ListAgentContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestSandboxedAgentLifecycle(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	task, err := e.CreateTask(ctx, TaskInput{Title: "Sayfalama ekle", Description: "GET /bookmarks"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	ws := f.opts.Sandbox.WorkspaceRoot
	if a.Sandbox != SandboxPodman || a.WorkspaceKind != KindClone || filepath.Dir(filepath.Dir(a.WorktreePath)) != ws {
		t.Fatalf("agent %+v", a)
	}
	if got := gitIn(t, f.repo, "for-each-ref", "--format=%(objectname)", git.BaseRefPrefix+a.ID); got == "" {
		t.Fatal("base not protected in the main repository")
	}
	if b, err := os.ReadFile(filepath.Join(ws, "README.txt")); err != nil || !strings.Contains(string(b), "Do not cd into") {
		t.Fatalf("workspaces README: %v", err)
	}

	log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
	for _, want := range []string{"bypass=on", "prompt=ok", "seeded=yes", "token=" + sandbox.Placeholder, "api=403"} {
		screen(t, e, a.ID, want+"\n")
	}
	ev := log.wait(t, 10*time.Second, func(u Update) bool { return u.Type == "sandbox_event" })
	if ev.SandboxEvent.AgentID != a.ID || ev.SandboxEvent.Event.Kind != sandbox.APIRejected ||
		ev.SandboxEvent.Event.Detail != "GET /v1/models" {
		t.Fatalf("sandbox event %+v", ev.SandboxEvent)
	}

	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "log=Ozan Test|"+sandbox.CoAuthor(agentName(a.ID))+"\n")
	// The host refuses to run git in the agent's repository.
	if _, err := e.git.IsDirty(ctx, a.WorktreePath); !errors.Is(err, git.ErrUntrusted) {
		t.Fatalf("host git in the agent repository: %v", err)
	}

	typeLine(t, e, a.ID, "exit")
	// SessionEnd sets the state first; the process watcher then records the exit.
	log.wait(t, 30*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == a.ID && u.Agent.ExitedAt != nil
	})
	got, _ := e.Get(a.ID)
	if got.ExitReason != ExitProcess || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit %+v", got)
	}
	if containerExists(t, sandbox.ContainerName(a.ID)) {
		t.Fatal("container still exists after exit")
	}
	if _, err := os.Stat(filepath.Join(f.opts.RuntimeDir, "agents", a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime dir with sockets not removed")
	}
	var n int
	_ = f.store.DB.QueryRow("SELECT count(*) FROM events WHERE agent_id = ? AND kind = 'api_rejected'", a.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("sandbox events persisted: %d", n)
	}
	if st := e.Status(); st.Sandbox != SandboxPodman || st.Image != sandbox.WorkerImage() {
		t.Fatalf("status %+v", st)
	}
}

// An agent that ignores SIGTERM: the podman client is killed after the stop
// timeout, which leaves the container running; the engine must remove it.
func TestSandboxedStopRemovesStubbornContainer(t *testing.T) {
	f := sandboxFixture(t, true)
	e := f.engine(t)
	log := record(t, e)
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
	start := time.Now()
	if err := e.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop took %v", time.Since(start).Round(100*time.Millisecond))
	if containerExists(t, sandbox.ContainerName(a.ID)) {
		t.Fatal("container survived Stop")
	}
	if got, _ := e.Get(a.ID); got.ExitReason != ExitStopped {
		t.Fatalf("exit reason %q", got.ExitReason)
	}
}

func TestSandboxedStartRefusesBeforeCreatingState(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	ctx := context.Background()
	if err := os.Remove(f.opts.Sandbox.TokenFile); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, StartRequest{}); !errors.Is(err, sandbox.ErrTokenFile) {
		t.Fatalf("missing token file: %v", err)
	}
	gitIn(t, f.repo, "config", "--unset", "user.email")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null") // the developer's own identity must not count
	if _, err := e.Start(ctx, StartRequest{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing git identity: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.opts.Sandbox.WorkspaceRoot, e.Project().Slug)); len(entries) != 0 {
		t.Fatalf("workspaces created: %v", entries)
	}
	if len(e.List()) != 0 {
		t.Fatal("agents recorded for refused starts")
	}
}
