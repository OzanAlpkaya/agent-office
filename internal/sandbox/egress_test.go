package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAllowlist(t *testing.T) {
	a, err := ParseAllowlist([]string{"proxy.golang.org", " .example.com ", ""})
	if err != nil {
		t.Fatal(err)
	}
	yes := []string{"proxy.golang.org", "PROXY.golang.org.", "a.example.com", "x.y.example.com"}
	no := []string{"golang.org", "evilproxy.golang.org", "example.com", "evilexample.com", "1.2.3.4",
		"proxy.golang.org.evil.com", "", "localhost", "[::1]", "a..example.com"}
	for _, h := range yes {
		if !a.Allows(h) {
			t.Errorf("Allows(%q) = false", h)
		}
	}
	for _, h := range no {
		if a.Allows(h) {
			t.Errorf("Allows(%q) = true", h)
		}
	}
	for _, bad := range []string{"*.example.com", "https://example.com", "example.com:443", "10.0.0.1",
		"Example.com", "localhost", "-a.com", "a_b.com", "::1"} {
		if _, err := ParseAllowlist([]string{bad}); err == nil {
			t.Errorf("ParseAllowlist(%q) accepted", bad)
		}
	}
	if _, err := ParseAllowlist(DefaultEgressAllow); err != nil {
		t.Fatalf("default allowlist: %v", err)
	}
}

func TestPublicOnly(t *testing.T) {
	refused := []string{"127.0.0.1:443", "[::1]:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:443", "100.100.100.100:443", "0.0.0.0:443", "[fe80::1]:443", "[fd00::1]:443",
		"[::ffff:127.0.0.1]:443", "224.0.0.1:443", "240.0.0.1:443"}
	for _, a := range refused {
		if publicOnly("tcp", a, nil) == nil {
			t.Errorf("publicOnly(%s) allowed", a)
		}
	}
	for _, a := range []string{"140.82.121.4:443", "[2606:4700:4700::1111]:443"} {
		if err := publicOnly("tcp", a, nil); err != nil {
			t.Errorf("publicOnly(%s): %v", a, err)
		}
	}
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) add(e Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }
func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// startEgress serves an EgressProxy on a unix socket. Its Dial connects every
// allowed target to an in-process echo server and records the address.
func startEgress(t *testing.T, allow ...string) (sock string, rec *recorder, dialed *[]string) {
	t.Helper()
	return startEgressGrants(t, nil, allow...)
}

// startEgressGrants is startEgress with a run-time grant set (ADR 0019).
func startEgressGrants(t *testing.T, g *Grants, allow ...string) (sock string, rec *recorder, dialed *[]string) {
	t.Helper()
	a, err := ParseAllowlist(allow)
	if err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	var mu sync.Mutex
	dialed = &[]string{}
	rec = &recorder{}
	p := &EgressProxy{Allow: a, Grants: g, OnEvent: rec.add, Log: slog.New(slog.DiscardHandler),
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			*dialed = append(*dialed, addr)
			mu.Unlock()
			if strings.HasPrefix(addr, "unreachable.") {
				return nil, errors.New("no route")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
		}}
	sock = filepath.Join(t.TempDir(), "egress.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Serve(l) }()
	t.Cleanup(func() { l.Close(); echo.Close() })
	return sock, rec, dialed
}

func connect(t *testing.T, sock, req string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	return strings.TrimSpace(line), c, br
}

func TestEgressTunnel(t *testing.T) {
	sock, rec, dialed := startEgress(t, DefaultEgressAllow...)
	status, c, br := connect(t, sock, "CONNECT proxy.golang.org:443 HTTP/1.1\r\nHost: proxy.golang.org:443\r\n\r\n")
	defer c.Close()
	if status != "HTTP/1.1 200 Connection established" {
		t.Fatalf("status %q", status)
	}
	if blank, _ := br.ReadString('\n'); blank != "\r\n" {
		t.Fatalf("header end %q", blank)
	}
	for _, msg := range []string{"hello", "second message"} {
		fmt.Fprint(c, msg)
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(br, buf); err != nil || string(buf) != msg {
			t.Fatalf("echo %q %v", buf, err)
		}
	}
	if len(*dialed) != 1 || (*dialed)[0] != "proxy.golang.org:443" {
		t.Fatalf("dialed %v", *dialed)
	}
	if ev := rec.all(); len(ev) != 0 {
		t.Fatalf("events for an allowed tunnel: %+v", ev)
	}
}

func TestEgressRefusals(t *testing.T) {
	sock, rec, dialed := startEgress(t, DefaultEgressAllow...)
	cases := []struct{ req, status, detail string }{
		{"CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n", "403", "github.com:443"},
		{"CONNECT github.com:22 HTTP/1.1\r\nHost: github.com:22\r\n\r\n", "403", "github.com:22"},
		{"CONNECT proxy.golang.org:80 HTTP/1.1\r\nHost: proxy.golang.org:80\r\n\r\n", "403", "proxy.golang.org:80"},
		{"CONNECT 140.82.121.4:443 HTTP/1.1\r\nHost: 140.82.121.4:443\r\n\r\n", "403", "140.82.121.4:443"},
		{"GET http://proxy.golang.org/ HTTP/1.1\r\nHost: proxy.golang.org\r\n\r\n", "405", "GET proxy.golang.org"},
	}
	for _, tc := range cases {
		status, c, _ := connect(t, sock, tc.req)
		c.Close()
		if !strings.Contains(status, " "+tc.status+" ") {
			t.Errorf("%q: status %q, want %s", tc.detail, status, tc.status)
		}
	}
	ev := rec.all()
	if len(ev) != len(cases) {
		t.Fatalf("events %+v", ev)
	}
	for i, tc := range cases {
		if ev[i].Kind != EgressDenied || ev[i].Detail != tc.detail {
			t.Errorf("event %d = %+v, want %s %q", i, ev[i], EgressDenied, tc.detail)
		}
	}
	if len(*dialed) != 0 {
		t.Fatalf("refused requests were dialed: %v", *dialed)
	}
}

func TestEgressUpstreamFailure(t *testing.T) {
	sock, rec, _ := startEgress(t, ".example.com")
	status, c, _ := connect(t, sock, "CONNECT unreachable.example.com:443 HTTP/1.1\r\nHost: unreachable.example.com:443\r\n\r\n")
	c.Close()
	if !strings.Contains(status, " 502 ") {
		t.Fatalf("status %q", status)
	}
	if ev := rec.all(); len(ev) != 1 || ev[0].Kind != EgressFailed {
		t.Fatalf("events %+v", ev)
	}
}

func TestEgressOversizedHeaderIsDropped(t *testing.T) {
	sock, _, dialed := startEgress(t, DefaultEgressAllow...)
	req := "CONNECT proxy.golang.org:443 HTTP/1.1\r\nX-Pad: " + strings.Repeat("a", egressMaxHeader) + "\r\n\r\n"
	status, c, _ := connect(t, sock, req)
	c.Close()
	if strings.Contains(status, " 200 ") || len(*dialed) != 0 {
		t.Fatalf("oversized header tunnelled: %q %v", status, *dialed)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("GET /v1/x\x1b[31m\nnext"); strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("control chars kept: %q", got)
	}
	if got := sanitize(strings.Repeat("a", 500)); len(got) > maxDetail+len("…") {
		t.Fatalf("not truncated: %d", len(got))
	}
}

// A granted host is reachable for this proxy's agent only, from the moment it
// is added; other hosts stay refused.
func TestEgressGrants(t *testing.T) {
	g := &Grants{}
	sock, _, dialed := startEgressGrants(t, g, DefaultEgressAllow...)
	req := "CONNECT docs.example.org:443 HTTP/1.1\r\nHost: docs.example.org:443\r\n\r\n"
	status, c, _ := connect(t, sock, req)
	c.Close()
	if !strings.Contains(status, " 403 ") {
		t.Fatalf("before the grant: %q", status)
	}
	if err := g.Add("docs.example.org"); err != nil {
		t.Fatal(err)
	}
	status, c, _ = connect(t, sock, req)
	c.Close()
	if !strings.Contains(status, " 200 ") || len(*dialed) != 1 {
		t.Fatalf("after the grant: %q, dialed %v", status, *dialed)
	}
	status, c, _ = connect(t, sock, "CONNECT other.example.org:443 HTTP/1.1\r\nHost: other.example.org:443\r\n\r\n")
	c.Close()
	if !strings.Contains(status, " 403 ") {
		t.Fatalf("ungranted host: %q", status)
	}
	other, _, _ := startEgress(t, DefaultEgressAllow...) // another agent's proxy
	status, c, _ = connect(t, other, req)
	c.Close()
	if !strings.Contains(status, " 403 ") {
		t.Fatalf("grant leaked to another proxy: %q", status)
	}
	for _, bad := range []string{".example.org", "*.example.org", "1.2.3.4", "Example.org", "localhost", "a.org:443"} {
		if g.Add(bad) == nil {
			t.Errorf("Add(%q) accepted", bad)
		}
	}
	var none *Grants
	if none.Allows("docs.example.org") {
		t.Fatal("nil grants allowed a host")
	}
}
