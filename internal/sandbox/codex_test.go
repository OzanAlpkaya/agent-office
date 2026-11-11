package sandbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeLogin(t *testing.T, dir string, doc map[string]any, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "auth.json")
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadCodexLogin(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 11, 21, 1, 34, 20, 0, time.UTC)
	tok := fakeJWT(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	p := writeLogin(t, dir, map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": tok,
		"refresh_token": "never-read", "account_id": "11111111-2222-4333-8444-555555555555"}}, 0o600)
	l, err := ReadCodexLogin(p)
	if err != nil {
		t.Fatal(err)
	}
	if l.Token.Reveal() != tok || l.Account != "11111111-2222-4333-8444-555555555555" || l.Plan != "plus" || !l.Expires.Equal(exp) {
		t.Fatalf("login %+v", l)
	}
	for name, c := range map[string]struct {
		doc  map[string]any
		mode os.FileMode
		want string
	}{
		"api key login": {map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "x"}, 0o600, "not a ChatGPT login"},
		"no account":    {map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": tok}}, 0o600, "no account id"},
		"readable":      {map[string]any{"auth_mode": "chatgpt"}, 0o644, "readable only by you"},
	} {
		d := t.TempDir()
		_, err := ReadCodexLogin(writeLogin(t, d, c.doc, c.mode))
		if !errors.Is(err, ErrCodexAuth) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), tok) {
			t.Errorf("%s: the error carries the token", name)
		}
	}
}

// TestCodexProxy: only the placeholder login and the allowed routes pass; the
// real token and account go upstream; usage is metered; budgets refuse model
// requests; TLS with the local CA works over a unix socket (Spike G).
func TestCodexProxy(t *testing.T) {
	real := "real-access-token-" + strings.Repeat("x", 20)
	var got []*http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(context.Background()))
		if r.URL.Path == "/backend-api/codex/responses" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_9","model":"gpt-6.1-sol",`+
				`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":7}}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=upstream-cookie")
		if r.URL.Path == "/backend-api/wham/accounts/check" {
			_, _ = io.WriteString(w, `{"accounts":[{"id":"acct-1","name":"Ozan A","plan_type":"plus","profile_picture_url":"https://x/p.png",`+
				`"workspace_backend_origin":"NO_CONSTRAINT"}],"user":{"email":"someone@example.com"}}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(up.Close)
	upURL, _ := url.Parse(up.URL)
	var events []Event
	var metered []usage.Message
	budgetSpent := false
	p := &CodexProxy{
		Login: func() (CodexLogin, error) {
			return CodexLogin{Token: secret.New(real), Account: "acct-1"}, nil
		},
		Placeholder: "placeholder-bearer", Upstream: upURL, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnEvent: func(e Event) { events = append(events, e) }, OnUsage: func(m usage.Message) { metered = append(metered, m) },
		Allow: func() error {
			if budgetSpent {
				return errors.New("this session's budget is spent")
			}
			return nil
		},
	}
	ca, err := NewLocalCA()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p.Handler()}
	go func() { _ = srv.Serve(ca.Listener(ln)) }()
	t.Cleanup(func() { srv.Close() })
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("CA certificate")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	do := func(method, path, bearer, account string) *http.Response {
		req, _ := http.NewRequest(method, "https://127.0.0.1:8080"+path, strings.NewReader("{}"))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if account != "" {
			req.Header.Set("ChatGPT-Account-Id", account)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}
	if r := do("GET", "/backend-api/codex/models", "placeholder-bearer", "acct-1"); r.StatusCode != 200 || r.Header.Get("Set-Cookie") != "" {
		t.Fatalf("models: %d, cookie %q", r.StatusCode, r.Header.Get("Set-Cookie"))
	}
	// The account routes reach the agent without the user's personal details.
	req, _ := http.NewRequest("GET", "https://127.0.0.1:8080/backend-api/wham/accounts/check", nil)
	req.Header.Set("Authorization", "Bearer placeholder-bearer")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(body), "Ozan") || strings.Contains(string(body), "someone@") ||
		strings.Contains(string(body), "p.png") || !strings.Contains(string(body), `"workspace_backend_origin":"NO_CONSTRAINT"`) ||
		!strings.Contains(string(body), `"plan_type":"plus"`) {
		t.Fatalf("accounts check reached the agent as %d %s", resp.StatusCode, body)
	}
	got = got[:1]
	if r := do("POST", "/backend-api/codex/responses", "placeholder-bearer", "acct-1"); r.StatusCode != 200 {
		t.Fatalf("responses: %d", r.StatusCode)
	}
	if len(got) != 2 || got[1].Header.Get("Authorization") != "Bearer "+real || got[1].Header.Get("Chatgpt-Account-Id") != "acct-1" {
		t.Fatalf("upstream got %d requests; the real token was not put on", len(got))
	}
	if len(metered) != 1 || metered[0].ID != "resp_9" || metered[0].Input != 60 || metered[0].CacheRead != 40 || metered[0].Output != 7 {
		t.Fatalf("metered %+v", metered)
	}
	for name, c := range map[string]struct {
		method, path, bearer, account string
		want                          int
		kind                          EventKind
	}{
		"other route":     {"GET", "/backend-api/ps/plugins/list", "placeholder-bearer", "acct-1", 403, APIRejected},
		"other host api":  {"POST", "/v1/responses", "placeholder-bearer", "", 403, APIRejected},
		"real token sent": {"GET", "/backend-api/codex/models", real, "acct-1", 401, APIAuth},
		"no token":        {"GET", "/backend-api/codex/models", "", "", 401, APIAuth},
		"other account":   {"GET", "/backend-api/codex/models", "placeholder-bearer", "acct-2", 401, APIAuth},
		"encoded path":    {"GET", "/backend-api/codex%2Fmodels", "placeholder-bearer", "acct-1", 403, APIRejected},
	} {
		events = nil
		if r := do(c.method, c.path, c.bearer, c.account); r.StatusCode != c.want || len(events) != 1 || events[0].Kind != c.kind {
			t.Errorf("%s: %d %+v", name, r.StatusCode, events)
		}
	}
	if len(got) != 2 {
		t.Fatalf("refused requests reached the upstream: %d", len(got))
	}
	budgetSpent = true
	if r := do("POST", "/backend-api/codex/responses", "placeholder-bearer", "acct-1"); r.StatusCode != 403 {
		t.Fatalf("over budget: %d", r.StatusCode)
	}
	if r := do("GET", "/backend-api/codex/models", "placeholder-bearer", "acct-1"); r.StatusCode != 200 {
		t.Fatalf("a model list is not a model request, budget or not: %d", r.StatusCode)
	}
	// The CA signs for loopback only.
	cert, _ := x509.ParseCertificate(ca.server.Certificate[0])
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "chatgpt.com"}); err == nil {
		t.Fatal("the loopback certificate verifies for chatgpt.com")
	}
}
