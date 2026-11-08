package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/api"
	"github.com/OzanAlpkaya/agent-office/internal/auth"
	"github.com/OzanAlpkaya/agent-office/internal/store"
)

func TestParseRemoteHosts(t *testing.T) {
	got, err := parseRemoteHosts(" Box.Tail1234.ts.net., laptop.tail1234.ts.net ,")
	if err != nil || len(got) != 2 || got[0] != "box.tail1234.ts.net" || got[1] != "laptop.tail1234.ts.net" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := parseRemoteHosts(""); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, bad := range []string{
		"box.example.com", "https://box.tail1234.ts.net", "box.tail1234.ts.net:443", "tail1234.ts.net", "ts.net",
		"box..ts.net", "-box.tail1234.ts.net", "box.tail1234.ts.net/x", "box_1.tail1234.ts.net", "100.101.102.103",
	} {
		if _, err := parseRemoteHosts(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	c, err := parseServe([]string{"--repo", "/tmp", "--remote-host", "box.tail1234.ts.net"})
	if err != nil || len(c.remoteHosts) != 1 {
		t.Fatalf("serve flag: %+v %v", c.remoteHosts, err)
	}
	t.Setenv("AGENT_OFFICE_REMOTE_HOSTS", "box.example.com")
	if _, err := parseServe([]string{"--repo", "/tmp"}); err == nil {
		t.Fatal("env var not validated")
	}
}

func TestControlSocketPath(t *testing.T) {
	a := controlSocketPath("/run/user/1000/agent-office", "/home/u/.local/share/agent-office")
	b := controlSocketPath("/run/user/1000/agent-office", "/home/u/.cache/agent-office-dev/data")
	if a == b || filepath.Dir(a) != "/run/user/1000/agent-office" || len(a) > 100 {
		t.Fatalf("paths %q %q", a, b)
	}
	if a != controlSocketPath("/run/user/1000/agent-office", "/home/u/.local/share/agent-office/") {
		t.Fatal("trailing slash changes the path")
	}
}

// TestPairCommand runs `agent-office pair` against a control socket served
// like the daemon serves it.
func TestPairCommand(t *testing.T) {
	rt, err := os.MkdirTemp("", "ao-rt-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	data := t.TempDir()

	var out, errOut bytes.Buffer
	if code := pairCommand([]string{"--data-dir", data}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "no agent-office daemon is running") {
		t.Fatalf("no daemon: %d %q", code, errOut.String())
	}

	st, err := store.Open(context.Background(), filepath.Join(data, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := auth.New(context.Background(), st.Q, auth.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := runtimeDir()
	path := controlSocketPath(dir, data)
	l, err := listenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}
	srv := &http.Server{Handler: api.NewControl(m, api.ControlInfo{LocalURL: "http://127.0.0.1:7680/",
		RemoteHosts: []string{"box.tail1234.ts.net"}}, log)}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	if _, err := listenControl(path); err == nil || !strings.Contains(err.Error(), "another agent-office daemon") {
		t.Fatalf("second daemon on the same data dir: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := pairCommand([]string{"--data-dir", data}, &out, &errOut); code != 0 {
		t.Fatalf("pair: %d %s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "Pairing code: ") || !strings.Contains(s, "http://127.0.0.1:7680/#pair=") ||
		!strings.Contains(s, "https://box.tail1234.ts.net/") {
		t.Fatalf("output %q", s)
	}
	codeStr := strings.Fields(strings.SplitN(s, "Pairing code: ", 2)[1])[0]
	if _, _, err := m.Pair(context.Background(), codeStr, "browser", "local"); err != nil {
		t.Fatalf("printed code does not pair: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := pairCommand([]string{"--data-dir", data, "--token", "e2e"}, &out, &errOut); code != 0 {
		t.Fatalf("token: %d %s", code, errOut.String())
	}
	tok := strings.TrimSpace(out.String())
	if d, ok := m.Authenticate(context.Background(), tok); !ok || d.Kind != auth.KindToken || d.Name != "e2e" {
		t.Fatalf("printed token: %v %+v", ok, d)
	}
	if strings.Contains(errOut.String(), tok) {
		t.Fatal("token printed to stderr")
	}
	if code := pairCommand([]string{"--data-dir", data, "--token", " "}, &out, &errOut); code != 1 {
		t.Fatal("empty token name accepted")
	}
}
