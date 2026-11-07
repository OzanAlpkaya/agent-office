package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/store"
)

// fakeAgent ignores the claude flags and behaves like a line-echoing program.
const fakeAgent = "#!/bin/sh\necho fake-agent-ready\nexec cat\n"

type fixture struct {
	srv *httptest.Server
	eng *engine.Engine
	ws  string // ws:// base URL
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "ao-api-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(bin, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng, err := engine.New(context.Background(), engine.Options{
		Repo: repo, WorktreeRoot: filepath.Join(dir, "wt"), Store: st, MaxAgents: 1,
		ClaudeBin: bin, HookCommand: "true", RuntimeDir: filepath.Join(dir, "rt"),
		BaseEnv: []string{"PATH=" + os.Getenv("PATH")}, StopTimeout: time.Second, Logger: log,
		AgentOfficeBin: bin,
	})
	if err != nil {
		t.Fatal(err)
	}
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>agent-office</title>")}}

	// Bind first so the handler knows its port before the server starts.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	srv := httptest.NewUnstartedServer(New(eng, ui, Options{Port: port, Version: "test", Logger: log}))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(func() { srv.Close(); eng.Shutdown() })
	return &fixture{srv: srv, eng: eng, ws: "ws" + strings.TrimPrefix(srv.URL, "http")}
}

func (f *fixture) do(t *testing.T, method, path string, hdr map[string]string) *http.Response {
	t.Helper()
	return f.doBody(t, method, path, hdr, "")
}

func (f *fixture) doBody(t *testing.T, method, path string, hdr map[string]string, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *fixture) start(t *testing.T) engine.AgentInfo {
	t.Helper()
	resp := f.do(t, "POST", "/api/agents", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	var a engine.AgentInfo
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, want string) {
	t.Helper()
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v (got %q)", want, err, got.String())
		}
		got.Write(data)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	f := newFixture(t)
	if r := f.do(t, "GET", "/api/agents", map[string]string{"Host": "evil.example:" + mustPort(f)}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %d", r.StatusCode)
	}
	if r := f.do(t, "GET", "/api/agents", map[string]string{"Host": "localhost:" + mustPort(f)}); r.StatusCode != http.StatusOK {
		t.Fatalf("localhost Host: %d", r.StatusCode)
	}
}

func mustPort(f *fixture) string { u, _ := url.Parse(f.srv.URL); return u.Port() }

func TestRejectsCrossOriginWrites(t *testing.T) {
	f := newFixture(t)
	if r := f.do(t, "POST", "/api/agents", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
	if len(f.eng.List()) != 0 {
		t.Fatal("cross-origin POST started an agent")
	}
	a := f.start(t)
	if r := f.do(t, "POST", "/api/agents", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("second start: %d", r.StatusCode)
	}
	if r := f.do(t, "DELETE", "/api/agents/"+a.ID, nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("delete running: %d", r.StatusCode)
	}
	if r := f.do(t, "POST", "/api/agents/"+a.ID+"/discard", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin discard: %d", r.StatusCode)
	}
}

func TestStatusAPI(t *testing.T) {
	f := newFixture(t)
	r := f.doBody(t, "GET", "/api/status", nil, "")
	var st engine.Status
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("status %d %v", r.StatusCode, err)
	}
	if st.Sandbox != engine.SandboxHost || st.Warnings == nil {
		t.Fatalf("host fixture status %+v", st)
	}
}

func TestImportRefusesHostAgents(t *testing.T) {
	f := newFixture(t)
	ct := map[string]string{"Content-Type": "application/json"}
	r := f.doBody(t, "POST", "/api/agents", ct, `{}`)
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	for _, path := range []string{"/api/agents/" + a.ID + "/import", "/api/agents/" + a.ID + "/save-work"} {
		if r := f.doBody(t, "POST", path, ct, ``); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s on a host agent: %d", path, r.StatusCode)
		}
	}
	if r := f.doBody(t, "POST", "/api/agents/nope/import", ct, ``); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d", r.StatusCode)
	}
}

func TestAgentEventsAPI(t *testing.T) {
	f := newFixture(t)
	ct := map[string]string{"Content-Type": "application/json"}
	r := f.doBody(t, "POST", "/api/agents", ct, `{}`)
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	r = f.doBody(t, "GET", "/api/agents/"+a.ID+"/events", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("events of a host agent: %d %s", r.StatusCode, body)
	}
	if r := f.doBody(t, "GET", "/api/agents/nope/events", nil, ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d", r.StatusCode)
	}
}

func TestTasksAPI(t *testing.T) {
	f := newFixture(t)
	json415 := f.doBody(t, "POST", "/api/tasks", nil, `{"title":"x"}`) // no Content-Type
	if json415.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type: %d", json415.StatusCode)
	}
	ct := map[string]string{"Content-Type": "application/json"}
	if r := f.doBody(t, "POST", "/api/tasks", ct, `{"title":"x","owner":"me"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", "/api/tasks", ct, `{"title":"   "}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty title: %d", r.StatusCode)
	}
	r := f.doBody(t, "POST", "/api/tasks", ct, `{"title":"Sayfalama ekle","description":"limit/offset"}`)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)

	r = f.doBody(t, "PATCH", "/api/tasks/"+task.ID, ct, `{"status":"review"}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d", r.StatusCode)
	}
	if r := f.doBody(t, "PATCH", "/api/tasks/"+task.ID, ct, `{"status":"doing"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status: %d", r.StatusCode)
	}

	// Start an agent for the task; its branch carries the task slug.
	r = f.doBody(t, "POST", "/api/agents", ct, `{"task_id":"`+task.ID+`"}`)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("start for task: %d", r.StatusCode)
	}
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	if !strings.HasPrefix(a.Branch, "agent-office/sayfalama-ekle-") || a.TaskID != task.ID {
		t.Fatalf("agent %+v", a)
	}
	if r := f.do(t, "POST", "/api/agents", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("max agents: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", "/api/agents", ct, `{"task_id":"missing"}`); r.StatusCode != http.StatusConflict && r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown task: %d", r.StatusCode)
	}

	// A dirty worktree is kept on DELETE and only removed by discard.
	f.do(t, "POST", "/api/agents/"+a.ID+"/stop", nil)
	if err := os.WriteFile(filepath.Join(a.WorktreePath, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := f.do(t, "DELETE", "/api/agents/"+a.ID, nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("delete dirty: %d", r.StatusCode)
	}
	if r := f.do(t, "POST", "/api/agents/"+a.ID+"/discard", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("discard: %d", r.StatusCode)
	}
	if r := f.do(t, "DELETE", "/api/tasks/"+task.ID, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete task: %d", r.StatusCode)
	}
}

func TestTerminalRoundTripAndExit(t *testing.T) {
	f := newFixture(t)
	a := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, f.ws+"/api/agents/"+a.ID+"/terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, c, "fake-agent-ready")
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ping-123\r"}`))
	readUntil(t, ctx, c, "ping-123")

	if r := f.do(t, "POST", "/api/agents/"+a.ID+"/stop", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d", r.StatusCode)
	}
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("expected normal closure, got %v", err)
			}
			break
		}
	}

	// Re-attaching to an exited agent replays its output, then closes.
	c2, _, err := websocket.Dial(ctx, f.ws+"/api/agents/"+a.ID+"/terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, c2, "ping-123")
	if r := f.do(t, "DELETE", "/api/agents/"+a.ID, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete exited: %d", r.StatusCode)
	}
}

func TestCrossOriginWebSocketRejected(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, f.ws+"/api/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WS: err=%v resp=%v", err, resp)
	}
}

func TestEventsSnapshotAndUpdates(t *testing.T) {
	f := newFixture(t)
	a := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.ws+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	type msg struct {
		Type   string                 `json:"type"`
		Agents []engine.AgentInfo     `json:"agents"`
		Tasks  []engine.Task          `json:"tasks"`
		Agent  engine.AgentInfo       `json:"agent"`
		Access []engine.AccessRequest `json:"access_requests"`
		PRs    []engine.PullRequest   `json:"pull_requests"`
	}
	read := func() msg {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m msg
		_ = json.Unmarshal(data, &m)
		return m
	}
	if m := read(); m.Type != "snapshot" || len(m.Agents) != 1 || m.Agents[0].ID != a.ID || m.Tasks == nil || m.Access == nil || m.PRs == nil {
		t.Fatalf("snapshot %+v", m)
	}
	f.do(t, "POST", "/api/agents/"+a.ID+"/stop", nil)
	for {
		if m := read(); m.Type == "agent" && m.Agent.State == "exited" {
			return
		}
	}
}

func TestUIFallbackAndHeaders(t *testing.T) {
	f := newFixture(t)
	r := f.do(t, "GET", "/some/client/route", nil)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || !strings.Contains(string(body), "agent-office") {
		t.Fatalf("fallback: %d %q", r.StatusCode, body)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
}

func TestStartRoleAPI(t *testing.T) {
	f := newFixture(t)
	ct := map[string]string{"Content-Type": "application/json"}
	if r := f.doBody(t, "POST", "/api/agents", ct, `{"role":"lead"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("lead without a goal: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", "/api/agents", ct, `{"role":"admin"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown role: %d", r.StatusCode)
	}
	r := f.doBody(t, "POST", "/api/tasks", ct, `{"title":"Goal"}`)
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)
	r = f.doBody(t, "POST", "/api/agents", ct, `{"task_id":"`+task.ID+`","role":"lead"}`)
	var a engine.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil || r.StatusCode != http.StatusCreated || a.Role != engine.RoleLead {
		t.Fatalf("start lead: %d %+v %v", r.StatusCode, a, err)
	}
	r = f.doBody(t, "GET", "/api/agents/"+a.ID+"/messages", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("messages: %d %s", r.StatusCode, body)
	}
	if r := f.doBody(t, "GET", "/api/agents/nope/messages", nil, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("messages of an unknown agent: %d", r.StatusCode)
	}
}

// Deciding an access request is a state change: it needs a same-origin POST,
// and unknown or malformed ids are refused.
func TestAccessRequestsAPI(t *testing.T) {
	f := newFixture(t)
	r := f.doBody(t, "GET", "/api/access-requests", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("list: %d %s", r.StatusCode, body)
	}
	cases := []struct {
		path string
		hdr  map[string]string
		want int
	}{
		{"/api/access-requests/1/approve", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"/api/access-requests/1/deny", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"/api/access-requests/1/approve", nil, http.StatusNotFound},
		{"/api/access-requests/abc/approve", nil, http.StatusBadRequest},
		{"/api/access-requests/0/deny", nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if r := f.do(t, "POST", tc.path, tc.hdr); r.StatusCode != tc.want {
			t.Errorf("POST %s %v: %d, want %d", tc.path, tc.hdr, r.StatusCode, tc.want)
		}
	}
	// A GET never reaches the decision handler; it falls through to the UI.
	if r := f.do(t, "GET", "/api/access-requests/1/approve", nil); strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Error("GET reached the decision handler")
	}
}

// Without --github the pull request endpoints refuse clearly; the list is
// empty; the origin guard applies to every POST.
func TestPullRequestsAPIWithoutGitHub(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, "GET", "/api/pull-requests", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("list %d %s", resp.StatusCode, b)
	}
	ct := map[string]string{"Content-Type": "application/json"}
	r := f.doBody(t, "POST", "/api/tasks", ct, `{"title":"Add tags"}`)
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{"/api/tasks/" + task.ID + "/pull-request", "", http.StatusConflict},
		{"/api/pull-requests/1/review", "", http.StatusConflict},
		{"/api/pull-requests/1/follow-up", `{"review_id":1}`, http.StatusConflict},
		{"/api/pull-requests/1/follow-up", `{}`, http.StatusBadRequest},
		{"/api/pull-requests/1/accept", `{"commits":["abc"]}`, http.StatusConflict},
		{"/api/pull-requests/x/review", "", http.StatusBadRequest},
		{"/api/pull-requests/1/accept", `{"commits":["a"],"force":true}`, http.StatusBadRequest},
	} {
		if r := f.doBody(t, "POST", c.path, ct, c.body); r.StatusCode != c.want {
			t.Errorf("POST %s: %d, want %d", c.path, r.StatusCode, c.want)
		}
	}
	if r := f.do(t, "POST", "/api/tasks/"+task.ID+"/pull-request", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
}
