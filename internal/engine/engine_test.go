package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
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
	var sid string
	for i, a := range os.Args {
		if a == "--session-id" && i+1 < len(os.Args) {
			sid = os.Args[i+1]
		}
	}
	sock := os.Getenv(agent.SocketEnv)
	send := func(event string, extra string) {
		payload := fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q%s}`, event, sid, extra)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hooks.Forward(ctx, sock, strings.NewReader(payload), io.Discard)
	}
	if os.Getenv("AO_FAKE_BAD_SESSION") == "1" {
		sid = "00000000-0000-4000-8000-000000000000"
	}
	fmt.Println("fake claude ready")
	send("SessionStart", `,"source":"startup"`)
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
		case "exit":
			send("SessionEnd", `,"reason":"prompt_input_exit"`)
			return 0
		case "crash":
			return 7 // exit without a SessionEnd hook
		}
	}
	return 0
}

func newEngine(t *testing.T, extraEnv ...string) *Engine {
	t.Helper()
	runtime, err := os.MkdirTemp("", "ao-eng-") // short path: unix socket length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	e, err := New(Options{
		Repo:        t.TempDir(),
		ClaudeBin:   os.Args[0],
		HookCommand: "true",
		RuntimeDir:  runtime,
		BaseEnv:     append([]string{"AO_FAKE_CLAUDE=1", "PATH=" + os.Getenv("PATH")}, extraEnv...),
		StopTimeout: time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	return e
}

// waitState consumes updates until the agent reaches want.
func waitState(t *testing.T, updates <-chan AgentInfo, want hooks.State) AgentInfo {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case a, ok := <-updates:
			if !ok {
				t.Fatalf("updates closed while waiting for %s", want)
			}
			if a.State == string(want) {
				return a
			}
		case <-timeout:
			t.Fatalf("timeout waiting for state %s", want)
		}
	}
}

func typeLine(t *testing.T, e *Engine, id, line string) {
	t.Helper()
	term, err := e.Terminal(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleFollowsHooks(t *testing.T) {
	e := newEngine(t)
	updates, cancel := e.Subscribe()
	defer cancel()

	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	if a.State != string(hooks.Starting) || a.SessionID == "" {
		t.Fatalf("initial %+v", a)
	}
	if _, err := e.Start(); !errors.Is(err, ErrAgentRunning) {
		t.Fatalf("second Start: %v", err)
	}

	waitState(t, updates, hooks.Idle)
	typeLine(t, e, a.ID, "work")
	got := waitState(t, updates, hooks.WaitingPermission)
	if got.Activity != "Write" {
		t.Fatalf("activity %q", got.Activity)
	}
	typeLine(t, e, a.ID, "approve")
	waitState(t, updates, hooks.Idle) // the SubagentStop in between must not interfere
	typeLine(t, e, a.ID, "exit")
	got = waitState(t, updates, hooks.Exited)
	if got.ExitCode == nil || *got.ExitCode != 0 {
		// The SessionEnd hook may win the race; the process-exit update follows.
		got = waitFor(t, updates, func(a AgentInfo) bool { return a.ExitCode != nil })
	}
	if *got.ExitCode != 0 {
		t.Fatalf("exit code %d", *got.ExitCode)
	}

	entries, _ := os.ReadDir(e.opts.RuntimeDir + "/agents")
	if len(entries) != 0 {
		t.Fatalf("socket/settings not cleaned up: %v", entries)
	}
	if err := e.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.List()) != 0 {
		t.Fatal("agent not removed")
	}
}

func waitFor(t *testing.T, updates <-chan AgentInfo, ok func(AgentInfo) bool) AgentInfo {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case a := <-updates:
			if ok(a) {
				return a
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
}

func TestUserInterruptLeavesWaitingState(t *testing.T) {
	e := newEngine(t)
	updates, cancel := e.Subscribe()
	defer cancel()
	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, updates, hooks.Idle)
	typeLine(t, e, a.ID, "work")
	waitState(t, updates, hooks.WaitingPermission)
	if err := e.Input(a.ID, []byte("\x1b")); err != nil { // Esc dismisses the permission prompt
		t.Fatal(err)
	}
	waitState(t, updates, hooks.Idle)
	// Escape sequences such as arrow keys are not interrupts.
	typeLine(t, e, a.ID, "work")
	waitState(t, updates, hooks.WaitingPermission)
	_ = e.Input(a.ID, []byte("\x1b[A"))
	if got, _ := e.Get(a.ID); got.State != string(hooks.WaitingPermission) {
		t.Fatalf("arrow key changed state to %s", got.State)
	}
	if err := e.Input("missing", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Input unknown agent: %v", err)
	}
}

func TestCrashWithoutSessionEndStillExits(t *testing.T) {
	e := newEngine(t)
	updates, cancel := e.Subscribe()
	defer cancel()
	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, updates, hooks.Idle)
	typeLine(t, e, a.ID, "crash")
	got := waitState(t, updates, hooks.Exited)
	if got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("exit code %v", got.ExitCode)
	}
	// A new agent can start once the old one exited.
	if _, err := e.Start(); err != nil {
		t.Fatalf("Start after exit: %v", err)
	}
}

func TestMismatchedSessionIsIgnored(t *testing.T) {
	e := newEngine(t, "AO_FAKE_BAD_SESSION=1")
	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // the fake sends SessionStart right away
	got, _ := e.Get(a.ID)
	if got.State != string(hooks.Starting) {
		t.Fatalf("hook with foreign session_id changed state to %s", got.State)
	}
}

func TestStopAndRemove(t *testing.T) {
	e := newEngine(t)
	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(a.ID); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("Remove running agent: %v", err)
	}
	if err := e.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(a.ID)
	if got.State != string(hooks.Exited) || got.ExitedAt == nil {
		t.Fatalf("after Stop: %+v", got)
	}
	if err := e.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stop unknown: %v", err)
	}
}

func TestAgentEnvIsScrubbed(t *testing.T) {
	e := newEngine(t, "CLAUDECODE=1", "CLAUDE_CODE_MESSAGING_TOKEN=secret")
	a, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	term, _ := e.Terminal(a.ID)
	// Read the fake's environment from /proc while it runs.
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", term.Pid()))
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(raw), "\x00")
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_") {
			t.Fatalf("leaked into agent env: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, agent.SocketEnv+"=") }) {
		t.Fatal("socket env missing")
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{Repo: "relative", RuntimeDir: "/tmp/x", ClaudeBin: "c", HookCommand: "h"}); err == nil {
		t.Fatal("relative repo accepted")
	}
	if _, err := New(Options{Repo: "/definitely/missing", RuntimeDir: t.TempDir(), ClaudeBin: "c", HookCommand: "h"}); err == nil {
		t.Fatal("missing repo accepted")
	}
}
