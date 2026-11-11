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
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/auth"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/store"
)

// fakeAgent ignores the claude flags and behaves like a line-echoing program.
const fakeAgent = "#!/bin/sh\necho fake-agent-ready\nexec cat\n"

type fixture struct {
	srv   *httptest.Server
	hub   *engine.Hub
	eng   *engine.Engine // the project's
	pid   string         // its id
	pp    string         // its routes: /api/projects/<id>
	ws    string         // ws:// base URL
	auth  *auth.Manager
	token string // a paired device's token; do() sends it unless the test sets Authorization
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith lets a test change the API options (e.g. remote hosts).
func newFixtureWith(t *testing.T, change func(*Options)) *fixture {
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
	hub, err := engine.NewHub(context.Background(), engine.HubOptions{Base: engine.Options{
		WorktreeRoot: filepath.Join(dir, "wt"), Store: st, MaxAgents: 1,
		ClaudeBin: bin, HookCommand: "true", RuntimeDir: filepath.Join(dir, "rt"),
		BaseEnv: []string{"PATH=" + os.Getenv("PATH")}, StopTimeout: time.Second, Logger: log,
		AgentOfficeBin: bin, HistoryDir: filepath.Join(dir, "terminals"),
	}, Defaults: engine.NeutralDefaults()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := hub.Register(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := hub.Engine(p.ID)
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
	am, err := auth.New(context.Background(), st.Q, auth.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := am.CreateToken(context.Background(), "tests")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Port: port, Version: "test", Logger: log, Auth: am}
	if change != nil {
		change(&opts)
	}
	srv := httptest.NewUnstartedServer(New(hub, ui, opts))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(func() { srv.Close(); hub.Shutdown() })
	return &fixture{srv: srv, hub: hub, eng: eng, pid: p.ID, pp: "/api/projects/" + p.ID,
		ws: "ws" + strings.TrimPrefix(srv.URL, "http"), auth: am, token: tok}
}

// dialOpts authenticates a WebSocket as the fixture's device.
func (f *fixture) dialOpts() *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + f.token}}}
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
	if _, set := hdr["Authorization"]; !set {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else if v == "" {
			req.Header.Del(k)
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
	resp := f.do(t, "POST", f.pp+"/agents", nil)
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
	if r := f.do(t, "GET", f.pp+"/agents", map[string]string{"Host": "evil.example:" + mustPort(f)}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %d", r.StatusCode)
	}
	if r := f.do(t, "GET", f.pp+"/agents", map[string]string{"Host": "localhost:" + mustPort(f)}); r.StatusCode != http.StatusOK {
		t.Fatalf("localhost Host: %d", r.StatusCode)
	}
}

func mustPort(f *fixture) string { u, _ := url.Parse(f.srv.URL); return u.Port() }

func TestRejectsCrossOriginWrites(t *testing.T) {
	f := newFixture(t)
	if r := f.do(t, "POST", f.pp+"/agents", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
	if len(f.eng.List()) != 0 {
		t.Fatal("cross-origin POST started an agent")
	}
	a := f.start(t)
	if r := f.do(t, "POST", f.pp+"/agents", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("second start: %d", r.StatusCode)
	}
	if r := f.do(t, "DELETE", f.pp+"/agents/"+a.ID, nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("delete running: %d", r.StatusCode)
	}
	if r := f.do(t, "POST", f.pp+"/agents/"+a.ID+"/discard", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin discard: %d", r.StatusCode)
	}
}

func TestStatusAPI(t *testing.T) {
	f := newFixture(t)
	r := f.doBody(t, "GET", f.pp+"/status", nil, "")
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
	r := f.doBody(t, "POST", f.pp+"/agents", ct, `{}`)
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	for _, path := range []string{f.pp + "/agents/" + a.ID + "/import", f.pp + "/agents/" + a.ID + "/save-work"} {
		if r := f.doBody(t, "POST", path, ct, ``); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s on a host agent: %d", path, r.StatusCode)
		}
	}
	if r := f.doBody(t, "POST", f.pp+"/agents/nope/import", ct, ``); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d", r.StatusCode)
	}
}

func TestAgentEventsAPI(t *testing.T) {
	f := newFixture(t)
	ct := map[string]string{"Content-Type": "application/json"}
	r := f.doBody(t, "POST", f.pp+"/agents", ct, `{}`)
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	r = f.doBody(t, "GET", f.pp+"/agents/"+a.ID+"/events", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("events of a host agent: %d %s", r.StatusCode, body)
	}
	if r := f.doBody(t, "GET", f.pp+"/agents/nope/events", nil, ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d", r.StatusCode)
	}
}

func TestTasksAPI(t *testing.T) {
	f := newFixture(t)
	json415 := f.doBody(t, "POST", f.pp+"/tasks", nil, `{"title":"x"}`) // no Content-Type
	if json415.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type: %d", json415.StatusCode)
	}
	ct := map[string]string{"Content-Type": "application/json"}
	if r := f.doBody(t, "POST", f.pp+"/tasks", ct, `{"title":"x","owner":"me"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", f.pp+"/tasks", ct, `{"title":"   "}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty title: %d", r.StatusCode)
	}
	r := f.doBody(t, "POST", f.pp+"/tasks", ct, `{"title":"Sayfalama ekle","description":"limit/offset"}`)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)

	r = f.doBody(t, "PATCH", f.pp+"/tasks/"+task.ID, ct, `{"status":"review"}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d", r.StatusCode)
	}
	if r := f.doBody(t, "PATCH", f.pp+"/tasks/"+task.ID, ct, `{"status":"doing"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status: %d", r.StatusCode)
	}

	// Start an agent for the task; its branch carries the task slug.
	r = f.doBody(t, "POST", f.pp+"/agents", ct, `{"task_id":"`+task.ID+`"}`)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("start for task: %d", r.StatusCode)
	}
	var a engine.AgentInfo
	_ = json.NewDecoder(r.Body).Decode(&a)
	if !strings.HasPrefix(a.Branch, "agent-office/sayfalama-ekle-") || a.TaskID != task.ID {
		t.Fatalf("agent %+v", a)
	}
	if r := f.do(t, "POST", f.pp+"/agents", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("max agents: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", f.pp+"/agents", ct, `{"task_id":"missing"}`); r.StatusCode != http.StatusConflict && r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown task: %d", r.StatusCode)
	}

	// Uncommitted work is committed when the agent exits (ADR 0051), so the
	// worktree goes on DELETE and the branch with the work stays.
	for _, kv := range [][2]string{{"user.name", "Ozan Test"}, {"user.email", "ozan@example.com"}} {
		if out, err := exec.Command("git", "-C", a.WorktreePath, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(a.WorktreePath, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.do(t, "POST", f.pp+"/agents/"+a.ID+"/stop", nil)
	r = f.do(t, "DELETE", f.pp+"/agents/"+a.ID, nil)
	var res engine.RemoveResult
	_ = json.NewDecoder(r.Body).Decode(&res)
	if r.StatusCode != http.StatusOK || res.Branch != "kept" {
		t.Fatalf("delete after the exit saved the work: %d %+v", r.StatusCode, res)
	}
	if r := f.do(t, "DELETE", f.pp+"/tasks/"+task.ID, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete task: %d", r.StatusCode)
	}
}

func TestTerminalRoundTripAndExit(t *testing.T) {
	f := newFixture(t)
	a := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, f.ws+f.pp+"/agents/"+a.ID+"/terminal", f.dialOpts())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, c, "fake-agent-ready")
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ping-123\r"}`))
	readUntil(t, ctx, c, "ping-123")

	if r := f.do(t, "POST", f.pp+"/agents/"+a.ID+"/stop", nil); r.StatusCode != http.StatusOK {
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
	c2, _, err := websocket.Dial(ctx, f.ws+f.pp+"/agents/"+a.ID+"/terminal", f.dialOpts())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, c2, "ping-123")
	if r := f.do(t, "DELETE", f.pp+"/agents/"+a.ID, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete exited: %d", r.StatusCode)
	}
}

func TestCrossOriginWebSocketRejected(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, f.ws+f.pp+"/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}, "Authorization": []string{"Bearer " + f.token}},
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
	c, _, err := websocket.Dial(ctx, f.ws+f.pp+"/events", f.dialOpts())
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
	f.do(t, "POST", f.pp+"/agents/"+a.ID+"/stop", nil)
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
	if r := f.doBody(t, "POST", f.pp+"/agents", ct, `{"role":"lead"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("lead without a goal: %d", r.StatusCode)
	}
	if r := f.doBody(t, "POST", f.pp+"/agents", ct, `{"role":"admin"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown role: %d", r.StatusCode)
	}
	r := f.doBody(t, "POST", f.pp+"/tasks", ct, `{"title":"Goal"}`)
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)
	r = f.doBody(t, "POST", f.pp+"/agents", ct, `{"task_id":"`+task.ID+`","role":"lead"}`)
	var a engine.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil || r.StatusCode != http.StatusCreated || a.Role != engine.RoleTechLead || a.Kind != engine.KindLead {
		t.Fatalf("start lead: %d %+v %v", r.StatusCode, a, err)
	}
	r = f.doBody(t, "GET", f.pp+"/agents/"+a.ID+"/messages", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("messages: %d %s", r.StatusCode, body)
	}
	if r := f.doBody(t, "GET", f.pp+"/agents/nope/messages", nil, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("messages of an unknown agent: %d", r.StatusCode)
	}
}

// Deciding an access request is a state change: it needs a same-origin POST,
// and unknown or malformed ids are refused.
func TestAccessRequestsAPI(t *testing.T) {
	f := newFixture(t)
	r := f.doBody(t, "GET", f.pp+"/access-requests", nil, "")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("list: %d %s", r.StatusCode, body)
	}
	cases := []struct {
		path string
		hdr  map[string]string
		want int
	}{
		{f.pp + "/access-requests/1/approve", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{f.pp + "/access-requests/1/deny", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{f.pp + "/access-requests/1/approve", nil, http.StatusNotFound},
		{f.pp + "/access-requests/abc/approve", nil, http.StatusBadRequest},
		{f.pp + "/access-requests/0/deny", nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if r := f.do(t, "POST", tc.path, tc.hdr); r.StatusCode != tc.want {
			t.Errorf("POST %s %v: %d, want %d", tc.path, tc.hdr, r.StatusCode, tc.want)
		}
	}
	// A GET never reaches the decision handler; it falls through to the UI.
	if r := f.do(t, "GET", f.pp+"/access-requests/1/approve", nil); strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Error("GET reached the decision handler")
	}
}

// Without --github the pull request endpoints refuse clearly; the list is
// empty; the origin guard applies to every POST.
func TestPullRequestsAPIWithoutGitHub(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, "GET", f.pp+"/pull-requests", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("list %d %s", resp.StatusCode, b)
	}
	ct := map[string]string{"Content-Type": "application/json"}
	r := f.doBody(t, "POST", f.pp+"/tasks", ct, `{"title":"Add tags"}`)
	var task engine.Task
	_ = json.NewDecoder(r.Body).Decode(&task)
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{f.pp + "/tasks/" + task.ID + "/pull-request", "", http.StatusConflict},
		{f.pp + "/pull-requests/1/review", "", http.StatusConflict},
		{f.pp + "/pull-requests/1/follow-up", `{"review_id":1}`, http.StatusConflict},
		{f.pp + "/pull-requests/1/follow-up", `{}`, http.StatusBadRequest},
		{f.pp + "/pull-requests/1/accept", `{"commits":["abc"]}`, http.StatusConflict},
		{f.pp + "/pull-requests/x/review", "", http.StatusBadRequest},
		{f.pp + "/pull-requests/1/accept", `{"commits":["a"],"force":true}`, http.StatusBadRequest},
	} {
		if r := f.doBody(t, "POST", c.path, ct, c.body); r.StatusCode != c.want {
			t.Errorf("POST %s: %d, want %d", c.path, r.StatusCode, c.want)
		}
	}
	if r := f.do(t, "POST", f.pp+"/tasks/"+task.ID+"/pull-request", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
}

func TestUsageAPI(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	r := f.doBody(t, "GET", f.pp+"/usage", nil, "")
	if r.StatusCode != http.StatusOK || r.Header.Get("X-Request-Id") == "" {
		t.Fatalf("usage %d, request id %q", r.StatusCode, r.Header.Get("X-Request-Id"))
	}
	var rep engine.UsageReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 1 || rep.Sessions[0].Models == nil || rep.Models == nil || rep.Prices.AsOf == "" {
		t.Fatalf("report %+v", rep)
	}
	if r2 := f.doBody(t, "GET", f.pp+"/usage", nil, ""); r2.Header.Get("X-Request-Id") == r.Header.Get("X-Request-Id") {
		t.Fatal("request ids repeat")
	}
}

// Phase 12's endpoints: roles, inbox, specs, QA runs, memory, the assistant's
// briefing and messages to an agent, and their place in the snapshot.
func TestTeamWorkflowAPI(t *testing.T) {
	f := newFixture(t)
	ct := map[string]string{"Content-Type": "application/json"}
	decode := func(r *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	var roles []map[string]any
	r := f.do(t, "GET", f.pp+"/roles", nil)
	decode(r, &roles)
	names := map[string]string{}
	for _, x := range roles {
		names[x["name"].(string)] = x["behaviour"].(string)
		if _, ok := x["prompt"]; ok {
			t.Fatal("role prompts are not served")
		}
	}
	if names["tech-lead"] != "lead" || names["docs"] != "worker" || names["assistant"] != "assistant" {
		t.Fatalf("roles %v", names)
	}
	for _, p := range []string{f.pp + "/inbox", f.pp + "/specs", f.pp + "/qa-runs", f.pp + "/memory"} {
		body, _ := io.ReadAll(f.do(t, "GET", p, nil).Body)
		if strings.TrimSpace(string(body)) != "[]" {
			t.Errorf("%s: %s", p, body)
		}
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", f.pp + "/inbox/x/read", "", http.StatusBadRequest},
		{"POST", f.pp + "/inbox/9/read", "", http.StatusNotFound},
		{"POST", f.pp + "/inbox/9/reply", `{"body":"hi"}`, http.StatusNotFound},
		{"PUT", f.pp + "/specs/9", `{"summary":"s","design":"","sub_tasks":[]}`, http.StatusNotFound},
		{"POST", f.pp + "/specs/9/approve", "", http.StatusNotFound},
		{"POST", f.pp + "/memory", `{"kind":"rumour","title":"t","body":"b"}`, http.StatusBadRequest},
		{"POST", f.pp + "/memory", `{"kind":"lesson","title":"t","body":"b","extra":1}`, http.StatusBadRequest},
		{"POST", f.pp + "/tasks/nope/resolve-conflict", "", http.StatusNotFound},
		{"POST", f.pp + "/agents/nope/message", `{"body":"hi"}`, http.StatusNotFound},
	} {
		if r := f.doBody(t, c.method, c.path, ct, c.body); r.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, r.StatusCode, c.want)
		}
	}
	var m engine.MemoryEntry
	r = f.doBody(t, "POST", f.pp+"/memory", ct, `{"kind":"decision","title":"SQLite","body":"Only SQLite."}`)
	decode(r, &m)
	if r.StatusCode != http.StatusCreated || m.Status != engine.MemoryAccepted {
		t.Fatalf("add memory %d %+v", r.StatusCode, m)
	}
	r = f.doBody(t, "PATCH", f.pp+"/memory/"+strconv.FormatInt(m.ID, 10), ct, `{"body":"Only SQLite, via modernc."}`)
	decode(r, &m)
	if r.StatusCode != http.StatusOK || m.Body != "Only SQLite, via modernc." {
		t.Fatalf("edit memory %d %+v", r.StatusCode, m)
	}
	r = f.doBody(t, "PATCH", f.pp+"/memory/"+strconv.FormatInt(m.ID, 10), ct, `{"roles":["worker"],"paths":["internal/store/"]}`)
	decode(r, &m)
	if r.StatusCode != http.StatusOK || len(m.Roles) != 1 || len(m.Paths) != 1 {
		t.Fatalf("scope memory %d %+v", r.StatusCode, m)
	}
	if r := f.doBody(t, "PATCH", f.pp+"/memory/"+strconv.FormatInt(m.ID, 10), ct, `{"roles":["boss"]}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown role in scope: %d", r.StatusCode)
	}
	r = f.do(t, "GET", f.pp+"/memory/export", nil)
	md, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/markdown") ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(string(md), "### SQLite") {
		t.Fatalf("export %d %q", r.StatusCode, md)
	}
	if r := f.do(t, "DELETE", f.pp+"/memory/"+strconv.FormatInt(m.ID, 10), nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete memory %d", r.StatusCode)
	}

	// The briefing button starts the assistant (no task); the user can write to it.
	var as engine.AgentInfo
	r = f.do(t, "POST", f.pp+"/assistant/briefing", nil)
	decode(r, &as)
	if r.StatusCode != http.StatusOK || as.Kind != engine.KindAssistant || as.TaskID != "" {
		t.Fatalf("briefing %d %+v", r.StatusCode, as)
	}
	if r := f.doBody(t, "POST", f.pp+"/agents/"+as.ID+"/message", ct, `{"body":"hello"}`); r.StatusCode != http.StatusCreated {
		t.Fatalf("message the assistant: %d", r.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.ws+f.pp+"/events", f.dialOpts())
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]json.RawMessage
	_ = json.Unmarshal(data, &snap)
	for _, k := range []string{"inbox", "specs", "qa_runs", "memory"} {
		if v := string(snap[k]); !strings.HasPrefix(v, "[") {
			t.Errorf("snapshot %s = %q", k, v)
		}
	}
}

// Resume, sessions and saved terminals (ADR 0035, 0036).
func TestResumeAndSessionsAPI(t *testing.T) {
	decode := func(r *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixture(t)
	a := f.start(t)
	ct := map[string]string{"Content-Type": "application/json"}
	if r := f.doBody(t, "POST", f.pp+"/agents/"+a.ID+"/resume", ct, `{}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("resume a running agent: %d", r.StatusCode)
	}
	for i := 0; ; i++ { // output to save
		out, _ := f.eng.SessionTerminal(context.Background(), a.ID, a.SessionID)
		if strings.Contains(string(out), "fake-agent-ready") {
			break
		}
		if i > 100 {
			t.Fatal("the agent printed nothing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := f.eng.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	var got engine.AgentInfo
	r := f.doBody(t, "POST", f.pp+"/agents/"+a.ID+"/resume", ct, `{"prompt":"go on"}`)
	decode(r, &got)
	if r.StatusCode != http.StatusOK || got.ID != a.ID || got.ResumedFrom != a.SessionID || got.SessionID == a.SessionID {
		t.Fatalf("resume %d %+v", r.StatusCode, got)
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", f.pp + "/agents/nope/resume", `{}`, http.StatusNotFound},
		{"POST", f.pp + "/agents/" + a.ID + "/resume", `{"prompt":1}`, http.StatusBadRequest},
		{"GET", f.pp + "/agents/nope/sessions", "", http.StatusNotFound},
		{"GET", f.pp + "/agents/" + a.ID + "/sessions/00000000-0000-4000-8000-000000000000/terminal", "", http.StatusNotFound},
	} {
		if r := f.doBody(t, c.method, c.path, ct, c.body); r.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, r.StatusCode, c.want)
		}
	}
	var ss []engine.SessionInfo
	r = f.do(t, "GET", f.pp+"/agents/"+a.ID+"/sessions", nil)
	decode(r, &ss)
	if r.StatusCode != http.StatusOK || len(ss) != 2 || !ss[0].Live || ss[1].ID != a.SessionID || !ss[1].Terminal {
		t.Fatalf("sessions %d %+v", r.StatusCode, ss)
	}
	r = f.do(t, "GET", f.pp+"/agents/"+a.ID+"/sessions/"+a.SessionID+"/terminal", nil)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(string(body), "fake-agent-ready") {
		t.Fatalf("saved terminal %d %q %q", r.StatusCode, r.Header.Get("Content-Type"), body)
	}
}
