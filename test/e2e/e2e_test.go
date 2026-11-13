//go:build e2e

// Package e2e runs agent-office end to end against a real Claude Code CLI.
// It is opt-in because it spends tokens (haiku, a few cents per run):
//
//	AO_E2E_REPO=~/projects/agent-office-playground make e2e
//
// Requirements: `claude` on PATH, a git repository with at least one commit
// that Claude Code already trusts (worktrees inherit the repository's trust,
// docs/research/claude-code-trust.md). The test creates worktrees and branches
// in that repository and removes them again, also when it fails.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type agentInfo struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	TaskID         string `json:"task_id"`
	State          string `json:"state"`
	Activity       string `json:"activity"`
	Branch         string `json:"branch"`
	WorktreePath   string `json:"worktree_path"`
	WorktreeStatus string `json:"worktree_status"`
	HasTerminal    bool   `json:"has_terminal"`
	ExitReason     string `json:"exit_reason"`
	ResumedFrom    string `json:"resumed_from"`
	ExitDetail     string `json:"exit_detail"` // Phase 16 (ADR 0051)
	ExitSignal     string `json:"exit_signal"`
	OOMKilled      bool   `json:"oom_killed"`
}

type taskInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type daemon struct {
	t       *testing.T
	bin     string
	args    []string
	env     []string // added to the test's environment (Phase 20: HTTPS_PROXY)
	base    string
	logPath string
	cmd     *exec.Cmd
}

func (d *daemon) start() {
	d.t.Helper()
	logf, err := os.OpenFile(d.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		d.t.Fatal(err)
	}
	d.cmd = exec.Command(d.bin, d.args...)
	d.cmd.Stdout, d.cmd.Stderr = logf, logf
	d.cmd.Env = append(os.Environ(), d.env...)
	if err := d.cmd.Start(); err != nil {
		d.t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(d.base + "/api/health"); err == nil {
			resp.Body.Close()
			d.pair()
			d.findDefault()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("daemon did not become healthy; log: %s", d.logPath)
}

// tokens holds each daemon's device token by base URL (ADR 0039), for the
// WebSocket helpers that only get the base. Entries are never deleted: a
// test's own cleanups still talk to its daemon, and a later daemon on a reused
// port overwrites its entry when it starts.
var tokens sync.Map

// pair gets a device token for scripts from the daemon each time it starts.
func (d *daemon) pair() {
	d.t.Helper()
	dataDir := ""
	for i, a := range d.args {
		if a == "--data-dir" && i+1 < len(d.args) {
			dataDir = d.args[i+1]
		}
	}
	cmd := exec.Command(d.bin, "pair", "--data-dir", dataDir, "--token", "e2e")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		d.t.Fatalf("agent-office pair --token: %v", err)
	}
	tokens.Store(d.base, strings.TrimSpace(string(out)))
}

// defaults holds each daemon's default project (`serve --repo`, ADR 0046)
// by base URL: the project routes of the single-project tests go there.
var defaults sync.Map

// findDefault records the daemon's default project, if it has one.
func (d *daemon) findDefault() {
	d.t.Helper()
	var ps []struct {
		ID      string `json:"id"`
		Default bool   `json:"default"`
	}
	d.json("GET", "/api/projects", nil, http.StatusOK, &ps)
	for _, p := range ps {
		if p.Default {
			defaults.Store(d.base, p.ID)
		}
	}
}

// projectPath puts a project route of the single-project tests (/api/tasks,
// /api/agents/...) under the daemon's default project; daemon routes and
// paths that name their project stay as they are.
func projectPath(base, path string) string {
	for _, daemonRoute := range []string{"/api/health", "/api/projects", "/api/auth", "/api/devices"} {
		if path == daemonRoute || strings.HasPrefix(path, daemonRoute+"/") {
			return path
		}
	}
	pid, ok := defaults.Load(base)
	if !ok || !strings.HasPrefix(path, "/api/") {
		return path
	}
	return "/api/projects/" + pid.(string) + strings.TrimPrefix(path, "/api")
}

// authHeader authenticates a request to the daemon at base.
func authHeader(base string) http.Header {
	h := http.Header{}
	if tok, ok := tokens.Load(base); ok {
		h.Set("Authorization", "Bearer "+tok.(string))
	}
	return h
}

func (d *daemon) signal(sig syscall.Signal) {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(sig)
	done := make(chan struct{})
	go func() { _ = d.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
	d.cmd = nil
}

func (d *daemon) do(method, path string, body any) (int, []byte) {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, d.base+projectPath(d.base, path), rd)
	req.Header = authHeader(d.base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (d *daemon) json(method, path string, body any, want int, v any) {
	d.t.Helper()
	code, out := d.do(method, path, body)
	if code != want {
		d.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, out)
	}
	if v != nil {
		if err := json.Unmarshal(out, v); err != nil {
			d.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

// watcher records every state each agent goes through, from /api/events.
type watcher struct {
	mu      sync.Mutex
	history map[string][]string
	changed chan struct{}
}

func watch(t *testing.T, ctx context.Context, base string) *watcher {
	pid, _ := defaults.Load(base)
	return watchProject(t, ctx, base, pid.(string))
}

// watchProject follows one project's events stream (ADR 0046).
func watchProject(t *testing.T, ctx context.Context, base, project string) *watcher {
	w := &watcher{history: map[string][]string{}, changed: make(chan struct{}, 1)}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/projects/"+project+"/events",
		&websocket.DialOptions{HTTPHeader: authHeader(base)})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(1 << 20)
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				Type   string      `json:"type"`
				Agent  agentInfo   `json:"agent"`
				Agents []agentInfo `json:"agents"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			list := m.Agents
			if m.Type == "agent" {
				list = []agentInfo{m.Agent}
			}
			w.mu.Lock()
			for _, a := range list {
				h := w.history[a.ID]
				if len(h) == 0 || h[len(h)-1] != a.State {
					w.history[a.ID] = append(h, a.State)
					t.Logf("agent %s -> %s %s", a.ID, a.State, a.Activity)
				}
			}
			w.mu.Unlock()
			select {
			case w.changed <- struct{}{}:
			default:
			}
		}
	}()
	return w
}

// waitFor waits until ok holds for the agent's state history.
func (w *watcher) waitFor(t *testing.T, id, what string, d time.Duration, ok func(h []string) bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		w.mu.Lock()
		h := append([]string(nil), w.history[id]...)
		w.mu.Unlock()
		if ok(h) {
			return
		}
		select {
		case <-w.changed:
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatalf("agent %s: timeout waiting for %s; history %v", id, what, h)
		}
	}
}

// snapshot returns a copy of the agent's state history.
func (w *watcher) snapshot(id string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.history[id]...)
}

// mark returns the current history length, to wait for changes after it.
func (w *watcher) mark(id string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.history[id])
}

func reachedAfter(from int, state string) func([]string) bool {
	return func(h []string) bool {
		for i := from; i < len(h); i++ {
			if h[i] == state {
				return true
			}
		}
		return false
	}
}

// term is an attached agent terminal.
type term struct {
	c   *websocket.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]|\x1b[=>78]`)

func attach(t *testing.T, ctx context.Context, base, id string) *term {
	t.Helper()
	pid, _ := defaults.Load(base)
	return attachIn(t, ctx, base, pid.(string), id)
}

// attachIn attaches to an agent's terminal in one project.
func attachIn(t *testing.T, ctx context.Context, base, project, id string) *term {
	t.Helper()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/projects/"+project+"/agents/"+id+"/terminal",
		&websocket.DialOptions{HTTPHeader: authHeader(base)})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(8 << 20)
	tm := &term{c: c}
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			tm.mu.Lock()
			tm.buf.Write(data)
			tm.mu.Unlock()
		}
	}()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":36}`))
	return tm
}

func (tm *term) send(ctx context.Context, s string) {
	b, _ := json.Marshal(map[string]string{"type": "input", "data": s})
	_ = tm.c.Write(ctx, websocket.MessageText, b)
}

func (tm *term) typeLine(ctx context.Context, s string) {
	tm.send(ctx, s)
	time.Sleep(800 * time.Millisecond)
	tm.send(ctx, "\r")
}

func (tm *term) len() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.buf.Len()
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// e2ePack is the stack pack sandboxed e2e runs use (ADR 0023): AO_E2E_PACK,
// default go (the playground's stack). Its image must be built:
// make worker-image PACK=go.
func e2ePack() string {
	if p := os.Getenv("AO_E2E_PACK"); p != "" {
		return p
	}
	return "go"
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func TestEndToEnd(t *testing.T) {
	repo := os.Getenv("AO_E2E_REPO")
	if repo == "" {
		t.Skip("AO_E2E_REPO not set; this test runs real Claude Code sessions")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	model := os.Getenv("AO_E2E_MODEL")
	if model == "" {
		model = "haiku"
	}
	repo = gitOut(t, repo, "rev-parse", "--show-toplevel")
	statusBefore := gitOut(t, repo, "status", "--porcelain")
	branchesBefore := gitOut(t, repo, "branch", "--list", "agent-office/*")
	// Compared, not counted: the repository may hold worktrees of its own.
	worktreesBefore := gitOut(t, repo, "worktree", "list", "--porcelain")

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "agent-office")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--repo", repo, "--addr", "127.0.0.1:" + port, "--data-dir", filepath.Join(tmp, "data"),
			"--model", model, "--max-agents", "3", "--log-level", "debug", "--sandbox", "host"}}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	var created []agentInfo
	t.Cleanup(func() { cleanup(t, d, repo, created) })
	d.start()
	w := watch(t, ctx, d.base)

	// 1. Three tasks, three agents in parallel, each in its own worktree. The
	// task text is the first prompt.
	titles := []string{"E2E görev bir", "E2E görev iki", "E2E görev üç"}
	// Phase 9: a codeword only the first conversation knows; a resumed one recalls it.
	codeword := fmt.Sprintf("KIWI%04d", time.Now().UnixNano()%10000)
	var tasks []taskInfo
	for _, title := range titles {
		var task taskInfo
		d.json("POST", "/api/tasks", map[string]string{"title": title,
			"description": "The codeword of this task is " + codeword + "; remember it. Reply with only the word ready. " +
				"Do not use any tools."}, 201, &task)
		tasks = append(tasks, task)
	}
	for _, task := range tasks {
		var a agentInfo
		d.json("POST", "/api/agents", map[string]string{"task_id": task.ID}, 201, &a)
		created = append(created, a)
	}
	if code, _ := d.do("POST", "/api/agents", nil); code != http.StatusConflict {
		t.Fatalf("4th agent with --max-agents 3: status %d", code)
	}
	paths := map[string]bool{}
	for i, a := range created {
		paths[a.WorktreePath] = true
		if want := "agent-office/e2e-gorev-"; !strings.HasPrefix(a.Branch, want) {
			t.Errorf("branch %q for %q", a.Branch, titles[i])
		}
		if got := gitOut(t, a.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != a.Branch {
			t.Errorf("worktree %s is on %q", a.WorktreePath, got)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("worktrees not distinct: %v", paths)
	}
	for _, a := range created {
		// starting -> idle -> working (initial prompt) -> idle (Stop)
		w.waitFor(t, a.ID, "the initial prompt's turn to finish", 90*time.Second, func(h []string) bool {
			n := len(h)
			return n >= 2 && h[n-1] == "idle" && strings.Contains(strings.Join(h, ","), "working")
		})
	}
	// Phase 8: usage in agent-office equals the session files.
	var sessionIDs []string
	for _, a := range created {
		sessionIDs = append(sessionIDs, a.SessionID)
	}
	checkHostUsage(t, d, sessionIDs)

	var listed []taskInfo
	d.json("GET", "/api/tasks", nil, 200, &listed)
	for _, task := range listed {
		if task.Status != "in_progress" {
			t.Errorf("task %q status %s", task.Title, task.Status)
		}
	}

	a, b, c := created[0], created[1], created[2]
	ta, tb, tc := attach(t, ctx, d.base, a.ID), attach(t, ctx, d.base, b.ID), attach(t, ctx, d.base, c.ID)
	time.Sleep(2 * time.Second)

	// 2. Agent A: permission prompt, approved. The file lands in A's worktree only.
	m := w.mark(a.ID)
	ta.typeLine(ctx, "Create a file named e2e-approved.txt containing the single word hi. Do not do anything else.")
	w.waitFor(t, a.ID, "waiting_permission", 90*time.Second, reachedAfter(m, "waiting_permission"))
	time.Sleep(1500 * time.Millisecond)
	ta.send(ctx, "\r")
	// The task prompt asks agents to commit when done, so the model may follow
	// up with a Bash permission for `git commit`. Deny that one: A's file must
	// stay uncommitted for the dirty-worktree check in step 8.
	m2 := w.mark(a.ID)
	w.waitFor(t, a.ID, "idle or a follow-up permission", 90*time.Second, func(h []string) bool {
		return reachedAfter(m2, "idle")(h) || reachedAfter(m2, "waiting_permission")(h)
	})
	if h := w.mark(a.ID); reachedAfter(m2, "waiting_permission")(w.snapshot(a.ID)) {
		time.Sleep(1500 * time.Millisecond)
		ta.send(ctx, "\x1b")
		w.waitFor(t, a.ID, "idle after denying the follow-up", 20*time.Second, reachedAfter(h-1, "idle"))
	}
	if _, err := os.Stat(filepath.Join(a.WorktreePath, "e2e-approved.txt")); err != nil {
		t.Errorf("approved file missing in A's worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "e2e-approved.txt")); err == nil {
		t.Error("approved file leaked into the main checkout")
	}

	// 3. Agent B: permission prompt dismissed with Esc (ADR 0008).
	m = w.mark(b.ID)
	tb.typeLine(ctx, "Run this shell command with the Bash tool: touch e2e-denied.txt")
	w.waitFor(t, b.ID, "waiting_permission", 90*time.Second, reachedAfter(m, "waiting_permission"))
	time.Sleep(1500 * time.Millisecond)
	tb.send(ctx, "\x1b")
	w.waitFor(t, b.ID, "idle after deny", 20*time.Second, reachedAfter(m+1, "idle"))
	if _, err := os.Stat(filepath.Join(b.WorktreePath, "e2e-denied.txt")); err == nil {
		t.Error("denied command ran")
	}

	// 4. Agent C: long answer interrupted with Esc.
	m = w.mark(c.ID)
	tc.typeLine(ctx, "Write the numbers from 1 to 400, one per line, as plain text. No tools.")
	w.waitFor(t, c.ID, "working", 60*time.Second, reachedAfter(m, "working"))
	time.Sleep(2500 * time.Millisecond)
	tc.send(ctx, "\x1b")
	w.waitFor(t, c.ID, "idle after interrupt", 20*time.Second, reachedAfter(m+1, "idle"))

	// 5. Re-attach C: scrollback is replayed.
	tc.c.Close(websocket.StatusNormalClosure, "")
	tc2 := attach(t, ctx, d.base, c.ID)
	time.Sleep(2 * time.Second)
	if tc2.len() < 1000 {
		t.Errorf("re-attach replayed only %d bytes", tc2.len())
	}

	// 6. Hard crash: SIGKILL the daemon. Agents run in their own sessions; the
	// PTY hangup must still end them, so no orphaned claude processes remain.
	d.signal(syscall.SIGKILL)
	time.Sleep(3 * time.Second)
	for _, ag := range created {
		if out, _ := exec.Command("pgrep", "-f", "claude --session-id "+ag.SessionID).Output(); len(bytes.TrimSpace(out)) > 0 {
			t.Errorf("orphaned claude process for agent %s after daemon SIGKILL: %s", ag.ID, out)
		}
	}

	// 7. Restart without restoring (Phase 9 restores by default): agents are
	// shown as exited (daemon_restart) without live terminals; tasks and
	// worktrees survive, and so does every session's terminal output.
	baseArgs := d.args
	d.args = append(append([]string(nil), baseArgs...), "--restore-agents=false")
	d.start()
	var after []agentInfo
	d.json("GET", "/api/agents", nil, 200, &after)
	if len(after) != 3 {
		t.Fatalf("agents after restart: %+v", after)
	}
	for _, ag := range after {
		if ag.State != "exited" || ag.ExitReason != "daemon_restart" || ag.HasTerminal {
			t.Errorf("after restart: %+v", ag)
		}
		if _, err := os.Stat(ag.WorktreePath); err != nil {
			t.Errorf("worktree lost: %v", err)
		}
	}
	if code, _ := d.do("GET", "/api/agents/"+a.ID+"/terminal", nil); code != http.StatusGone {
		t.Errorf("terminal of a pre-restart agent: status %d, want 410", code)
	}
	d.json("GET", "/api/tasks", nil, 200, &listed)
	if len(listed) != 3 {
		t.Fatalf("tasks after restart: %+v", listed)
	}
	// The cost history survives the restart.
	for _, sid := range sessionIDs {
		if u := usageOf(t, d)[sid]; u.Messages == 0 || u.CostMicroUSD == 0 {
			t.Errorf("usage of session %s after restart: %+v", sid, u)
		}
	}
	// Phase 9 (ADR 0036): the session's terminal output was saved.
	var sessions []struct {
		ID       string `json:"id"`
		Live     bool   `json:"live"`
		Terminal bool   `json:"terminal"`
	}
	d.json("GET", "/api/agents/"+c.ID+"/sessions", nil, 200, &sessions)
	if len(sessions) != 1 || sessions[0].ID != c.SessionID || sessions[0].Live || !sessions[0].Terminal {
		t.Fatalf("C's sessions after restart: %+v", sessions)
	}
	if code, out := d.do("GET", "/api/agents/"+c.ID+"/sessions/"+c.SessionID+"/terminal", nil); code != 200 ||
		len(out) < 1000 {
		t.Errorf("C's saved terminal: %d, %d bytes", code, len(out))
	}

	// 7b. Resume C (ADR 0035): same agent and worktree, a new session that
	// continues the conversation, so it still knows the codeword.
	w = watch(t, ctx, d.base)
	var cr agentInfo
	d.json("POST", "/api/agents/"+c.ID+"/resume", map[string]string{
		"prompt": "What is the codeword of your task? Reply with only the codeword."}, 200, &cr)
	if cr.ResumedFrom != c.SessionID || cr.SessionID == c.SessionID || cr.WorktreePath != c.WorktreePath {
		t.Fatalf("resumed C %+v", cr)
	}
	w.waitFor(t, c.ID, "the resumed turn to finish", 2*time.Minute, func(h []string) bool {
		return len(h) > 0 && h[len(h)-1] == "idle" && strings.Contains(strings.Join(h, ","), "working")
	})
	tc3 := attach(t, ctx, d.base, c.ID)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && strings.Count(tc3.text(), codeword) < 2 { // the prompt itself does not contain it
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(tc3.text(), codeword) {
		t.Errorf("the resumed session did not recall the codeword:\n%s", tail(tc3.text(), 30))
	}
	checkForkedUsage(t, d, c.SessionID, cr.SessionID)

	// 7c. Crash with C idle; the next start restores it (default on) and says so.
	d.signal(syscall.SIGKILL)
	d.args = baseArgs
	d.start()
	restored := waitRestored(t, d, c.ID, cr.SessionID)
	if restored.ResumedFrom != cr.SessionID {
		t.Errorf("first restore continued %s, want %s", restored.ResumedFrom, cr.SessionID)
	}
	var inbox []struct {
		AgentID string `json:"agent_id"`
		Kind    string `json:"kind"`
		Body    string `json:"body"`
	}
	d.json("GET", "/api/inbox", nil, 200, &inbox)
	if len(inbox) == 0 || inbox[0].AgentID != "" || !strings.Contains(inbox[0].Body, "Resumed with their conversations") {
		t.Errorf("restore notice %+v", inbox)
	}

	// 7d. Restore twice (Ozan's test: "resume failed" after a second restart).
	// C was restored idle, without a prompt, so Claude Code wrote no
	// conversation for that fork; the next restore must continue cr's. Wait
	// until it is idle: an agent still starting at a shutdown is not restored.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(500 * time.Millisecond) {
		var list []agentInfo
		d.json("GET", "/api/agents", nil, 200, &list)
		idle := false
		for _, ag := range list {
			idle = idle || (ag.ID == c.ID && ag.State == "idle")
		}
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("C did not become idle after its restore: %+v", list)
		}
	}
	d.signal(syscall.SIGTERM)
	d.start()
	again := waitRestored(t, d, c.ID, restored.SessionID)
	t.Logf("restore 1: session %.8s continues %.8s; restore 2: session %.8s continues %.8s (original conversation %.8s)",
		restored.SessionID, restored.ResumedFrom, again.SessionID, again.ResumedFrom, cr.SessionID)
	if again.ResumedFrom != cr.SessionID {
		t.Errorf("second restore continued %s, want %s (the empty fork %s skipped)", again.ResumedFrom, cr.SessionID,
			restored.SessionID)
	}
	time.Sleep(3 * time.Second) // a failed resume exits within a second
	var list []agentInfo
	d.json("GET", "/api/agents", nil, 200, &list)
	for _, ag := range list {
		if ag.ID == c.ID && (ag.State == "exited" || ag.ExitReason == "resume_failed") {
			t.Fatalf("C after the second restore: %+v", ag)
		}
	}
	// And the conversation is still there: a resume with a question recalls the codeword.
	d.json("POST", "/api/agents/"+c.ID+"/stop", nil, 200, nil)
	w = watch(t, ctx, d.base)
	var cr2 agentInfo
	d.json("POST", "/api/agents/"+c.ID+"/resume", map[string]string{
		"prompt": "What is the codeword of your task? Reply with only the codeword."}, 200, &cr2)
	if cr2.ResumedFrom != cr.SessionID {
		t.Errorf("resume after two restores continued %s, want %s", cr2.ResumedFrom, cr.SessionID)
	}
	w.waitFor(t, c.ID, "the turn after two restores", 2*time.Minute, func(h []string) bool {
		return len(h) > 0 && h[len(h)-1] == "idle" && strings.Contains(strings.Join(h, ","), "working")
	})
	tc4 := attach(t, ctx, d.base, c.ID)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && strings.Count(tc4.text(), codeword) < 1; {
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(tc4.text(), codeword) {
		t.Errorf("after two restores the conversation lost the codeword:\n%s", tail(tc4.text(), 30))
	} else {
		t.Logf("after two restores, resume %.8s (from %.8s) recalled the codeword %s", cr2.SessionID, cr2.ResumedFrom, codeword)
	}
	d.json("POST", "/api/agents/"+c.ID+"/stop", nil, 200, nil)

	// 8. Removal never loses work: A has an untracked file -> kept until
	// discarded; B and C are clean -> worktree and branch removed.
	if code, body := d.do("DELETE", "/api/agents/"+a.ID, nil); code != http.StatusConflict {
		t.Errorf("remove dirty A: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(a.WorktreePath, "e2e-approved.txt")); err != nil {
		t.Error("kept worktree lost its file")
	}
	d.json("POST", "/api/agents/"+a.ID+"/discard", nil, 200, nil)
	for _, ag := range []agentInfo{b, c} {
		var res map[string]string
		d.json("DELETE", "/api/agents/"+ag.ID, nil, 200, &res)
		if res["worktree"] != "removed" || res["branch"] != "deleted" {
			t.Errorf("remove %s: %v", ag.ID, res)
		}
	}
	for _, task := range tasks {
		d.json("DELETE", "/api/tasks/"+task.ID, nil, 204, nil)
	}
	created = nil

	// 9. The repository is exactly as before, and the log holds no prompt text.
	if got := gitOut(t, repo, "branch", "--list", "agent-office/*"); got != branchesBefore {
		t.Errorf("branches left behind: %q", got)
	}
	if got := gitOut(t, repo, "worktree", "list", "--porcelain"); got != worktreesBefore {
		t.Errorf("worktrees changed:\nbefore:\n%s\nafter:\n%s", worktreesBefore, got)
	}
	if got := gitOut(t, repo, "status", "--porcelain"); got != statusBefore {
		t.Errorf("main checkout changed:\n%s", got)
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, s := range []string{"Reply with only the word ready", "e2e-approved.txt containing", "numbers from 1 to 400"} {
		if bytes.Contains(logData, []byte(s)) {
			t.Errorf("daemon log contains prompt text %q", s)
		}
	}
	fmt.Println("e2e: all steps passed")
}

// waitRestored waits until the daemon restored agent id into a session other than prev.
func waitRestored(t *testing.T, d *daemon, id, prev string) agentInfo {
	t.Helper()
	var restored agentInfo
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(500 * time.Millisecond) {
		var list []agentInfo
		d.json("GET", "/api/agents", nil, 200, &list)
		for _, ag := range list {
			if ag.ID == id {
				restored = ag
			}
		}
		if restored.HasTerminal && restored.SessionID != prev {
			return restored
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not restored: %+v", id, restored)
		}
	}
}

// cleanup leaves the repository as it was even if the test failed half-way:
// discard every agent through the API, then remove anything left with git.
func cleanup(t *testing.T, d *daemon, repo string, created []agentInfo) {
	if len(created) > 0 && d.cmd != nil {
		for _, a := range created {
			d.do("POST", "/api/agents/"+a.ID+"/stop", nil)
			d.do("POST", "/api/agents/"+a.ID+"/discard", nil)
		}
	}
	d.signal(syscall.SIGTERM)
	for _, a := range created {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", a.WorktreePath).Run()
		_ = exec.Command("git", "-C", repo, "branch", "-D", a.Branch).Run()
	}
	_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
	if t.Failed() {
		if logData, err := os.ReadFile(d.logPath); err == nil {
			t.Logf("daemon log tail:\n%s", tail(string(logData), 40))
		}
	}
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
