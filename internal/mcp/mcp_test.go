package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeTeam records calls and answers with fixed values.
type fakeTeam struct {
	mu    sync.Mutex
	calls []string // "<caller> <tool> <json input>"
	err   error
}

func (f *fakeTeam) record(caller, tool string, in any) {
	b, _ := json.Marshal(in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, caller+" "+tool+" "+string(b))
}

func (f *fakeTeam) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeTeam) CreateTask(_ context.Context, caller string, in CreateTaskInput) (TaskInfo, error) {
	f.record(caller, ToolCreateTask, in)
	return TaskInfo{ID: "t2", Title: in.Title, Status: "todo", ParentTaskID: "t1"}, f.err
}

func (f *fakeTeam) AssignTask(_ context.Context, caller string, in AssignTaskInput) (AssignTaskOutput, error) {
	f.record(caller, ToolAssignTask, in)
	return AssignTaskOutput{TaskID: in.TaskID, AgentID: "w1", Branch: "agent-office/x"}, f.err
}

func (f *fakeTeam) ListTasks(_ context.Context, caller string) (ListTasksOutput, error) {
	f.record(caller, ToolListTasks, struct{}{})
	return ListTasksOutput{You: AgentRef{ID: caller, Role: RoleLead, State: "working"}, Tasks: []TaskInfo{}}, f.err
}

func (f *fakeTeam) SendMessage(_ context.Context, caller string, in SendMessageInput) (SendMessageOutput, error) {
	f.record(caller, ToolSendMessage, in)
	return SendMessageOutput{MessageID: 1, To: in.To, Status: "queued"}, f.err
}

func (f *fakeTeam) ReportDone(_ context.Context, caller string, in ReportDoneInput) (ReportDoneOutput, error) {
	f.record(caller, ToolReportDone, in)
	return ReportDoneOutput{TaskID: "t2", Status: "review", Next: "end your turn"}, f.err
}

func (f *fakeTeam) ReportNoChange(_ context.Context, caller string, in ReportNoChangeInput) (ReportNoChangeOutput, error) {
	f.record(caller, ToolReportNoChange, in)
	return ReportNoChangeOutput{TaskID: "t2", Status: "review", Posted: true, Next: "end your turn"}, f.err
}

func (f *fakeTeam) RequestAccess(_ context.Context, caller string, in RequestAccessInput) (RequestAccessOutput, error) {
	f.record(caller, ToolRequestAccess, in)
	return RequestAccessOutput{RequestID: 3, Status: "pending"}, f.err
}

func (f *fakeTeam) SubmitReview(_ context.Context, caller string, in SubmitReviewInput) (SubmitReviewOutput, error) {
	f.record(caller, ToolSubmitReview, in)
	return SubmitReviewOutput{ReviewID: 5, Posted: true, Next: "end your turn"}, f.err
}

func (f *fakeTeam) MessageUser(_ context.Context, caller string, in MessageUserInput) (MessageUserOutput, error) {
	f.record(caller, ToolMessageUser, in)
	return MessageUserOutput{MessageID: 9, Status: "shown"}, f.err
}

var quiet = slog.New(slog.DiscardHandler)

// The built-in roles' tool lists (internal/role, which this package cannot
// import in tests: it imports mcp). The engine passes a role's list.
const (
	RoleLead     = "lead"
	RoleWorker   = "worker"
	RoleReviewer = "reviewer"
)

var roleTools = map[string][]string{
	RoleLead: {ToolCreateTask, ToolAssignTask, ToolListTasks, ToolSendMessage, ToolMessageUser, ToolReportDone,
		ToolRequestAccess},
	RoleWorker: {ToolListTasks, ToolSendMessage, ToolMessageUser, ToolReportDone, ToolReportNoChange,
		ToolRequestAccess},
	RoleReviewer: {ToolListTasks, ToolSubmitReview},
}

// connect serves NewServer(team, caller, role) in memory and returns a client.
func connect(t *testing.T, team Team, caller, role string) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := NewServer(team, caller, roleTools[role], "test", quiet).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, tool string, args any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", tool, err)
	}
	return res
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func toolNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), &sdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func TestToolsPerRole(t *testing.T) {
	lead := toolNames(t, connect(t, &fakeTeam{}, "l1", RoleLead))
	want := []string{ToolAssignTask, ToolCreateTask, ToolListTasks, ToolMessageUser, ToolReportDone, ToolRequestAccess,
		ToolSendMessage}
	if !slices.Equal(lead, want) {
		t.Fatalf("lead tools %v", lead)
	}
	worker := toolNames(t, connect(t, &fakeTeam{}, "w1", RoleWorker))
	want = []string{ToolListTasks, ToolMessageUser, ToolReportDone, ToolReportNoChange, ToolRequestAccess, ToolSendMessage}
	if !slices.Equal(worker, want) {
		t.Fatalf("worker tools %v", worker)
	}
	reviewer := toolNames(t, connect(t, &fakeTeam{}, "r1", RoleReviewer))
	want = []string{ToolListTasks, ToolSubmitReview}
	if !slices.Equal(reviewer, want) {
		t.Fatalf("reviewer tools %v", reviewer)
	}
	// Not listed means not callable: the SDK refuses an unknown tool.
	cs := connect(t, &fakeTeam{}, "w1", RoleWorker)
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: ToolCreateTask,
		Arguments: map[string]any{"title": "x"}}); err == nil {
		t.Fatal("worker could call create_task")
	}
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: ToolSubmitReview,
		Arguments: map[string]any{"verdict": "approve", "summary": "x"}}); err == nil {
		t.Fatal("worker could call submit_review")
	}
}

func TestReportNoChange(t *testing.T) {
	team := &fakeTeam{}
	cs := connect(t, team, "w1", RoleWorker)
	res := call(t, cs, ToolReportNoChange, map[string]any{"reason": "the line is already there"})
	if res.IsError {
		t.Fatalf("tool error %s", text(res))
	}
	if calls := team.all(); len(calls) != 1 || calls[0] != `w1 report_no_change {"reason":"the line is already there"}` {
		t.Fatalf("calls %v", calls)
	}
	for _, args := range []map[string]any{{}, {"reason": 3}, {"reason": "x", "pr": 2}} {
		if res := call(t, cs, ToolReportNoChange, args); !res.IsError {
			t.Errorf("%v accepted", args)
		}
	}
	// Leads and reviewers do not even see it.
	for _, role := range []string{RoleLead, RoleReviewer} {
		if slices.Contains(toolNames(t, connect(t, &fakeTeam{}, "x1", role)), ToolReportNoChange) {
			t.Errorf("%s has report_no_change", role)
		}
	}
}

func TestSubmitReview(t *testing.T) {
	team := &fakeTeam{}
	cs := connect(t, team, "rev-1", RoleReviewer)
	res := call(t, cs, ToolSubmitReview, map[string]any{"verdict": "changes_requested", "summary": "needs a test",
		"comments": []map[string]any{{"path": "a.go", "line": 3, "body": "nil"}}})
	if res.IsError {
		t.Fatalf("tool error %s", text(res))
	}
	want := `rev-1 submit_review {"verdict":"changes_requested","summary":"needs a test","comments":[{"path":"a.go","line":3,"body":"nil"}]}`
	if calls := team.all(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls %v", calls)
	}
	for _, args := range []map[string]any{
		{"summary": "x"},
		{"verdict": "approve"},
		{"verdict": "approve", "summary": "x", "comments": []map[string]any{{"path": "a.go", "line": "3", "body": "b"}}},
		{"verdict": "approve", "summary": "x", "comments": []map[string]any{{"path": "a.go", "line": 3}}},
		{"verdict": "approve", "summary": "x", "comments": []map[string]any{{"path": "a.go", "line": 3, "body": "b", "side": "LEFT"}}},
		{"verdict": "approve", "summary": "x", "event": "APPROVE"},
	} {
		if res := call(t, cs, ToolSubmitReview, args); !res.IsError {
			t.Errorf("accepted %v", args)
		}
	}
	if calls := team.all(); len(calls) != 1 {
		t.Fatalf("Team called with invalid input: %v", calls)
	}
}

// Every tool passes its validated input to Team with the connection's agent
// as caller, and returns the result as structured content.
func TestEveryToolCallsTeamAsCaller(t *testing.T) {
	team := &fakeTeam{}
	cs := connect(t, team, "lead-1", RoleLead)
	cases := []struct {
		tool string
		args map[string]any
		want string // recorded call
		out  string // a field of the structured result
	}{
		{ToolCreateTask, map[string]any{"title": "Add tags", "description": "d"},
			`lead-1 create_task {"title":"Add tags","description":"d"}`, `"parent_task_id":"t1"`},
		{ToolAssignTask, map[string]any{"task_id": "t2"}, `lead-1 assign_task {"task_id":"t2"}`, `"agent_id":"w1"`},
		{ToolListTasks, map[string]any{}, `lead-1 list_tasks {}`, `"id":"lead-1"`},
		{ToolSendMessage, map[string]any{"to": "w1", "body": "hi"}, `lead-1 send_message {"to":"w1","body":"hi"}`, `"status":"queued"`},
		{ToolReportDone, map[string]any{"summary": "done"}, `lead-1 report_done {"summary":"done"}`, `"status":"review"`},
		{ToolRequestAccess, map[string]any{"kind": "domain", "value": "a.example.com", "reason": "docs"},
			`lead-1 request_access {"kind":"domain","value":"a.example.com","reason":"docs"}`, `"request_id":3`},
	}
	for _, tc := range cases {
		res := call(t, cs, tc.tool, tc.args)
		if res.IsError {
			t.Fatalf("%s: tool error %s", tc.tool, text(res))
		}
		b, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), tc.out) {
			t.Errorf("%s: structured result %s, want %s", tc.tool, b, tc.out)
		}
		if calls := team.all(); calls[len(calls)-1] != tc.want {
			t.Errorf("%s: recorded %q, want %q", tc.tool, calls[len(calls)-1], tc.want)
		}
	}
}

// The SDK checks arguments against the tool's schema before Team is called:
// missing, unknown and wrongly typed fields are tool errors.
func TestSchemaValidation(t *testing.T) {
	team := &fakeTeam{}
	cs := connect(t, team, "lead-1", RoleLead)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{ToolCreateTask, map[string]any{}},
		{ToolCreateTask, map[string]any{"title": 7}},
		{ToolCreateTask, map[string]any{"title": "x", "created_by": "someone-else"}},
		{ToolAssignTask, map[string]any{}},
		{ToolAssignTask, map[string]any{"task_id": "t2", "as_agent": "w9"}},
		{ToolSendMessage, map[string]any{"to": "w1"}},
		{ToolSendMessage, map[string]any{"to": "w1", "body": "x", "from": "w2"}},
		{ToolReportDone, map[string]any{}},
		{ToolRequestAccess, map[string]any{"kind": "domain", "value": "a.example.com"}},
		{ToolRequestAccess, map[string]any{"kind": "domain", "value": []string{"a"}, "reason": "r"}},
		{ToolListTasks, map[string]any{"caller": "w2"}},
	}
	for _, tc := range cases {
		res := call(t, cs, tc.tool, tc.args)
		if !res.IsError {
			t.Errorf("%s %v: accepted", tc.tool, tc.args)
		}
	}
	if calls := team.all(); len(calls) != 0 {
		t.Fatalf("Team called with invalid input: %v", calls)
	}
}

// A refusal's message reaches the agent; any other error is replaced by a
// generic one, so internal details stay in the daemon log.
func TestErrorMapping(t *testing.T) {
	team := &fakeTeam{err: UserError{errors.New("forbidden: only the lead can create tasks")}}
	cs := connect(t, team, "w1", RoleLead)
	res := call(t, cs, ToolCreateTask, map[string]any{"title": "x"})
	if !res.IsError || !strings.Contains(text(res), "only the lead can create tasks") {
		t.Fatalf("user error: %v %q", res.IsError, text(res))
	}
	team.err = errors.New("sqlite: database is locked at /home/u/.local/share/agent-office/agent-office.db")
	res = call(t, cs, ToolCreateTask, map[string]any{"title": "x"})
	if !res.IsError || strings.Contains(text(res), "sqlite") || !strings.Contains(text(res), "internal error") {
		t.Fatalf("internal error leaked: %q", text(res))
	}
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ao-mcp-") // short path: unix socket length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func listen(t *testing.T, team Team, caller, role string) *Listener {
	t.Helper()
	ln, err := Listen(filepath.Join(shortDir(t), "mcp.sock"), NewServer(team, caller, roleTools[role], "test", quiet), quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// dialClient connects an MCP client to a socket directly.
func dialClient(t *testing.T, path string) *sdk.ClientSession {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "1"}, nil).
		Connect(context.Background(), &sdk.IOTransport{Reader: c, Writer: c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// Calls on a socket are attributed to that socket's agent; two sockets, two
// agents. The socket is private to the daemon's user.
func TestListenerIdentity(t *testing.T) {
	team := &fakeTeam{}
	a, b := listen(t, team, "agent-a", RoleWorker), listen(t, team, "agent-b", RoleWorker)
	fi, err := os.Stat(a.Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", fi.Mode(), err)
	}
	call(t, dialClient(t, a.Path()), ToolSendMessage, map[string]any{"to": "lead", "body": "from a"})
	call(t, dialClient(t, b.Path()), ToolSendMessage, map[string]any{"to": "lead", "body": "from b"})
	calls := team.all()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "agent-a ") || !strings.HasPrefix(calls[1], "agent-b ") {
		t.Fatalf("calls %v", calls)
	}
}

func TestListenerConnectionLimit(t *testing.T) {
	ln := listen(t, &fakeTeam{}, "w1", RoleWorker)
	for range MaxConns {
		dialClient(t, ln.Path())
	}
	c, err := net.Dial("unix", ln.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("connection beyond the limit: read %v, want EOF", err)
	}
}

// Closing the listener ends live sessions and removes the socket.
func TestListenerClose(t *testing.T) {
	ln := listen(t, &fakeTeam{}, "w1", RoleWorker)
	cs := dialClient(t, ln.Path())
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cs.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session still open after Close")
	}
	if _, err := os.Stat(ln.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind: %v", err)
	}
}

// The stdio bridge carries a full session: Claude Code's side talks
// newline-delimited JSON on stdin/stdout, the daemon's side is the socket.
func TestBridge(t *testing.T) {
	team := &fakeTeam{}
	ln := listen(t, team, "w1", RoleWorker)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- Bridge(context.Background(), ln.Path(), stdinR, stdoutW); stdoutW.Close() }()

	cs, err := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1"}, nil).
		Connect(context.Background(), &sdk.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := call(t, cs, ToolReportDone, map[string]any{"summary": "all done"})
	if res.IsError {
		t.Fatalf("tool error %s", text(res))
	}
	if calls := team.all(); len(calls) != 1 || calls[0] != `w1 report_done {"summary":"all done"}` {
		t.Fatalf("calls %v", calls)
	}
	cs.Close() // closes stdin: the bridge half-closes, the daemon ends the session
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("bridge: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not exit after stdin closed")
	}
}

func TestBridgeDaemonDown(t *testing.T) {
	err := Bridge(context.Background(), filepath.Join(shortDir(t), "missing.sock"), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "agent-office daemon") {
		t.Fatalf("err %v", err)
	}
}
