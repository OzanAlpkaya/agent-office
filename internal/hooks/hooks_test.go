package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
)

func TestNext(t *testing.T) {
	ev := func(k agent.EventKind, tool string) agent.Event { return agent.Event{Kind: k, Tool: tool} }
	cases := []struct {
		name string
		cur  Status
		ev   agent.Event
		want Status
	}{
		{"session start", Status{State: Starting}, ev(agent.SessionStarted, ""), Status{State: Idle}},
		{"prompt", Status{State: Idle}, ev(agent.TurnStarted, ""), Status{State: Working}},
		{"tool start", Status{State: Working}, ev(agent.ToolStarted, "Bash"), Status{State: Working, Activity: "Bash"}},
		{"permission", Status{State: Working, Activity: "Write"}, ev(agent.PermissionRequested, "Write"), Status{State: WaitingPermission, Activity: "Write"}},
		{"late notification keeps tool", Status{State: WaitingPermission, Activity: "Write"}, ev(agent.PermissionRequested, ""), Status{State: WaitingPermission, Activity: "Write"}},
		{"approved", Status{State: WaitingPermission, Activity: "Write"}, ev(agent.ToolFinished, "Write"), Status{State: Working}},
		{"denied", Status{State: WaitingPermission, Activity: "Write"}, ev(agent.ToolFailed, "Write"), Status{State: Working}},
		{"turn done", Status{State: Working}, ev(agent.TurnFinished, ""), Status{State: Idle}},
		{"idle prompt", Status{State: Idle}, ev(agent.Idle, ""), Status{State: Idle}},
		{"session end", Status{State: Working}, ev(agent.SessionEnded, ""), Status{State: Exited}},
		{"exited is final", Status{State: Exited}, ev(agent.SessionStarted, ""), Status{State: Exited}},
		{"interrupt while working", Status{State: Working, Activity: "Bash"}, ev(agent.UserInterrupted, ""), Status{State: Idle}},
		{"dismiss permission", Status{State: WaitingPermission, Activity: "Bash"}, ev(agent.UserInterrupted, ""), Status{State: Idle}},
		{"esc while idle", Status{State: Idle}, ev(agent.UserInterrupted, ""), Status{State: Idle}},
		{"esc while starting", Status{State: Starting}, ev(agent.UserInterrupted, ""), Status{State: Starting}},
		{"other ignored", Status{State: Working, Activity: "Bash"}, ev(agent.Other, ""), Status{State: Working, Activity: "Bash"}},
		{"subagent ignored", Status{State: Working, Activity: "Task"},
			agent.Event{Kind: agent.TurnFinished, Subagent: true}, Status{State: Working, Activity: "Task"}},
	}
	for _, c := range cases {
		if got := Next(c.cur, c.ev); got != c.want {
			t.Errorf("%s: Next(%+v, %+v) = %+v, want %+v", c.name, c.cur, c.ev, got, c.want)
		}
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func shortTempDir(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to ~108 bytes; t.TempDir() can exceed that.
	dir, err := os.MkdirTemp("", "ao-hooks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestListenForwardRoundTrip(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "agents", "a1.sock")
	var got []byte
	ln, err := Listen(path, func(_ context.Context, p []byte) ([]byte, error) {
		got = p
		return []byte(`{"decision":"block","reason":"hi"}`), nil
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := Forward(ctx, path, strings.NewReader(`{"hook_event_name":"Stop"}`), &out); err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"hook_event_name":"Stop"}` || out.String() != `{"decision":"block","reason":"hi"}` {
		t.Fatalf("server got %q, client got %q", got, out.String())
	}

	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket file not removed on Close")
	}
}

func TestHandlerErrorGivesEmptyReply(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "a.sock")
	ln, err := Listen(path, func(context.Context, []byte) ([]byte, error) {
		return []byte("must not be sent"), errors.New("session mismatch")
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var out bytes.Buffer
	if err := Forward(context.Background(), path, strings.NewReader(`{}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("rejected hook got reply %q", out.String())
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "a.sock")
	var called atomic.Bool
	ln, err := Listen(path, func(context.Context, []byte) ([]byte, error) { called.Store(true); return nil, nil }, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	big := strings.NewReader(strings.Repeat("x", MaxPayload+1))
	if err := Forward(context.Background(), path, big, io.Discard); err == nil {
		t.Fatal("client should refuse an oversized payload")
	}

	// A raw client that ignores the limit is cut off by the server.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte(strings.Repeat("x", MaxPayload+1)))
	_ = c.(*net.UnixConn).CloseWrite()
	reply, _ := io.ReadAll(c)
	c.Close()
	if len(reply) != 0 || called.Load() {
		t.Fatalf("server accepted an oversized payload (reply %d bytes, handler called %v)", len(reply), called.Load())
	}
}

func TestForwardFailsFastWhenDaemonDown(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "missing.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := Forward(ctx, path, strings.NewReader(`{}`), io.Discard)
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("took %v; must fail fast", time.Since(start))
	}
}
