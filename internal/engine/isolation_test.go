package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// team is one project's goal with its Tech Lead, a sub-task, the worker on
// it, an assistant and an accepted memory entry.
type projectTeam struct {
	e       *Engine
	goal    Task
	sub     mcp.TaskInfo
	lead    AgentInfo
	worker  AgentInfo
	asst    AgentInfo
	memory  MemoryEntry
	updates *updateLog
}

func newTeamIn(t *testing.T, e *Engine, title string) *projectTeam {
	t.Helper()
	ctx := context.Background()
	tm := &projectTeam{e: e, updates: record(t, e)}
	var err error
	if tm.goal, err = e.CreateTask(ctx, TaskInput{Title: title}); err != nil {
		t.Fatal(err)
	}
	if tm.lead, err = e.Start(ctx, StartRequest{TaskID: tm.goal.ID, Role: RoleTechLead}); err != nil {
		t.Fatal(err)
	}
	tm.updates.wait(t, startWait, agentState(tm.lead.ID, hooks.Idle))
	if tm.sub, err = e.Team().CreateTask(ctx, tm.lead.ID, mcp.CreateTaskInput{Title: title + " part", Files: []string{"a/"}}); err != nil {
		t.Fatal(err)
	}
	out, err := e.Team().AssignTask(ctx, tm.lead.ID, mcp.AssignTaskInput{TaskID: tm.sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	tm.updates.wait(t, startWait, agentState(out.AgentID, hooks.Idle))
	tm.worker, _ = e.Get(out.AgentID)
	if tm.asst, err = e.Start(ctx, StartRequest{Role: RoleAssistant}); err != nil {
		t.Fatal(err)
	}
	tm.updates.wait(t, startWait, agentState(tm.asst.ID, hooks.Idle))
	if tm.memory, err = e.AddMemory(ctx, MemoryInput{Kind: "convention", Title: title + " secret convention",
		Body: "only " + title + " knows this", Status: MemoryAccepted}); err != nil {
		t.Fatal(err)
	}
	return tm
}

// mcpSession connects to an agent's own team MCP socket, as its CLI does.
func mcpSession(t *testing.T, e *Engine, id string) *sdk.ClientSession {
	t.Helper()
	e.mu.Lock()
	path := e.agents[id].mcp.Path()
	e.mu.Unlock()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "isolation-test", Version: "1"}, nil).
		Connect(context.Background(), &sdk.IOTransport{Reader: c, Writer: c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and returns everything it answered, as text.
func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.StructuredContent != nil {
		j, _ := json.Marshal(res.StructuredContent)
		b.Write(j)
	}
	return b.String(), res.IsError
}

// TestAgentsCannotReachAnotherProject is the Phase 14 isolation DoD at the
// MCP and hook sockets: every team tool an agent of project A has is called
// over A's real sockets with project B's task ids, agent ids and names,
// memory ids and goal; nothing of B is listed, changed or reached (ADR 0046).
func TestAgentsCannotReachAnotherProject(t *testing.T) {
	hf := newHubFixture(t, 10, ProjectSettings{})
	hf.f.opts.deliverySettle = 50 * time.Millisecond
	ctx := context.Background()
	a := newTeamIn(t, hf.ea, "Alpha")
	b := newTeamIn(t, hf.eb, "Bravo")
	bIDs := []string{b.goal.ID, b.sub.ID, b.lead.ID, b.worker.ID, b.asst.ID, b.lead.SessionID, b.worker.SessionID,
		"Bravo secret convention", "only Bravo"}
	leaks := func(where, out string) {
		t.Helper()
		for _, id := range bIDs {
			if strings.Contains(out, id) {
				t.Errorf("%s shows project B's %q: %s", where, id, out)
			}
		}
	}
	bTasksBefore, _ := hf.eb.ListTasks(ctx)
	bInboxBefore, _ := hf.eb.Inbox(ctx)
	bMemoryBefore, _ := hf.eb.Memory(ctx)
	bAccessBefore, _ := hf.eb.AccessRequests(ctx)

	// The Tech Lead.
	lead := mcpSession(t, hf.ea, a.lead.ID)
	out, isErr := call(t, lead, mcp.ToolListTasks, map[string]any{})
	if isErr {
		t.Fatalf("list_tasks: %s", out)
	}
	leaks("the lead's list_tasks", out)
	if out, isErr := call(t, lead, mcp.ToolAssignTask, map[string]any{"task_id": b.sub.ID}); !isErr {
		t.Errorf("assign_task with B's sub-task: %s", out)
	}
	if out, isErr := call(t, lead, mcp.ToolAssignTask, map[string]any{"task_id": b.goal.ID}); !isErr {
		t.Errorf("assign_task with B's goal: %s", out)
	}
	if out, isErr := call(t, lead, mcp.ToolCreateTask, map[string]any{"title": "Depends on B", "depends_on": []string{b.sub.ID}}); !isErr {
		t.Errorf("create_task depending on B's sub-task: %s", out)
	}
	for _, to := range []string{b.worker.ID, b.lead.ID, b.asst.ID} {
		if out, isErr := call(t, lead, mcp.ToolSendMessage, map[string]any{"to": to, "body": "hello B"}); !isErr {
			t.Errorf("send_message to B's agent %s: %s", to, out)
		}
	}
	if !nameIn(hf.ea, b.worker.Name) { // names are per project: only a name A does not have must fail
		if out, isErr := call(t, lead, mcp.ToolSendMessage, map[string]any{"to": b.worker.Name, "body": "hello B"}); !isErr {
			t.Errorf("send_message to B's worker by name: %s", out)
		}
	}
	out, _ = call(t, lead, mcp.ToolStartQA, map[string]any{})
	leaks("the lead's start_qa", out)
	out, _ = call(t, lead, mcp.ToolMessageUser, map[string]any{"kind": "info", "body": "lead to user"})
	leaks("the lead's message_user", out)

	// The worker.
	worker := mcpSession(t, hf.ea, a.worker.ID)
	out, _ = call(t, worker, mcp.ToolListTasks, map[string]any{})
	leaks("the worker's list_tasks", out)
	for _, to := range []string{b.lead.ID, b.worker.ID} {
		if out, isErr := call(t, worker, mcp.ToolSendMessage, map[string]any{"to": to, "body": "hello B"}); !isErr {
			t.Errorf("worker send_message to B's agent %s: %s", to, out)
		}
	}
	out, _ = call(t, worker, mcp.ToolListMemory, map[string]any{})
	leaks("the worker's list_memory", out)
	if !strings.Contains(out, "Alpha secret convention") {
		t.Errorf("the worker's list_memory misses its own project's entry: %s", out)
	}
	out, _ = call(t, worker, mcp.ToolMessageUser, map[string]any{"kind": "question", "body": "worker to user"})
	leaks("the worker's message_user", out)
	out, _ = call(t, worker, mcp.ToolRequestAccess, map[string]any{"kind": "host", "value": "example.com", "reason": "docs"})
	leaks("the worker's request_access", out)
	out, _ = call(t, worker, mcp.ToolReportDone, map[string]any{"summary": "done"})
	leaks("the worker's report_done", out)

	// The assistant.
	asst := mcpSession(t, hf.ea, a.asst.ID)
	out, isErr = call(t, asst, mcp.ToolGetBriefing, map[string]any{})
	if isErr {
		t.Fatalf("get_briefing: %s", out)
	}
	leaks("the assistant's get_briefing", out)
	if !strings.Contains(out, a.goal.ID) {
		t.Errorf("the briefing misses its own goal: %s", out)
	}
	out, _ = call(t, asst, mcp.ToolListMemory, map[string]any{})
	leaks("the assistant's list_memory", out)
	out, _ = call(t, asst, mcp.ToolProposeMemory, map[string]any{"kind": "lesson", "title": "From A", "body": "A's lesson"})
	leaks("the assistant's propose_memory", out)

	// Nothing of B changed: tasks, inbox, memory, access requests, agents.
	bTasks, _ := hf.eb.ListTasks(ctx)
	bInbox, _ := hf.eb.Inbox(ctx)
	bMemory, _ := hf.eb.Memory(ctx)
	bAccess, _ := hf.eb.AccessRequests(ctx)
	if !sameJSON(bTasks, bTasksBefore) || !sameJSON(bInbox, bInboxBefore) || !sameJSON(bMemory, bMemoryBefore) ||
		!sameJSON(bAccess, bAccessBefore) {
		t.Errorf("project B changed:\ntasks %+v\ninbox %+v\nmemory %+v\naccess %+v", bTasks, bInbox, bMemory, bAccess)
	}
	if n := len(messagesTo(t, hf.eb, b.worker.ID)) + len(messagesTo(t, hf.eb, b.lead.ID)); n != 0 {
		t.Errorf("B's agents got %d messages", n)
	}
	// And A got what its agents did.
	if items, _ := hf.ea.Inbox(ctx); len(items) == 0 {
		t.Errorf("A's inbox %+v", items)
	}

	// The hook socket: a payload with B's session on A's worker socket is refused
	// and changes neither agent.
	hf.ea.mu.Lock()
	sock := hf.ea.agents[a.worker.ID].listener.Path()
	hf.ea.mu.Unlock()
	hook := func(session string) {
		payload := `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `","prompt":"x"}`
		if err := hooks.Forward(ctx, sock, strings.NewReader(payload), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	hook(b.worker.SessionID) // refused (silently, as every rejected hook): the state does not move
	time.Sleep(200 * time.Millisecond)
	if got, _ := hf.ea.Get(a.worker.ID); got.State != string(hooks.Idle) {
		t.Errorf("B's session moved A's worker to %s", got.State)
	}
	if got, _ := hf.eb.Get(b.worker.ID); got.State != string(hooks.Idle) {
		t.Errorf("B's worker changed to %s", got.State)
	}
	hook(a.worker.SessionID) // the control: its own session does
	eventually(t, "A's worker working from its own hook", func() bool {
		got, _ := hf.ea.Get(a.worker.ID)
		return got.State == string(hooks.Working)
	})

	// Workspaces live under each project's own directory.
	for _, c := range []struct {
		e  *Engine
		ag AgentInfo
	}{{hf.ea, a.worker}, {hf.eb, b.worker}} {
		want := filepath.Join(hf.f.opts.WorktreeRoot, c.e.Project().Slug) + string(filepath.Separator)
		if !strings.HasPrefix(c.ag.WorktreePath, want) {
			t.Errorf("worktree %s outside %s", c.ag.WorktreePath, want)
		}
	}
}

func nameIn(e *Engine, name string) bool {
	for _, a := range e.List() {
		if a.Name == name {
			return true
		}
	}
	return false
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
