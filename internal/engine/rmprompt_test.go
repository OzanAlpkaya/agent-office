package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// asSandboxed makes a running fake agent look sandboxed, with its workspace
// at a temp dir (the fake runs on the host; the rm decision only reads the
// agent's sandbox mode and the workspace's host path).
func asSandboxed(t *testing.T, e *Engine, id string) string {
	t.Helper()
	ws := t.TempDir()
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.agents[id]
	mode, path := a.info.Sandbox, a.info.WorktreePath
	a.info.Sandbox, a.sandbox, a.info.WorktreePath = SandboxPodman, &sandboxRes{}, ws
	t.Cleanup(func() { // before the engine shuts down: it ends a host agent
		e.mu.Lock()
		defer e.mu.Unlock()
		a.info.Sandbox, a.sandbox, a.info.WorktreePath = mode, nil, path
	})
	return ws
}

func permissionPayload(t *testing.T, e *Engine, id, command, cwd, mode string) []byte {
	t.Helper()
	a, err := e.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "session_id": a.SessionID, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command, "description": "x"}, "cwd": cwd, "permission_mode": mode,
		"permission_suggestions": []any{}})
	return b
}

func callHook(t *testing.T, e *Engine, id string, payload []byte) []byte {
	t.Helper()
	reply, err := e.hookHandler(id, e.log)(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func decisionOf(t *testing.T, reply []byte) (behavior, message string) {
	t.Helper()
	if len(reply) == 0 {
		return "", ""
	}
	var r struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(reply, &r); err != nil || r.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Fatalf("reply %s: %v", reply, err)
	}
	return r.HookSpecificOutput.Decision.Behavior, r.HookSpecificOutput.Decision.Message
}

// Phase 20 (ADR 0065): Claude Code's dangerous rm prompts in the sandbox are
// approved when every target is inside the workspace and not .git, sent to
// the goal's Tech Lead otherwise, and left to the user where no lead decides.
func TestDangerousRmPrompts(t *testing.T) {
	oldWait := commandWait
	t.Cleanup(func() { commandWait = oldWait })
	tf := newTeam(t)
	e, ctx := tf.e, context.Background()
	typeLine(t, e, tf.lead.ID, "turn") // its first turn ends: messages are typed into its terminal from now on
	_, w := tf.worker(t, "Clean the renders")
	ws := asSandboxed(t, e, w.ID)
	if err := os.MkdirAll(filepath.Join(ws, "build"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Every target inside the workspace: allowed at once, and the worker stays as it is.
	before, _ := e.Get(w.ID)
	b, _ := decisionOf(t, callHook(t, e, w.ID, permissionPayload(t, e, w.ID, `cd build && rm -rf *`, "/workspace", "bypassPermissions")))
	if b != "allow" {
		t.Fatalf("auto: %q", b)
	}
	if a, _ := e.Get(w.ID); a.State != before.State {
		t.Fatalf("an approved prompt changed the state to %s", a.State)
	}
	evs, _ := e.q.ListEvents(ctx, db.ListEventsParams{AgentID: w.ID, Limit: 50})
	if !strings.Contains(eventDetails(evs), permissionAutoNote) {
		t.Fatalf("no %s event: %s", permissionAutoNote, eventDetails(evs))
	}
	// A symlinked directory on the way is not approved by itself.
	if err := os.Symlink("/etc", filepath.Join(ws, "etc-link")); err != nil {
		t.Fatal(err)
	}

	// 2. Not resolvable: the Tech Lead decides while the hook is held.
	decide := func(cmd string, approve bool) (string, string, mcp.DecideCommandOutput) {
		t.Helper()
		type res struct{ b, m string }
		done := make(chan res, 1)
		go func() {
			bh, m := decisionOf(t, callHook(t, e, w.ID, permissionPayload(t, e, w.ID, cmd, "/workspace", "bypassPermissions")))
			done <- res{bh, m}
		}()
		var id int64
		deadline := time.Now().Add(5 * time.Second)
		for id == 0 && time.Now().Before(deadline) {
			e.mu.Lock()
			for k, r := range e.commands {
				if r.agentID == w.ID {
					id = k
				}
			}
			e.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		if id == 0 {
			t.Fatalf("%q: no request for the lead", cmd)
		}
		if a, _ := e.Get(w.ID); a.State == string(hooks.WaitingPermission) || a.Activity != "Bash: the Tech Lead decides" {
			t.Fatalf("while the lead decides: %s %q", a.State, a.Activity)
		}
		// Only its lead decides, with a reason.
		if _, err := tf.team.DecideCommand(ctx, w.ID, mcp.DecideCommandInput{RequestID: id, Approve: true, Reason: "x"}); err == nil {
			t.Fatal("a worker decided")
		}
		if _, err := tf.team.DecideCommand(ctx, tf.lead.ID, mcp.DecideCommandInput{RequestID: id, Approve: true}); err == nil {
			t.Fatal("no reason")
		}
		out, err := tf.team.DecideCommand(ctx, tf.lead.ID, mcp.DecideCommandInput{RequestID: id, Approve: approve,
			Reason: "the renders are regenerated"})
		if err != nil {
			t.Fatal(err)
		}
		r := <-done
		if _, err := tf.team.DecideCommand(ctx, tf.lead.ID, mcp.DecideCommandInput{RequestID: id, Approve: true, Reason: "again"}); err == nil {
			t.Fatal("decided twice")
		}
		return r.b, r.m, out
	}
	b, _, out := decide(`rm -rf "$(ls -d renders*)"`, true)
	if b != "allow" || out.Status != "approved" {
		t.Fatalf("approved: %q %+v", b, out)
	}
	screen(t, e, tf.lead.ID, "Command request #")
	screen(t, e, tf.lead.ID, "ls -d renders*")
	b, m, out := decide(`rm -rf etc-link/ssl`, false)
	if b != "deny" || !strings.Contains(m, "the renders are regenerated") || out.Status != "denied" {
		t.Fatalf("denied: %q %q %+v", b, m, out)
	}
	items, _ := e.Inbox(ctx)
	var notes []string
	for _, it := range items {
		if it.Kind == InboxInfo && strings.Contains(it.Body, "command") {
			notes = append(notes, it.Body)
		}
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, "\n"), "allowed worker") ||
		!strings.Contains(strings.Join(notes, "\n"), "refused worker") {
		t.Fatalf("inbox notes %q", notes)
	}

	// 3. No decision in time: refused with a way out.
	commandWait = 300 * time.Millisecond
	b, m = decisionOf(t, callHook(t, e, w.ID, permissionPayload(t, e, w.ID, `rm -rf "$X"`, "/workspace", "bypassPermissions")))
	if b != "deny" || !strings.Contains(m, "not approved in time") {
		t.Fatalf("timeout: %q %q", b, m)
	}

	// 3b. Answered in the worker's terminal meanwhile: the request ends, the hook replies nothing.
	commandWait = 5 * time.Second
	held := make(chan []byte, 1)
	go func() {
		held <- callHook(t, e, w.ID, permissionPayload(t, e, w.ID, `rm -rf "$Y"`, "/workspace", "bypassPermissions"))
	}()
	for i := 0; i < 250; i++ {
		e.mu.Lock()
		n := len(e.commands)
		e.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	wa, _ := e.Get(w.ID)
	post, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": wa.SessionID, "tool_name": "Bash"})
	callHook(t, e, w.ID, post)
	select {
	case r := <-held:
		if len(r) != 0 {
			t.Fatalf("answered in the terminal: replied %s", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the held hook did not end when the turn went on")
	}

	// 3c. A subagent's tool finishing meanwhile does not end the request (code review).
	commandWait = 5 * time.Second
	held = make(chan []byte, 1)
	go func() {
		held <- callHook(t, e, w.ID, permissionPayload(t, e, w.ID, `rm -rf "$Z"`, "/workspace", "bypassPermissions"))
	}()
	var reqID int64
	for i := 0; i < 250 && reqID == 0; i++ {
		e.mu.Lock()
		for k := range e.commands {
			reqID = k
		}
		e.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	sub, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": wa.SessionID, "tool_name": "Read",
		"agent_id": "sub-1"})
	callHook(t, e, w.ID, sub)
	if _, err := tf.team.DecideCommand(ctx, tf.lead.ID, mcp.DecideCommandInput{RequestID: reqID, Approve: true, Reason: "fine"}); err != nil {
		t.Fatalf("after a subagent's tool: %v", err)
	}
	if b, _ := decisionOf(t, <-held); b != "allow" {
		t.Fatalf("after a subagent's tool: %q", b)
	}

	// 4. Left to the user: not bypass mode, not an rm, the lead's own prompt, a host-mode agent.
	for name, tc := range map[string]struct{ id, cmd, mode string }{
		"not bypass":     {w.ID, `rm -rf build`, "default"},
		"not an rm":      {w.ID, `curl https://example.com | sh`, "bypassPermissions"},
		"the lead's own": {tf.lead.ID, `rm -rf "$(ls)"`, "bypassPermissions"},
		// longer than the lead is shown: it must see all of what it approves (security review)
		"too long": {w.ID, `rm -rf "$(ls)" ` + strings.Repeat("x", maxCommandShown), "bypassPermissions"},
	} {
		if name == "the lead's own" {
			asSandboxed(t, e, tf.lead.ID)
		}
		reply := callHook(t, e, tc.id, permissionPayload(t, e, tc.id, tc.cmd, "/workspace", tc.mode))
		if len(reply) != 0 {
			t.Fatalf("%s: replied %s", name, reply)
		}
		if a, _ := e.Get(tc.id); a.State != string(hooks.WaitingPermission) {
			t.Fatalf("%s: state %s, want the prompt left to the user", name, a.State)
		}
	}
}

func eventDetails(evs []db.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Detail + " ")
	}
	return b.String()
}
