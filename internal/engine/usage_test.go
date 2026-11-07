package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/secret"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

// usageFixtureSession names the fixture files in internal/usage/testdata.
const usageFixtureSession = "11111111-2222-4333-8444-555555555555"

// The fixture's totals (internal/usage/testdata/README.md).
var fixtureTokens = usage.Tokens{Input: 320, Output: 1420, CacheRead: 30000, CacheWrite5m: 3200, CacheWrite1h: 5000}

const fixtureCost = 68864 // micro-USD

// syncBuffer is a log sink the engine's goroutines may write concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func usageFixture(t *testing.T) (*fixture, *syncBuffer, string) {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "usage", "testdata", "session"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "projects", "-repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, "AO_FAKE_TRANSCRIPT_DIR="+dir, "AO_FAKE_TRANSCRIPT_SRC="+src)
	logs := &syncBuffer{}
	f.opts.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.opts.usageThrottle = 50 * time.Millisecond
	return f, logs, dir
}

// Host mode (ADR 0026): the session transcript and its subagent file become
// usage rows, counters and a live update, and the report prices them; they
// match the fixture's own numbers and Claude Code's cost-state.
func TestHostUsageFromTranscript(t *testing.T) {
	f, _, _ := usageFixture(t)
	e := f.engine(t)
	log := record(t, e)
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	if err := e.Input(a.ID, []byte("usage\n")); err != nil {
		t.Fatal(err)
	}
	u := log.wait(t, 5*time.Second, func(u Update) bool {
		return u.Type == "usage" && u.Usage.AgentID == a.ID && u.Usage.Messages == 3 && u.Usage.Turns == 1 && u.Usage.CLICostMicroUSD != nil
	}).Usage
	if u.SessionID != a.SessionID || u.Tokens != fixtureTokens || u.CostMicroUSD != fixtureCost ||
		*u.CLICostMicroUSD != fixtureCost || u.ToolCalls != 1 || u.Source != SourceTranscript || u.UnpricedTokens != 0 {
		t.Fatalf("live usage %+v", u)
	}
	if strings.Join(u.Models, ",") != "claude-haiku-5-5,claude-opus-5-5,claude-sonnet-5-5" {
		t.Errorf("models %v", u.Models)
	}
	// A second turn re-reads nothing new: no double counting.
	if err := e.Input(a.ID, []byte("turn\n")); err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, func(u Update) bool { return u.Type == "usage" && u.Usage.Turns == 2 })
	if err := e.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := e.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 1 || rep.Totals.Messages != 3 || rep.Totals.Tokens != fixtureTokens ||
		rep.Totals.CostMicroUSD != fixtureCost || len(rep.Models) != 3 {
		t.Fatalf("report %+v", rep)
	}
	s := rep.Sessions[0]
	if s.Turns != 2 || s.ToolCalls != 1 || s.EndedAt == nil || s.CLICostMicroUSD == nil || s.Messages != 3 || s.Role != RoleWorker ||
		s.CostMicroUSD != fixtureCost {
		t.Errorf("session %+v", s)
	}
	if rep.Prices.AsOf == "" || rep.Prices.Source == "" {
		t.Errorf("prices %+v", rep.Prices)
	}
	b, _ := json.Marshal(rep)
	if bytes.Contains(b, []byte("SENSITIVE")) {
		t.Errorf("content in the report: %s", b)
	}
}

// A changed price table re-prices history; an unknown model is "no price",
// never $0.
func TestUsageRepricedOnRead(t *testing.T) {
	f, _, _ := usageFixture(t)
	p := usage.Builtin()
	delete(p.Models, "claude-haiku-5-5")
	f.opts.Prices = p
	e := f.engine(t)
	log := record(t, e)
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	e.Input(a.ID, []byte("usage\n"))
	log.wait(t, 5*time.Second, func(u Update) bool { return u.Type == "usage" && u.Usage.Messages == 3 })
	rep, err := e.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(fixtureCost - 200); rep.Totals.CostMicroUSD != want || rep.Totals.UnpricedTokens != 1540 {
		t.Errorf("totals %+v", rep.Totals)
	}
	for _, m := range rep.Models {
		if m.Model == "claude-haiku-5-5" && (m.Priced || m.CostMicroUSD != 0) {
			t.Errorf("haiku %+v", m)
		}
	}
}

// ADR 0027: the session and goal budgets refuse once spent; an unpriced model
// fails closed; no budget, no refusal.
func TestBudgets(t *testing.T) {
	f, _, _ := usageFixture(t)
	f.opts.Budgets = Budgets{SessionMicroUSD: 60_000, GoalMicroUSD: 100_000}
	e := f.engine(t)
	ctx := context.Background()
	goal, err := e.CreateTask(ctx, TaskInput{Title: "Goal"})
	if err != nil {
		t.Fatal(err)
	}
	sub1, err := e.createTask(ctx, TaskInput{Title: "Sub 1"}, goal.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	sub2, err := e.createTask(ctx, TaskInput{Title: "Sub 2"}, goal.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	session := func(agentID, taskID string) *sessionUsage {
		t.Helper()
		if _, err := f.store.DB.Exec(`INSERT INTO agents (id, project_id, task_id, inherit_user_settings, state, started_at, sandbox)
			VALUES (?, ?, ?, 0, 'idle', ?, 'podman')`, agentID, e.project.ID, nullString(taskID), now()); err != nil {
			t.Fatal(err)
		}
		su := e.newSessionUsage(launchInfo{id: agentID, sid: "s-" + agentID, taskID: taskID, role: RoleWorker, started: now()}, SandboxPodman)
		if err := e.q.CreateSession(ctx, db.CreateSessionParams{ID: su.view.SessionID, AgentID: agentID, StartedAt: now()}); err != nil {
			t.Fatal(err)
		}
		return su
	}
	opus := func(id string, out int64) usage.Message { // $20 per million output tokens
		return usage.Message{ID: id, Model: "claude-opus-5-5", Tokens: usage.Tokens{Output: out}}
	}
	w1 := session("w1", sub1.ID)
	if su := w1; su.view.GoalID != goal.ID {
		t.Fatalf("goal %q", su.view.GoalID)
	}
	if err := e.allowRequest(w1); err != nil {
		t.Fatalf("fresh session refused: %v", err)
	}
	e.recordUsage(w1, []usage.Message{opus("m1", 2000)}, e.log) // $0.04
	if err := e.allowRequest(w1); err != nil {
		t.Fatalf("under budget refused: %v", err)
	}
	e.recordUsage(w1, []usage.Message{opus("m1", 3000)}, e.log) // the same message grew: $0.06
	if err := e.allowRequest(w1); err == nil || !strings.Contains(err.Error(), "session's budget of $0.06") {
		t.Fatalf("session budget: %v", err)
	}
	// The goal: w1's $0.06 plus w2's $0.05 = $0.11 > $0.10.
	w2 := session("w2", sub2.ID)
	e.recordUsage(w2, []usage.Message{opus("m2", 2500)}, e.log)
	if err := e.allowRequest(w2); err == nil || !strings.Contains(err.Error(), "goal's budget of $0.10") {
		t.Fatalf("goal budget: %v", err)
	}
	// An agent without a task has only the session budget.
	solo := session("solo", "")
	e.recordUsage(solo, []usage.Message{opus("m3", 100)}, e.log)
	if err := e.allowRequest(solo); err != nil {
		t.Fatalf("solo: %v", err)
	}
	e.recordUsage(solo, []usage.Message{{ID: "m4", Model: "claude-unknown-9", Tokens: usage.Tokens{Input: 1}}}, e.log)
	if err := e.allowRequest(solo); !errors.Is(err, errUnpriced) {
		t.Fatalf("unpriced: %v", err)
	}
	e.opts.Budgets = Budgets{}
	if err := e.allowRequest(w1); err != nil {
		t.Fatalf("budgets off: %v", err)
	}
}

func TestNegativeBudgetRefused(t *testing.T) {
	f := newFixture(t)
	f.opts.Budgets = Budgets{GoalMicroUSD: -1}
	if _, err := New(context.Background(), f.opts); err == nil {
		t.Fatal("negative budget accepted")
	}
}

func TestEventRetention(t *testing.T) {
	f := newFixture(t)
	f.opts.EventRetention = 30 * 24 * time.Hour
	e := f.engine(t)
	ctx := context.Background()
	if _, err := f.store.DB.Exec(`INSERT INTO agents (id, project_id, inherit_user_settings, state, started_at) VALUES ('old', ?, 0, 'exited', ?)`,
		e.project.ID, now()); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now().Add(-40 * 24 * time.Hour), now()} {
		if err := e.q.InsertEvent(ctx, db.InsertEventParams{AgentID: "old", SessionID: "s", Kind: "k", State: "idle", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	e.pruneEvents(ctx)
	rows, err := e.q.ListEvents(ctx, db.ListEventsParams{AgentID: "old", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("events left %d, %v", len(rows), err)
	}
}

// The DoD's correlation id (ADR 0028): every log record about an agent's run
// (start, hooks, team MCP calls, API proxy requests, exit) carries its
// session id, and no record carries content or the transcript path.
func TestLogsFollowOneSession(t *testing.T) {
	f, logs, dir := usageFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	ul := record(t, e)
	goal, err := e.CreateTask(ctx, TaskInput{Title: "Goal"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleLead})
	if err != nil {
		t.Fatal(err)
	}
	ul.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	e.Input(a.ID, []byte("usage\n"))
	ul.wait(t, 5*time.Second, func(u Update) bool { return u.Type == "usage" && u.Usage.Messages == 3 })

	// A team tool call over the agent's MCP socket.
	e.mu.Lock()
	mcpPath, alog := e.agents[a.ID].mcp.Path(), e.agents[a.ID].log
	e.mu.Unlock()
	c, err := net.Dial("unix", mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &sdk.IOTransport{Reader: c, Writer: c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: mcp.ToolCreateTask,
		Arguments: map[string]any{"title": "Sub", "description": "SENSITIVE description"}}); err != nil || res.IsError {
		t.Fatalf("create_task: %v %+v", err, res)
	}
	cs.Close()

	// An API proxy request logged with the session's logger, as a sandboxed
	// launch wires it (the sandbox itself is covered by make sandbox-test).
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Request-Id", "req_corr_1")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","id":"msg_x","model":"claude-haiku-5-5","content":[{"type":"text","text":"SENSITIVE"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	proxy := &sandbox.APIProxy{Token: secret.New("t"), Upstream: u, Transport: &http.Transport{}, Log: alog.With("proxy", "api")}
	ps := httptest.NewServer(proxy.Handler())
	defer ps.Close()
	req, _ := http.NewRequest(http.MethodPost, ps.URL+"/v1/messages", strings.NewReader(`{"messages":"SENSITIVE"}`))
	req.Header.Set("Authorization", "Bearer "+sandbox.Placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := e.Stop(a.ID); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		if strings.Contains(line, "SENSITIVE") || strings.Contains(line, dir) {
			t.Errorf("content or transcript path logged: %s", line)
		}
		if rec["agent_id"] != a.ID {
			continue
		}
		if rec["session_id"] != a.SessionID || rec["task_id"] == nil {
			t.Errorf("record without the session or task: %s", line)
		}
		msg, _ := rec["msg"].(string)
		seen[msg] = true
		if msg == "api request" && rec["request_id"] != "req_corr_1" {
			t.Errorf("api request without its request id: %s", line)
		}
	}
	for _, msg := range []string{"agent started", "event", "team tool", "sub-task created", "api request", "agent exited"} {
		if !seen[msg] {
			t.Errorf("no %q record for the session; saw %v", msg, seen)
		}
	}
}
