//go:build sandbox

// Sandboxed agents against real rootless Podman and the worker image:
// make sandbox-test (needs make worker-image).
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// fakeClaudeScript stands in for Claude Code inside the worker: it reports
// what it sees, sends hooks through the real in-container forwarder and
// commits on request.
const fakeClaudeScript = `#!/bin/sh
sid=; prev=; last=; bypass=off; n=0
for a in "$@"; do
  [ "$prev" = --session-id ] && sid=$a
  [ "$a" = --dangerously-skip-permissions ] && bypass=on
  prev=$a; last=$a
done
send() { printf '{"hook_event_name":"%s","session_id":"%s"}' "$1" "$sid" | /opt/agent-office/agent-office hook >/dev/null 2>&1; }
IGNORE_TERM
echo "bypass=$bypass"
case "$last" in *"Task: Sayfalama ekle"*) echo prompt=ok;; esac
case "$last" in *"[agent-office] Review of pull request"*) echo followup=ok;; esac
case "$last" in *"code reviewer of a pull request"*) echo review-prompt=ok;; esac
grep -q '"hasTrustDialogAccepted": true' /config/.claude.json && echo seeded=yes
echo "token=$CLAUDE_CODE_OAUTH_TOKEN"
echo "api=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/v1/models)"
send SessionStart
while IFS= read -r line; do
  case "$line" in
    commit*) cd /workspace && n=$((n+1)) && echo "$n $(date +%s%N)" > "agent-$n.txt" && git add "agent-$n.txt" \
      && git commit -qm "feat: agent work $n" \
      && git log -1 --format='log=%an|%(trailers:key=Co-authored-by,valueonly)' | sed '/^$/d' \
      && echo "committed $n" ;;
    amend*) cd /workspace && git commit -q --amend -m "feat: agent work (amended)" && echo amended ;;
    nohook*) cd /workspace && git -c core.hooksPath=/dev/null commit -q --allow-empty -m "chore: no trailer" && echo nohook ;;
    dirty*) echo wip > /workspace/uncommitted.txt && echo dirty ;;
    message*) curl -s -o /dev/null -w 'message=%{http_code}\n' -X POST -H 'content-type: application/json' \
      -H "Authorization: Bearer $CLAUDE_CODE_OAUTH_TOKEN" -d '{"model":"m","messages":[]}' http://127.0.0.1:8080/v1/messages ;;
    mcp\ *) tool=${line#mcp }; tool=${tool%% *}; args=${line#mcp $tool }
      { printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fake","version":"1"}}}' \
          '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
          "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}"
        sleep 8; } | /opt/agent-office/agent-office mcp | grep '"id":2' | sed 's/^/mcp-reply=/' ;;
    sh\ *) sh -c "${line#sh }" 2>&1 | sed 's/^/sh: /' ;;
    stop*) send Stop ;;
    exit*) send SessionEnd; exit 0 ;;
  esac
done
`

func sandboxFixture(t *testing.T, ignoreTerm bool) *fixture {
	t.Helper()
	p := sandbox.Podman{}
	if ok, err := p.ImageExists(context.Background(), sandbox.BaseImage()); err != nil || !ok {
		t.Skipf("worker image %s missing: run make worker-image (%v)", sandbox.BaseImage(), err)
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
		Instance: "0123456789ab", Podman: p, Image: sandbox.BaseImage(),
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
	if evs, err := e.SandboxEvents(ctx, a.ID, 10); err != nil || len(evs) != 1 || evs[0].Detail != "GET /v1/models" {
		t.Fatalf("listed sandbox events %+v %v", evs, err)
	}
	if st := e.Status(); st.Sandbox != SandboxPodman || st.Image != sandbox.BaseImage() {
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

func startIdle(t *testing.T, e *Engine, log *updateLog) AgentInfo {
	t.Helper()
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
	return a
}

func exitAgent(t *testing.T, e *Engine, log *updateLog, id string) {
	t.Helper()
	typeLine(t, e, id, "exit")
	log.wait(t, 30*time.Second, func(u Update) bool { return u.Type == "agent" && u.Agent.ID == id && u.Agent.ExitedAt != nil })
}

func waitImport(t *testing.T, log *updateLog, id, trigger string) ImportInfo {
	t.Helper()
	u := log.wait(t, 60*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == id && u.Agent.LastImport != nil && u.Agent.LastImport.Trigger == trigger
	})
	return *u.Agent.LastImport
}

func TestSandboxedImportNowWhileRunning(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a := startIdle(t, e, log)

	if imp, err := e.Import(ctx, a.ID); err != nil || imp.Outcome != OutcomeNoCommits {
		t.Fatalf("no commits yet: %+v %v", imp, err)
	}
	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "log=Ozan Test|")
	imp, err := e.Import(ctx, a.ID)
	if err != nil || imp.Outcome != string(git.ImportCreated) || imp.Branch != a.Branch || imp.Commits != 1 || len(imp.Flagged) != 0 {
		t.Fatalf("first import: %+v %v", imp, err)
	}
	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "committed 2\n")
	if imp, err := e.Import(ctx, a.ID); err != nil || imp.Outcome != string(git.ImportFastForward) || imp.Commits != 2 {
		t.Fatalf("fast-forward: %+v %v", imp, err)
	}
	second := gitIn(t, f.repo, "rev-parse", a.Branch)

	typeLine(t, e, a.ID, "amend")
	screen(t, e, a.ID, "amended\n")
	imp, err = e.Import(ctx, a.ID)
	if err != nil || imp.Outcome != string(git.ImportDiverged) || imp.Branch != a.Branch+"-rewrite-2" || imp.Previous != second {
		t.Fatalf("rewrite: %+v %v", imp, err)
	}
	if gitIn(t, f.repo, "rev-parse", a.Branch) != second {
		t.Fatal("the imported branch moved on a rewrite")
	}
	log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "sandbox_event" && u.SandboxEvent.Event.Kind == sandbox.ImportDiverged &&
			strings.Contains(u.SandboxEvent.Event.Detail, a.Branch+"-rewrite-2")
	})

	typeLine(t, e, a.ID, "nohook")
	screen(t, e, a.ID, "nohook\n")
	imp, err = e.Import(ctx, a.ID)
	if err != nil || imp.Outcome != string(git.ImportFastForward) || len(imp.Flagged) != 1 ||
		!strings.Contains(imp.Flagged[0].Problem, "no Co-Authored-By") {
		t.Fatalf("flagged commit: %+v %v", imp, err)
	}
	log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "sandbox_event" && u.SandboxEvent.Event.Kind == sandbox.CommitFlagged
	})
	var n int
	_ = f.store.DB.QueryRow("SELECT count(*) FROM imports WHERE agent_id = ? AND trigger = 'manual'", a.ID).Scan(&n)
	if n != 5 {
		t.Fatalf("manual imports recorded: %d", n)
	}
}

func TestSandboxedImportOnExit(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "log=Ozan Test|")
	exitAgent(t, e, log, a.ID)
	imp := waitImport(t, log, a.ID, TriggerExit)
	if imp.Outcome != string(git.ImportCreated) || imp.Commits != 1 || len(imp.Flagged) != 0 {
		t.Fatalf("import on exit %+v", imp)
	}
	if trailer := gitIn(t, f.repo, "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)", a.Branch); trailer != sandbox.CoAuthor(agentName(a.ID)) {
		t.Fatalf("trailer in the main repository: %q", trailer)
	}
}

func TestSandboxedRemoveKeepsDirtyAndSavesWork(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a := startIdle(t, e, log)
	typeLine(t, e, a.ID, "dirty")
	screen(t, e, a.ID, "dirty\n")
	if _, err := e.SaveWork(ctx, a.ID); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("save work while running: %v", err)
	}
	exitAgent(t, e, log, a.ID)
	waitImport(t, log, a.ID, TriggerExit) // no commits yet

	if _, err := e.Remove(ctx, a.ID); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("dirty clone removed: %v", err)
	}
	if _, err := os.Stat(a.WorktreePath); err != nil {
		t.Fatal("dirty clone is gone")
	}
	imp, err := e.SaveWork(ctx, a.ID)
	if err != nil || imp.Trigger != TriggerSaveWork || imp.Outcome != string(git.ImportCreated) || len(imp.Flagged) != 0 {
		t.Fatalf("save work %+v %v", imp, err)
	}
	if files := gitIn(t, f.repo, "show", "--name-only", "--format=", a.Branch); files != "uncommitted.txt" {
		t.Fatalf("saved files %q", files)
	}
	if _, err := e.SaveWork(ctx, a.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("save work with nothing to save: %v", err)
	}
	res, err := e.Remove(ctx, a.ID)
	if err != nil || res.Worktree != "removed" || res.Branch != "kept" {
		t.Fatalf("remove %+v %v", res, err)
	}
	if _, err := os.Stat(a.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clone still on disk")
	}
	if got := gitIn(t, f.repo, "for-each-ref", git.BaseRefPrefix+a.ID); got != "" {
		t.Fatal("base ref not removed")
	}
	if _, err := os.Stat(filepath.Join(f.opts.Sandbox.AgentsRoot, a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("agent config dir not removed")
	}
}

func TestSandboxedRemoveWithoutCommitsDeletesBranch(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	exitAgent(t, e, log, a.ID)
	waitImport(t, log, a.ID, TriggerExit)
	res, err := e.Remove(context.Background(), a.ID)
	if err != nil || res.Branch != "deleted" {
		t.Fatalf("remove %+v %v", res, err)
	}
}

// Removal imports first; if that fails, nothing is removed.
func TestSandboxedRemoveRefusesWhenImportFails(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a := startIdle(t, e, log)
	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "log=Ozan Test|")
	if _, err := e.Import(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.repo, "switch", "-q", a.Branch) // the user looks at the agent's branch
	typeLine(t, e, a.ID, "commit")
	screen(t, e, a.ID, "committed 2\n")
	exitAgent(t, e, log, a.ID)
	log.wait(t, 30*time.Second, func(u Update) bool {
		return u.Type == "sandbox_event" && u.SandboxEvent.Event.Kind == sandbox.ImportFailed
	})
	if _, err := e.Remove(ctx, a.ID); !errors.Is(err, git.ErrBranchCheckedOut) {
		t.Fatalf("remove with a failing import: %v", err)
	}
	if _, err := os.Stat(a.WorktreePath); err != nil {
		t.Fatal("clone removed although its commit was not imported")
	}
	gitIn(t, f.repo, "switch", "-q", "main")
	if res, err := e.Remove(ctx, a.ID); err != nil || res.Worktree != "removed" {
		t.Fatalf("remove after switching away: %+v %v", res, err)
	}
	if n := gitIn(t, f.repo, "rev-list", "--count", "main.."+a.Branch); n != "2" {
		t.Fatalf("commits on the branch: %s", n)
	}
}

// A daemon that died leaves its container running under conmon. The next
// daemon on the same data removes it and imports the agent's work.
func TestSandboxedRestartRemovesOrphanAndImports(t *testing.T) {
	f := sandboxFixture(t, false)
	e1, err := New(context.Background(), f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log1 := record(t, e1)
	a := startIdle(t, e1, log1)
	typeLine(t, e1, a.ID, "commit")
	screen(t, e1, a.ID, "log=Ozan Test|")

	// The "next daemon" starts while the old one's worker still runs.
	e2, err := New(context.Background(), f.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e2.Shutdown)
	t.Cleanup(e1.Shutdown)
	if containerExists(t, sandbox.ContainerName(a.ID)) {
		t.Fatal("orphan container not removed by the next daemon")
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var trigger string
		err := f.store.DB.QueryRow("SELECT trigger FROM imports WHERE agent_id = ? AND trigger = 'restart'", a.ID).Scan(&trigger)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no import after restart")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := gitIn(t, f.repo, "rev-list", "--count", "main.."+a.Branch); n != "1" {
		t.Fatalf("commits imported after restart: %s", n)
	}
	got, _ := e2.Get(a.ID)
	if got.ExitReason != ExitDaemonRestart {
		t.Fatalf("exit reason %q", got.ExitReason)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.b.Bytes())
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

// hostFiles concatenates the regular files under dir, skipping skip.
func hostFiles(t *testing.T, dir, skip string) []byte {
	t.Helper()
	var all []byte
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == skip {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			all = append(all, b...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// agentFilesContain greps files owned by the container's subordinate uid.
// The pattern goes through stdin, never argv, which other users can read.
func agentFilesContain(t *testing.T, token string, dirs ...string) bool {
	t.Helper()
	cmd := exec.Command("podman", append([]string{"unshare", "grep", "-rqF", "-f", "-", "--"}, dirs...)...)
	cmd.Stdin = strings.NewReader(token + "\n")
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false
	default:
		t.Fatalf("grep agent files: %v", err)
		return false
	}
}

// TestSandboxedTokenStaysOutside is the Phase 3 secrets DoD (ADR 0015, ADR
// 0016): the OAuth token from the token file reaches the API upstream and
// nothing else. It is absent from the worker's processes, env and files,
// podman inspect, the runtime dir, the terminal, updates, logs, the database
// and both repositories.
func TestSandboxedTokenStaysOutside(t *testing.T) {
	f := sandboxFixture(t, false)
	token := "sk-ant-oat01-aoCanary" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.WriteFile(f.opts.Sandbox.TokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	f.opts.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auth := make(chan string, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
	}))
	t.Cleanup(up.Close)
	f.opts.Sandbox.apiUpstream, _ = url.Parse(up.URL)
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
	log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))

	// Positive control: the proxy injects the token from the file.
	typeLine(t, e, a.ID, "message")
	screen(t, e, a.ID, "message=200\n")
	select {
	case got := <-auth:
		if got != "Bearer "+token {
			t.Fatal("the upstream did not get the token from the token file")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the upstream")
	}

	name := sandbox.ContainerName(a.ID)
	inspect, err := exec.Command("podman", "inspect", name).Output()
	if err != nil || !bytes.Contains(inspect, []byte("CLAUDE_CODE_OAUTH_TOKEN="+sandbox.Placeholder)) {
		t.Fatalf("podman inspect without the placeholder env: %v", err)
	}
	noToken(t, "podman inspect", inspect, token)
	inside, err := exec.Command("podman", "exec", name, "sh", "-c",
		`cat /proc/[0-9]*/environ /proc/[0-9]*/cmdline 2>/dev/null; find /workspace /config /tmp /run -type f -exec cat {} + 2>/dev/null; true`).Output()
	if err != nil || !bytes.Contains(inside, []byte("sandbox-init")) {
		t.Fatalf("worker processes not read: %v", err)
	}
	noToken(t, "the worker's processes, env and files", inside, token)
	noToken(t, "the runtime dir", hostFiles(t, filepath.Join(f.opts.RuntimeDir, "agents", a.ID), ""), token)

	typeLine(t, e, a.ID, "commit")
	noToken(t, "the terminal", []byte(screen(t, e, a.ID, "committed 1\n")), token)
	exitAgent(t, e, log, a.ID)
	if imp := waitImport(t, log, a.ID, TriggerExit); imp.Commits != 1 {
		t.Fatalf("import %+v", imp)
	}

	log.mu.Lock()
	ups, err := json.Marshal(log.all)
	log.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "the updates", ups, token)
	noToken(t, "the daemon log", logs.Bytes(), token)
	for _, p := range []string{f.dbp, f.dbp + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err == nil {
			noToken(t, filepath.Base(p), b, token)
		}
	}
	history, err := exec.Command("git", "-C", f.repo, "log", "--all", "-p", "--format=raw").Output()
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "the main repository's history", history, token)
	noToken(t, "the main repository's files", hostFiles(t, f.repo, filepath.Join(f.repo, ".git", "objects")), token)
	if agentFilesContain(t, token, a.WorktreePath, f.opts.Sandbox.AgentsRoot) {
		t.Error("the token is in the agent's repository or config dir")
	}
	// The check above can find it: the token file itself.
	if !agentFilesContain(t, token, filepath.Dir(f.opts.Sandbox.TokenFile)) {
		t.Fatal("grep did not find the token in the token file")
	}
}

// A sandboxed team worker uses the team tools through the real in-container
// bridge and socket: identity comes from the mounted socket, and report_done
// imports its branch (trigger report_done) before the task moves to review.
func TestSandboxedTeamWorker(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	lead, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleLead})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 60*time.Second, agentState(lead.ID, hooks.Idle))
	tm := e.Team()
	sub, err := tm.CreateTask(ctx, lead.ID, mcp.CreateTaskInput{Title: "Add tag column"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := tm.AssignTask(ctx, lead.ID, mcp.AssignTaskInput{TaskID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	w := as.AgentID
	log.wait(t, 60*time.Second, agentState(w, hooks.Idle))

	if err := e.Input(w, []byte("mcp list_tasks {}\n")); err != nil {
		t.Fatal(err)
	}
	out := screen(t, e, w, "mcp-reply=")
	if !strings.Contains(out, `"id":"`+w+`","role":"worker"`) && !strings.Contains(out, `"id":"`+w+`"`) {
		t.Fatalf("list_tasks through the in-container bridge:\n%s", out)
	}
	if err := e.Input(w, []byte("commit\n")); err != nil {
		t.Fatal(err)
	}
	screen(t, e, w, "committed 1")
	if err := e.Input(w, []byte(`mcp report_done {"summary":"done in the sandbox"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 300 && !ok(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if !ok() {
			t.Fatalf("timeout waiting for %s:\n%s", what, screen(t, e, w, ""))
		}
	}
	waitFor("the task in review", func() bool { task, _ := e.getTask(ctx, sub.ID); return task.Status == StatusReview })
	imp := waitImport(t, log, w, TriggerReportDone)
	if imp.Commits != 1 || imp.Outcome != "created" {
		t.Fatalf("import %+v", imp)
	}
	if got := gitIn(t, f.repo, "log", "-1", "--format=%s", imp.Branch); got != "feat: agent work 1" {
		t.Fatalf("imported branch head %q", got)
	}
	msgs, _ := e.Messages(ctx, lead.ID)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "done in the sandbox") {
		t.Fatalf("lead messages %+v", msgs)
	}
}

// TestSandboxedReviewLoop runs the Phase 5 loop with real Podman and fake
// agents (no tokens): a sandboxed worker reports through the in-container
// bridge; its imported branch is pushed and becomes a pull request; a
// sandboxed reviewer submits through its bridge; the worker is relaunched in
// its own workspace (its config dir now belongs to the sandbox user) and its
// new commit updates the pull request.
func TestSandboxedReviewLoop(t *testing.T) {
	f := sandboxFixture(t, false)
	_, srv, bare := withGitHub(t, f)
	logs := &lockedBuffer{}
	f.opts.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("engine log:\n%s", logs.Bytes())
		}
	})
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	task, _ := e.CreateTask(ctx, TaskInput{Title: "Add tags"})
	w, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 60*time.Second, agentState(w.ID, hooks.Idle))
	type_ := func(id, line string) {
		t.Helper()
		if err := e.Input(id, []byte(line+"\n")); err != nil {
			t.Fatal(err)
		}
	}
	type_(w.ID, "commit")
	screen(t, e, w.ID, "committed 1")
	type_(w.ID, `mcp report_done {"summary":"tags added"}`)
	pr := log.wait(t, 90*time.Second, func(u Update) bool { return u.Type == "pull_request" && u.PullRequest.State == PROpen }).PullRequest
	if got := gitIn(t, bare, "rev-parse", "refs/heads/"+pr.Branch); got != pr.Head {
		t.Fatalf("remote %s, PR head %s", got, pr.Head)
	}
	type_(w.ID, "exit")
	log.wait(t, 30*time.Second, agentState(w.ID, hooks.Exited))

	r := log.wait(t, 60*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.Role == RoleReviewer && u.Agent.State == string(hooks.Idle)
	}).Agent
	screen(t, e, r.ID, "review-prompt=ok")
	type_(r.ID, `mcp submit_review {"verdict":"changes_requested","summary":"add a test","comments":[{"path":"agent-1.txt","line":1,"body":"why?"}]}`)
	out := screen(t, e, r.ID, "mcp-reply=")
	if !strings.Contains(out, `"posted":true`) {
		t.Fatalf("submit_review through the bridge:\n%s", out)
	}
	type_(r.ID, "stop")
	if got := srv.ReviewsOf(1); len(got) != 1 || got[0].CommitID != pr.Head {
		t.Fatalf("posted reviews %+v", got)
	}

	log.wait(t, 90*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.SessionID != w.SessionID && u.Agent.State == string(hooks.Idle)
	})
	screen(t, e, w.ID, "followup=ok")
	type_(w.ID, "commit")
	screen(t, e, w.ID, "committed 1")
	type_(w.ID, `mcp report_done {"summary":"test added"}`)
	pr2 := log.wait(t, 90*time.Second, func(u Update) bool {
		return u.Type == "pull_request" && u.PullRequest.State == PROpen && u.PullRequest.Head != pr.Head
	}).PullRequest
	if pr2.Number != 1 || gitIn(t, bare, "rev-parse", "refs/heads/"+pr2.Branch) != pr2.Head {
		t.Fatalf("follow-up push %+v", pr2)
	}
	if n := gitIn(t, f.repo, "rev-list", "--count", pr.Head+".."+pr2.Head); n != "1" {
		t.Fatalf("follow-up is %s commit(s) on top, want a fast-forward of 1", n)
	}
}
