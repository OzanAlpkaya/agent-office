package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// TestMain lets the test binary act as a fake `claude`: the engine starts
// os.Args[0] with AO_FAKE_CLAUDE=1, and fakeClaude sends hooks the way the
// real CLI does (through the per-agent socket), driven by terminal input.
func TestMain(m *testing.M) {
	if os.Getenv("AO_FAKE_CLAUDE") == "1" {
		os.Exit(fakeClaude())
	}
	os.Exit(m.Run())
}

func fakeClaude() int {
	var sid, resume string
	for i, a := range os.Args {
		if a == "--session-id" && i+1 < len(os.Args) {
			sid = os.Args[i+1]
		}
		if a == "--resume" && i+1 < len(os.Args) && slices.Contains(os.Args, "--fork-session") {
			resume = os.Args[i+1]
		}
	}
	sock := os.Getenv(agent.SocketEnv)
	// send forwards a hook and prints a non-empty reply (a Stop-hook block).
	// AO_FAKE_TRANSCRIPT_DIR: hooks carry transcript_path, as Claude Code's do,
	// and the "usage" command writes the usage fixture there (ADR 0026).
	transcript := ""
	if d := os.Getenv("AO_FAKE_TRANSCRIPT_DIR"); d != "" {
		transcript = filepath.Join(d, sid+".jsonl")
	}
	// AO_FAKE_CONV_DIR models where Claude Code keeps conversations (checked
	// against 2.1.319): a session has one once it gets a message (its initial
	// prompt or a submitted prompt); a fork that was only shown has none, and
	// --resume of a session without one fails.
	convDir := os.Getenv("AO_FAKE_CONV_DIR")
	hasConversation := func(id string) bool {
		_, err := os.Stat(filepath.Join(convDir, id))
		return convDir == "" || err == nil
	}
	markConversation := func() {
		if convDir != "" {
			_ = os.WriteFile(filepath.Join(convDir, sid), nil, 0o600)
		}
	}
	send := func(event string, extra string) {
		if event == "UserPromptSubmit" {
			markConversation()
		}
		if transcript != "" {
			extra += fmt.Sprintf(`,"transcript_path":%q`, transcript)
		}
		payload := fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q%s}`, event, sid, extra)
		// Below Claude Code's own 10 s hook timeout; a busy test machine (several
		// -race runs at once) needs more than a couple of seconds.
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var reply strings.Builder
		_ = hooks.Forward(ctx, sock, strings.NewReader(payload), &reply)
		if reply.Len() > 0 {
			fmt.Println("STOP-REPLY:" + reply.String())
		}
	}
	if os.Getenv("AO_FAKE_BAD_SESSION") == "1" {
		sid = "00000000-0000-4000-8000-000000000000"
	}
	if resume != "" {
		if _, err := os.Stat(".fake-no-conversation"); err == nil || !hasConversation(resume) {
			// What Claude Code 2.1.319 does for an unknown session: no SessionStart.
			fmt.Println("No conversation found with session ID: " + resume)
			return 1
		}
		fmt.Println("RESUMED:" + resume)
	}
	fmt.Println("fake claude ready")
	if n := len(os.Args); n >= 2 && os.Args[n-2] == "--" {
		markConversation() // the initial prompt is the conversation's first message
	}
	if n := len(os.Args); os.Getenv("AO_FAKE_PROMPT") == "1" && n >= 2 && os.Args[n-2] == "--" {
		// The initial prompt, on one line, so tests can look for it on screen.
		fmt.Println("PROMPT:" + strings.ReplaceAll(os.Args[n-1], "\n", " | "))
	}
	if resume != "" {
		send("SessionStart", `,"source":"fork"`)
	} else {
		send("SessionStart", `,"source":"startup"`)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		switch strings.Trim(in.Text(), " \t\r\x1b") {
		case "work":
			send("UserPromptSubmit", `,"prompt":"SENSITIVE"`)
			send("PreToolUse", `,"tool_name":"Write"`)
			send("PermissionRequest", `,"tool_name":"Write"`)
		case "approve":
			send("PostToolUse", `,"tool_name":"Write"`)
			send("SubagentStop", `,"agent_id":"bg-1"`)
			send("Stop", `,"stop_hook_active":false`)
		case "dirty":
			_ = os.WriteFile("agent-output.txt", []byte("work in progress\n"), 0o644)
			fmt.Println("wrote file")
		case "exit":
			send("SessionEnd", `,"reason":"prompt_input_exit"`)
			return 0
		case "crash":
			return 7 // exit without a SessionEnd hook
		case "killself": // killed by a signal, as by the OOM killer or podman kill
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			time.Sleep(time.Second)
		case "usage": // a turn whose transcript and subagent transcript hold the usage fixture
			src := os.Getenv("AO_FAKE_TRANSCRIPT_SRC")
			main, _ := os.ReadFile(filepath.Join(src, usageFixtureSession+".jsonl"))
			sub, _ := os.ReadFile(filepath.Join(src, usageFixtureSession, "subagents", "agent-a1b2c3.jsonl"))
			subDir := filepath.Join(filepath.Dir(transcript), sid, "subagents")
			_ = os.MkdirAll(subDir, 0o700)
			_ = os.WriteFile(filepath.Join(subDir, "agent-a1b2c3.jsonl"), sub, 0o600)
			_ = os.WriteFile(transcript, main, 0o600)
			send("UserPromptSubmit", `,"prompt":"SENSITIVE"`)
			send("PreToolUse", `,"tool_name":"Bash"`)
			send("PostToolUse", `,"tool_name":"Bash"`)
			send("SubagentStop", `,"agent_id":"a1b2c3"`)
			send("Stop", `,"stop_hook_active":false`)
		case "usage-fork": // a resumed session's transcript: the parent's records copied, one new message, a cumulative cost-state (ADR 0035)
			src := os.Getenv("AO_FAKE_TRANSCRIPT_SRC")
			main, _ := os.ReadFile(filepath.Join(src, usageFixtureSession+".jsonl"))
			main = append(main, []byte(`{"type":"assistant","requestId":"req_01DDDD","message":{"id":"msg_01DDDD","model":"claude-haiku-5-5","role":"assistant","content":[],"usage":{"input_tokens":100,"output_tokens":50}}}`+"\n"+
				`{"type":"cost-state","totalCostUSD":0.069864}`+"\n")...)
			_ = os.WriteFile(transcript, main, 0o600)
			send("UserPromptSubmit", `,"prompt":"SENSITIVE"`)
			send("Stop", `,"stop_hook_active":false`)
		case "usage-late": // the Stop hook runs before the transcript is written, as seen in the e2e run
			src := os.Getenv("AO_FAKE_TRANSCRIPT_SRC")
			main, _ := os.ReadFile(filepath.Join(src, usageFixtureSession+".jsonl"))
			send("UserPromptSubmit", `,"prompt":"SENSITIVE"`)
			send("Stop", `,"stop_hook_active":false`)
			time.Sleep(300 * time.Millisecond)
			_ = os.WriteFile(transcript, main, 0o600)
			fmt.Println("transcript written")
		case "busy": // a turn that keeps running
			send("UserPromptSubmit", "")
		case "turn": // a whole turn
			send("UserPromptSubmit", "")
			send("Stop", `,"stop_hook_active":false`)
		case "stop":
			send("Stop", `,"stop_hook_active":false`)
		case "stopactive":
			send("Stop", `,"stop_hook_active":true`)
		case "substop":
			send("Stop", `,"agent_id":"sub-1","stop_hook_active":false`)
		case "stopfailure":
			send("StopFailure", "")
		case "idlenote":
			send("Notification", `,"notification_type":"idle_prompt"`)
		case "commit":
			_ = os.WriteFile("work.txt", []byte("done\n"), 0o644)
			for _, args := range [][]string{{"add", "work.txt"}, {"-c", "user.name=t", "-c", "user.email=t@example.com",
				"commit", "-q", "-m", "feat: work"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					fmt.Printf("git failed: %v %s\n", err, out)
				}
			}
			fmt.Println("committed")
		case "commit-more": // another commit on top
			f, _ := os.OpenFile("work.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			fmt.Fprintf(f, "more %d\n", time.Now().UnixNano())
			f.Close()
			for _, args := range [][]string{{"add", "work.txt"}, {"-c", "user.name=t", "-c", "user.email=t@example.com",
				"commit", "-q", "-m", "fix: review feedback"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					fmt.Printf("git failed: %v %s\n", err, out)
				}
			}
			fmt.Println("committed more")
		case "commitall": // commit everything written with "write"
			for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com",
				"commit", "-q", "-m", "feat: work"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					fmt.Printf("git failed: %v %s\n", err, out)
				}
			}
			fmt.Println("committed all")
		default:
			if rev, ok := strings.CutPrefix(strings.TrimSpace(in.Text()), "merge "); ok {
				out, err := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "merge",
					"--no-edit", rev).CombinedOutput()
				fmt.Printf("merge done (err=%v): %s\n", err != nil, strings.ReplaceAll(string(out), "\n", " "))
				continue
			}
			if rest, ok := strings.CutPrefix(strings.TrimSpace(in.Text()), "write "); ok {
				// write <path> <text>: the file's whole content becomes <text> plus a newline
				name, text, _ := strings.Cut(rest, " ")
				_ = os.MkdirAll(filepath.Dir(name), 0o755)
				if err := os.WriteFile(name, []byte(text+"\n"), 0o644); err != nil {
					fmt.Println("write failed:", err)
				} else {
					fmt.Println("wrote " + name)
				}
				continue
			}
			if l := strings.TrimSpace(in.Text()); l != "" {
				fmt.Println("INPUT:" + l) // what the agent read, e.g. a message typed by the daemon
			}
		}
	}
	return 0
}

type fixture struct {
	repo  string
	root  string // worktree root
	dbp   string
	store *store.Store
	opts  Options
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "README.md")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T, extraEnv ...string) *fixture {
	t.Helper()
	runtime, err := os.MkdirTemp("", "ao-eng-") // short path: unix socket length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	f := &fixture{repo: newRepo(t), root: filepath.Join(t.TempDir(), "worktrees"), dbp: filepath.Join(t.TempDir(), "ao.db")}
	f.store, err = store.Open(context.Background(), f.dbp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.opts = Options{
		Repo: f.repo, WorktreeRoot: f.root, Store: f.store, MaxAgents: 3,
		ClaudeBin: os.Args[0], HookCommand: "true", RuntimeDir: runtime,
		BaseEnv: append([]string{"AO_FAKE_CLAUDE=1", "PATH=" + os.Getenv("PATH"), "AO_FAKE_CONV_DIR=" + t.TempDir()},
			extraEnv...),
		StopTimeout: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AgentOfficeBin: os.Args[0],
	}
	return f
}

func (f *fixture) engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(context.Background(), f.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	return e
}

// waitAgent consumes updates until agent id reaches state want.
func waitAgent(t *testing.T, updates <-chan Update, id string, want hooks.State) AgentInfo {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatalf("updates closed while waiting for %s", want)
			}
			if u.Type == "agent" && u.Agent.ID == id && u.Agent.State == string(want) {
				return *u.Agent
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %s to reach %s", id, want)
		}
	}
}

func typeLine(t *testing.T, e *Engine, id, line string) {
	t.Helper()
	if err := e.Input(id, []byte(line+"\n")); err != nil {
		t.Fatal(err)
	}
}

func TestStartCreatesWorktreeAndFollowsHooks(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	updates, cancel := e.Subscribe()
	defer cancel()

	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch != "agent-office/"+a.ID || a.WorktreePath != filepath.Join(f.root, e.Project().Slug, a.ID) {
		t.Fatalf("branch/path: %+v", a)
	}
	if got := gitIn(t, a.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != a.Branch {
		t.Fatalf("worktree on %q", got)
	}
	waitAgent(t, updates, a.ID, hooks.Idle)
	typeLine(t, e, a.ID, "work")
	if got := waitAgent(t, updates, a.ID, hooks.WaitingPermission); got.Activity != "Write" {
		t.Fatalf("activity %q", got.Activity)
	}
	typeLine(t, e, a.ID, "approve")
	waitAgent(t, updates, a.ID, hooks.Idle) // the SubagentStop in between must not interfere
	typeLine(t, e, a.ID, "exit")
	waitAgent(t, updates, a.ID, hooks.Exited)

	// State and reduced events are persisted; prompt text is not.
	var events int
	_ = f.store.DB.QueryRow("SELECT count(*) FROM events WHERE agent_id = ?", a.ID).Scan(&events)
	if events < 6 {
		t.Fatalf("only %d events persisted", events)
	}
	var leaked int
	_ = f.store.DB.QueryRow("SELECT count(*) FROM events WHERE kind || hook || tool || detail LIKE '%SENSITIVE%'").Scan(&leaked)
	if leaked != 0 {
		t.Fatal("prompt text persisted")
	}

	res, err := e.Remove(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res != (RemoveResult{Worktree: "removed", Branch: "deleted"}) {
		t.Fatalf("remove result %+v", res)
	}
	if _, err := os.Stat(a.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worktree folder still exists")
	}
	if len(e.List()) != 0 {
		t.Fatal("agent still listed")
	}
}

func TestTaskAgentBranchPromptAndStatus(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	task, err := e.CreateTask(ctx, TaskInput{Title: "Şifre sıfırlama ekle", Description: "--details: use a token"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if want := "agent-office/sifre-sifirlama-ekle-" + a.ID[:6]; a.Branch != want || a.TaskID != task.ID {
		t.Fatalf("branch %q task %q", a.Branch, a.TaskID)
	}
	tasks, _ := e.ListTasks(ctx)
	if tasks[0].Status != StatusInProgress {
		t.Fatalf("task status %s", tasks[0].Status)
	}
	term, _ := e.Terminal(a.ID)
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", term.Pid()))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if n := len(args); args[n-2] != "--" || !strings.Contains(args[n-1], "Task: Şifre sıfırlama ekle") ||
		!strings.Contains(args[n-1], a.Branch) {
		t.Fatalf("initial prompt not passed after --: %q", args[max(0, n-2):])
	}
	if err := e.DeleteTask(ctx, task.ID); !errors.Is(err, ErrTaskHasRunning) {
		t.Fatalf("delete task with running agent: %v", err)
	}
}

func TestMaxAgents(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	var ids []string
	for range 3 {
		a, err := e.Start(ctx, StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	paths := map[string]bool{}
	for _, a := range e.List() {
		paths[a.WorktreePath] = true
	}
	if len(paths) != 3 {
		t.Fatalf("worktrees not distinct: %v", paths)
	}
	if _, err := e.Start(ctx, StartRequest{}); !errors.Is(err, ErrTooManyAgents) {
		t.Fatalf("4th start: %v", err)
	}
	if err := e.Stop(ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, StartRequest{}); err != nil {
		t.Fatalf("start after a stop: %v", err)
	}
}

func TestRemoveKeepsWork(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	updates, cancel := e.Subscribe()
	defer cancel()

	// Agent 1: commits on its branch -> worktree removed, branch kept.
	a1, _ := e.Start(ctx, StartRequest{})
	waitAgent(t, updates, a1.ID, hooks.Idle)
	if err := os.WriteFile(filepath.Join(a1.WorktreePath, "feature.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, a1.WorktreePath, "add", "feature.txt")
	gitIn(t, a1.WorktreePath, "commit", "-q", "-m", "feature")
	_ = e.Stop(a1.ID)
	res, err := e.Remove(ctx, a1.ID)
	if err != nil || res != (RemoveResult{Worktree: "removed", Branch: "kept"}) {
		t.Fatalf("remove committed: %+v %v", res, err)
	}
	if gitIn(t, f.repo, "branch", "--list", a1.Branch) == "" {
		t.Fatal("branch with commits was deleted")
	}

	// Agent 2: uncommitted file -> kept until discarded.
	a2, _ := e.Start(ctx, StartRequest{})
	waitAgent(t, updates, a2.ID, hooks.Idle)
	typeLine(t, e, a2.ID, "dirty")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(a2.WorktreePath, "agent-output.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake agent did not write its file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := e.Remove(ctx, a2.ID); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("remove running: %v", err)
	}
	gitIn(t, f.repo, "config", "user.name", "Ozan Test") // the exit commits as the user (ADR 0051)
	gitIn(t, f.repo, "config", "user.email", "ozan@example.com")
	_ = e.Stop(a2.ID)
	e.mu.Lock()
	exitWork := e.agents[a2.ID].afterExit
	e.mu.Unlock()
	<-exitWork
	// The exit saved the work as a commit; a change made after it (by hand,
	// say) keeps the worktree.
	if err := os.WriteFile(filepath.Join(a2.WorktreePath, "late.txt"), []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Remove(ctx, a2.ID); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("remove dirty: %v", err)
	}
	if got, _ := e.Get(a2.ID); got.WorktreeStatus != WorktreeKept {
		t.Fatalf("worktree status %s", got.WorktreeStatus)
	}
	if _, err := os.Stat(filepath.Join(a2.WorktreePath, "late.txt")); err != nil {
		t.Fatal("uncommitted work was deleted")
	}
	if files := gitIn(t, f.repo, "show", "--name-only", "--format=", a2.Branch); files != "agent-output.txt" {
		t.Fatalf("saved on exit: %q", files)
	}
	res, err = e.Discard(ctx, a2.ID)
	if err != nil || res != (RemoveResult{Worktree: "removed", Branch: "kept"}) {
		t.Fatalf("discard: %+v %v", res, err)
	}
	if _, err := os.Stat(a2.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("discarded worktree still exists")
	}
}

func TestRestartMarksAgentsAndKeepsTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := e1.CreateTask(ctx, TaskInput{Title: "Persist me"})
	a, err := e1.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: the row says "working" and nobody marked it exited.
	e1.Shutdown()
	if err := f.store.Q.UpdateAgentState(ctx, db.UpdateAgentStateParams{State: "working", ID: a.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec("UPDATE agents SET exited_at = NULL, exit_reason = '' WHERE id = ?", a.ID); err != nil {
		t.Fatal(err)
	}

	e2 := f.engine(t)
	got, err := e2.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != string(hooks.Exited) || got.ExitReason != ExitDaemonRestart || got.HasTerminal || got.Name != a.Name {
		t.Fatalf("after restart: %+v", got)
	}
	if _, err := e2.Terminal(a.ID); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("terminal after restart: %v", err)
	}
	if _, err := os.Stat(got.WorktreePath); err != nil {
		t.Fatal("worktree lost on restart")
	}
	tasks, _ := e2.ListTasks(ctx)
	if len(tasks) != 1 || tasks[0].Title != "Persist me" {
		t.Fatalf("tasks after restart: %+v", tasks)
	}
	if _, err := e2.Remove(ctx, a.ID); err != nil {
		t.Fatalf("remove after restart: %v", err)
	}
}

func TestReconcileMarksMissingWorktreeRemoved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, _ := New(ctx, f.opts)
	a, _ := e1.Start(ctx, StartRequest{})
	e1.Shutdown()
	if err := os.RemoveAll(a.WorktreePath); err != nil {
		t.Fatal(err)
	}
	e2 := f.engine(t)
	got, _ := e2.Get(a.ID)
	if got.WorktreeStatus != WorktreeRemoved {
		t.Fatalf("status %s", got.WorktreeStatus)
	}
	if list := gitIn(t, f.repo, "worktree", "list"); strings.Contains(list, a.ID) {
		t.Fatalf("prune did not run: %s", list)
	}
}

func TestTaskValidationAndPatch(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	for _, in := range []TaskInput{{Title: ""}, {Title: "   "}, {Title: strings.Repeat("ş", MaxTitleLen+1)},
		{Title: "ok", Description: strings.Repeat("x", MaxDescriptionLen+1)}} {
		if _, err := e.CreateTask(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateTask(%.20q): %v", in.Title, err)
		}
	}
	task, err := e.CreateTask(ctx, TaskInput{Title: "Line one\nline two", Description: "  body  "})
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Line one line two" || task.Description != "body" || task.Status != StatusTodo {
		t.Fatalf("normalized: %+v", task)
	}
	if _, err := e.CreateTask(ctx, TaskInput{Title: strings.Repeat("ş", MaxTitleLen)}); err != nil {
		t.Fatalf("200 multi-byte characters must fit: %v", err)
	}
	bad := "doing"
	if _, err := e.UpdateTask(ctx, task.ID, TaskPatch{Status: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
	review := StatusReview
	got, err := e.UpdateTask(ctx, task.ID, TaskPatch{Status: &review})
	if err != nil || got.Status != StatusReview || got.Title != task.Title {
		t.Fatalf("patch: %+v %v", got, err)
	}
	if _, err := e.UpdateTask(ctx, "missing", TaskPatch{Status: &review}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
	if err := e.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCrashWithoutSessionEndStillExits(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	updates, cancel := e.Subscribe()
	defer cancel()
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitAgent(t, updates, a.ID, hooks.Idle)
	typeLine(t, e, a.ID, "crash")
	got := waitAgent(t, updates, a.ID, hooks.Exited)
	if got.ExitCode == nil || *got.ExitCode != 7 || got.ExitReason != ExitProcess {
		t.Fatalf("exit: %+v", got)
	}
}

func TestUserInterruptLeavesWaitingState(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	updates, cancel := e.Subscribe()
	defer cancel()
	a, _ := e.Start(context.Background(), StartRequest{})
	waitAgent(t, updates, a.ID, hooks.Idle)
	typeLine(t, e, a.ID, "work")
	waitAgent(t, updates, a.ID, hooks.WaitingPermission)
	if err := e.Input(a.ID, []byte("\x1b")); err != nil { // Esc dismisses the permission prompt
		t.Fatal(err)
	}
	waitAgent(t, updates, a.ID, hooks.Idle)
	typeLine(t, e, a.ID, "work")
	waitAgent(t, updates, a.ID, hooks.WaitingPermission)
	_ = e.Input(a.ID, []byte("\x1b[A")) // arrow key: not an interrupt
	if got, _ := e.Get(a.ID); got.State != string(hooks.WaitingPermission) {
		t.Fatalf("arrow key changed state to %s", got.State)
	}
}

func TestMismatchedSessionIsIgnored(t *testing.T) {
	f := newFixture(t, "AO_FAKE_BAD_SESSION=1")
	e := f.engine(t)
	a, _ := e.Start(context.Background(), StartRequest{})
	time.Sleep(500 * time.Millisecond) // the fake sends SessionStart right away
	if got, _ := e.Get(a.ID); got.State != string(hooks.Starting) {
		t.Fatalf("hook with foreign session_id changed state to %s", got.State)
	}
}

func TestAgentEnvIsScrubbed(t *testing.T) {
	f := newFixture(t, "CLAUDECODE=1", "CLAUDE_CODE_MESSAGING_TOKEN=secret")
	e := f.engine(t)
	a, _ := e.Start(context.Background(), StartRequest{})
	term, _ := e.Terminal(a.ID)
	// Until the child has exec'd, its environ is still the test process's.
	hasSocket := func(kv string) bool { return strings.HasPrefix(kv, agent.SocketEnv+"=") }
	var env []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", term.Pid()))
		if err != nil {
			t.Fatal(err)
		}
		if env = strings.Split(string(raw), "\x00"); slices.ContainsFunc(env, hasSocket) {
			break
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_") {
			t.Fatalf("leaked into agent env: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if !slices.ContainsFunc(env, hasSocket) {
		t.Fatal("socket env missing")
	}
}

func TestNewValidates(t *testing.T) {
	f := newFixture(t)
	bad := f.opts
	bad.Repo = "relative"
	if _, err := New(context.Background(), bad); err == nil {
		t.Fatal("relative repo accepted")
	}
	bad = f.opts
	bad.Repo = t.TempDir() // not a git repository
	if _, err := New(context.Background(), bad); err == nil {
		t.Fatal("non-git directory accepted")
	}
}

func TestTaskPrompt(t *testing.T) {
	p := taskPrompt("Add export", "CSV only.", "agent-office/add-export-abc123")
	for _, want := range []string{
		"Task: Add export\n",
		"\nCSV only.\n",
		"on branch agent-office/add-export-abc123.",
		"commit your changes on this branch",
		"Do not push.",
		"Do not touch any other branch",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(taskPrompt("T", "", "b"), "\n\n\n") {
		t.Error("empty description leaves a blank block")
	}
}

func TestSandboxEventsListsOnlySandboxEventsNewestFirst(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	updates, cancel := e.Subscribe()
	defer cancel()
	a, err := e.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitAgent(t, updates, a.ID, hooks.Idle)
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i, ev := range []db.InsertEventParams{
		{Kind: "egress_denied", Hook: "sandbox", Detail: "github.com:443"},
		{Kind: "Stop", Hook: "Stop"}, // a hook event: not listed
		{Kind: "api_rejected", Hook: "sandbox", Detail: "GET /v1/models"},
	} {
		ev.AgentID, ev.SessionID, ev.State, ev.At = a.ID, a.SessionID, "idle", at.Add(time.Duration(i)*time.Second)
		if err := f.store.Q.InsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.SandboxEvents(ctx, a.ID, 10)
	if err != nil || len(got) != 2 || got[0].Detail != "GET /v1/models" || got[1].Kind != sandbox.EgressDenied ||
		!got[1].At.Equal(at) {
		t.Fatalf("events %+v %v", got, err)
	}
	if got, _ := e.SandboxEvents(ctx, a.ID, 1); len(got) != 1 {
		t.Fatalf("limit 1: %+v", got)
	}
	if _, err := e.SandboxEvents(ctx, "nope", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, err := e.SandboxEvents(ctx, a.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit 0: %v", err)
	}
}
