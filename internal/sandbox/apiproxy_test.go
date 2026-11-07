package sandbox

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

const fakeRealToken = "fake-real-token-for-tests-only"

type upstreamSeen struct {
	mu                           sync.Mutex
	hits                         int
	auth, apiKey, path, rawQuery string
	body                         string
}

// newAPITest starts a fake Anthropic API and the proxy in front of it. The
// upstream streams two SSE events and holds the second until release closes.
func newAPITest(t *testing.T, maxBody int64) (proxyURL string, seen *upstreamSeen, rec *recorder, logs *bytes.Buffer, release chan struct{}) {
	t.Helper()
	seen = &upstreamSeen{}
	release = make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.mu.Lock()
		seen.hits++
		seen.auth, seen.apiKey, seen.path, seen.rawQuery, seen.body = r.Header.Get("Authorization"),
			r.Header.Get("X-Api-Key"), r.URL.Path, r.URL.RawQuery, string(b)
		seen.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	rec = &recorder{}
	logs = &bytes.Buffer{}
	p := &APIProxy{Token: secret.New(fakeRealToken), Upstream: u, Transport: up.Client().Transport,
		OnEvent: rec.add, MaxBody: maxBody,
		Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	return srv.URL, seen, rec, logs, release
}

func post(t *testing.T, u, path, auth string, body io.Reader, hdr ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAPIProxyForwardsAndStreams(t *testing.T) {
	u, seen, rec, logs, release := newAPITest(t, 0)
	resp := post(t, u, "/v1/messages?beta=true", "Bearer "+Placeholder, strings.NewReader(`{"model":"x"}`))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	// The first event must arrive while the upstream still holds the second.
	got := make(chan string, 1)
	go func() { line, _ := br.ReadString('\n'); got <- line }()
	select {
	case line := <-got:
		if line != "event: message_start\n" {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first event not streamed before the upstream finished: response is buffered")
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "event: message_stop") {
		t.Fatalf("rest %q", rest)
	}

	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.auth != "Bearer "+fakeRealToken || seen.apiKey != "" || seen.path != "/v1/messages" ||
		seen.rawQuery != "beta=true" || seen.body != `{"model":"x"}` {
		t.Fatalf("upstream saw auth-ok=%v apiKey=%q path=%q query=%q body=%q", seen.auth == "Bearer "+fakeRealToken, seen.apiKey, seen.path, seen.rawQuery, seen.body)
	}
	if ev := rec.all(); len(ev) != 0 {
		t.Fatalf("events %+v", ev)
	}
	if strings.Contains(logs.String(), fakeRealToken) {
		t.Fatal("token in logs")
	}
	if !strings.Contains(logs.String(), `"path":"/v1/messages"`) || strings.Contains(logs.String(), "beta=true") {
		t.Fatalf("log must carry the path but not the query: %s", logs.String())
	}
}

func TestAPIProxyRejectsOtherEndpoints(t *testing.T) {
	u, seen, rec, _, _ := newAPITest(t, 0)
	cases := []struct{ method, path, detail string }{
		{"GET", "/v1/messages", "GET /v1/messages"},
		{"GET", "/v1/models", "GET /v1/models"},
		{"POST", "/v1/messages/count_tokens", "POST /v1/messages/count_tokens"},
		{"POST", "/api/oauth/profile", "POST /api/oauth/profile"},
		{"DELETE", "/v1/messages", "DELETE /v1/messages"},
		{"POST", "/v1/messages/../models", "POST /v1/messages/../models"}, // exact match, no cleaning
		{"POST", "/v1/messages/", "POST /v1/messages/"},
		{"POST", "//v1/messages", "POST //v1/messages"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, u+c.path, nil)
		req.Header.Set("Authorization", "Bearer "+Placeholder)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 || !strings.Contains(string(b), `"permission_error"`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, b)
		}
	}
	ev := rec.all()
	if len(ev) != len(cases) {
		t.Fatalf("events %+v", ev)
	}
	for i, c := range cases {
		if ev[i].Kind != APIRejected || ev[i].Detail != c.detail {
			t.Errorf("event %d = %+v, want %q", i, ev[i], c.detail)
		}
	}
	if seen.hits != 0 {
		t.Fatalf("rejected requests reached the upstream: %d", seen.hits)
	}
}

func TestAPIProxyRejectsUnexpectedCredentials(t *testing.T) {
	u, seen, rec, logs, _ := newAPITest(t, 0)
	cases := map[string][]string{
		"none":          {""},
		"other bearer":  {"Bearer sk-ant-something-else"},
		"api key too":   {"Bearer " + Placeholder, "X-Api-Key", "sk-ant-key"},
		"two auth hdrs": {"Bearer " + Placeholder, "Authorization", "Bearer " + Placeholder},
	}
	for name, c := range cases {
		resp := post(t, u, "/v1/messages", c[0], strings.NewReader("{}"), c[1:]...)
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
	if ev := rec.all(); len(ev) != len(cases) || ev[0].Kind != APIAuth {
		t.Fatalf("events %+v", ev)
	}
	if seen.hits != 0 {
		t.Fatal("request with unexpected credentials reached the upstream")
	}
	if strings.Contains(logs.String(), "sk-ant-") {
		t.Fatal("credential header value in logs")
	}
}

func TestAPIProxyBodyLimit(t *testing.T) {
	u, seen, rec, _, release := newAPITest(t, 16)
	close(release)
	// Declared length: refused before the upstream is contacted.
	resp := post(t, u, "/v1/messages", "Bearer "+Placeholder, strings.NewReader(strings.Repeat("x", 1000)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || seen.hits != 0 {
		t.Fatalf("declared: status %d, upstream hits %d", resp.StatusCode, seen.hits)
	}
	// Undeclared (chunked) length: cut off while streaming.
	resp = post(t, u, "/v1/messages", "Bearer "+Placeholder, io.MultiReader(strings.NewReader(strings.Repeat("x", 1000))))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: status %d", resp.StatusCode)
	}
	if ev := rec.all(); len(ev) != 2 || ev[0].Kind != APIRejected || !strings.Contains(ev[1].Detail, "body larger") {
		t.Fatalf("events %+v", ev)
	}
}

func TestAPIProxyUpstreamDown(t *testing.T) {
	rec := &recorder{}
	p := &APIProxy{Token: secret.New(fakeRealToken), Upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		OnEvent: rec.add, Log: slog.New(slog.DiscardHandler)}
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	resp := post(t, srv.URL, "/v1/messages", "Bearer "+Placeholder, strings.NewReader("{}"))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || strings.Contains(string(b), fakeRealToken) {
		t.Fatalf("status %d body %s", resp.StatusCode, b)
	}
	if ev := rec.all(); len(ev) != 1 || ev[0].Kind != APIError {
		t.Fatalf("events %+v", ev)
	}
}

// ADR 0026/0027: the proxy meters usage from the stream (also when the
// upstream compresses), logs Anthropic's request id, and refuses requests
// once Allow says a budget is spent, without contacting the upstream.
func TestAPIProxyMetersUsageAndBudget(t *testing.T) {
	stream, err := os.ReadFile(filepath.Join("..", "usage", "testdata", "stream_a.sse"))
	if err != nil {
		t.Fatal(err)
	}
	var hits int
	var gotEncoding string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_01AAAA")
		if strings.Contains(gotEncoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			zw.Write(stream)
			zw.Close()
			return
		}
		w.Write(stream)
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	var mu sync.Mutex
	var got []usage.Message
	var refuse error
	rec := &recorder{}
	logs := &bytes.Buffer{}
	p := &APIProxy{Token: secret.New(fakeRealToken), Upstream: u, Transport: &http.Transport{},
		OnEvent: rec.add, Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnUsage: func(m usage.Message) { mu.Lock(); got = append(got, m); mu.Unlock() },
		Allow:   func() error { mu.Lock(); defer mu.Unlock(); return refuse }}
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL, "/v1/messages", "Bearer "+Placeholder, strings.NewReader("{}"), "Accept-Encoding", "br")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, stream) {
		t.Fatalf("status %d, body changed: %v", resp.StatusCode, !bytes.Equal(body, stream))
	}
	if gotEncoding != "gzip" {
		t.Errorf("upstream Accept-Encoding %q, want Go's gzip (the client's br removed)", gotEncoding)
	}
	mu.Lock()
	if len(got) == 0 || got[len(got)-1].ID != "msg_01AAAA" || got[len(got)-1].Output != 480 || got[len(got)-1].RequestID != "req_01AAAA" {
		t.Errorf("metered %+v", got)
	}
	refuse = errors.New("this session's budget of $1.00 is spent")
	mu.Unlock()
	if !strings.Contains(logs.String(), `"request_id":"req_01AAAA"`) {
		t.Errorf("request id not logged: %s", logs)
	}

	resp = post(t, srv.URL, "/v1/messages", "Bearer "+Placeholder, strings.NewReader("{}"))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "budget of $1.00 is spent") ||
		!strings.Contains(string(body), "permission_error") {
		t.Errorf("refusal %d %s", resp.StatusCode, body)
	}
	if hits != 1 {
		t.Errorf("upstream hits %d, want 1", hits)
	}
	if evs := rec.all(); len(evs) != 1 || evs[0].Kind != APIBudget {
		t.Errorf("events %+v", evs)
	}
	if strings.Contains(logs.String(), "SENSITIVE") {
		t.Errorf("content logged: %s", logs)
	}
}
