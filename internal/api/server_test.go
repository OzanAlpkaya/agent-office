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
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
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
	eng, err := engine.New(engine.Options{
		Repo: t.TempDir(), ClaudeBin: bin, HookCommand: "true", RuntimeDir: filepath.Join(dir, "rt"),
		BaseEnv: []string{"PATH=" + os.Getenv("PATH")}, StopTimeout: time.Second, Logger: log,
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
	req, _ := http.NewRequest(method, f.srv.URL+path, nil)
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
	if r := f.do(t, "DELETE", "/api/agents/"+a.ID, nil); r.StatusCode != http.StatusNoContent {
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
		Type   string             `json:"type"`
		Agents []engine.AgentInfo `json:"agents"`
		Agent  engine.AgentInfo   `json:"agent"`
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
	if m := read(); m.Type != "snapshot" || len(m.Agents) != 1 || m.Agents[0].ID != a.ID {
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
