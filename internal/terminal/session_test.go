package terminal

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func bash(t *testing.T, opts Options) *Session {
	t.Helper()
	cmd := exec.Command("bash", "--norc", "--noprofile")
	cmd.Env = []string{"TERM=xterm-256color", "PS1=$ ", "PATH=/usr/bin:/bin"}
	s, err := Start(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.Stop(ctx)
	})
	return s
}

// readUntil collects output from sub until it contains want.
func readUntil(t *testing.T, sub *Subscriber, want string) string {
	t.Helper()
	var out strings.Builder
	timeout := time.After(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		select {
		case chunk, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscriber closed while waiting for %q; got %q", want, out.String())
			}
			out.Write(chunk)
		case <-timeout:
			t.Fatalf("timeout waiting for %q; got %q", want, out.String())
		}
	}
	return out.String()
}

func TestInputOutputAndResize(t *testing.T) {
	s := bash(t, Options{})
	_, sub, ok := s.Attach()
	if !ok {
		t.Fatal("attach failed")
	}
	defer sub.Close()

	// The echoed command line contains "$((40+2))", so "hello-42" proves execution.
	if _, err := s.Write([]byte("echo hello-$((40+2))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, sub, "hello-42")

	if err := s.Resize(Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write([]byte("stty size\n"))
	readUntil(t, sub, "40 120")
	if got := s.Size(); got != (Size{Cols: 120, Rows: 40}) {
		t.Fatalf("Size() = %+v", got)
	}
}

func TestReattachReplaysScrollbackAndKeepsProcess(t *testing.T) {
	s := bash(t, Options{})
	_, sub, _ := s.Attach()
	_, _ = s.Write([]byte("X=kept; echo first-$((1+1))\n"))
	readUntil(t, sub, "first-2")
	sub.Close()
	sub.Close() // idempotent

	snapshot, sub2, ok := s.Attach()
	if !ok {
		t.Fatal("process should still run after detach")
	}
	defer sub2.Close()
	if !strings.Contains(string(snapshot), "first-2") {
		t.Fatalf("snapshot missing earlier output: %q", snapshot)
	}
	_, _ = s.Write([]byte("echo got-$X\n"))
	readUntil(t, sub2, "got-kept")
}

func TestScrollbackIsBounded(t *testing.T) {
	s := bash(t, Options{ScrollbackBytes: 1024})
	_, sub, _ := s.Attach()
	defer sub.Close()
	_, _ = s.Write([]byte("for i in $(seq 1 500); do echo line-$i; done; echo end-$((7*6))\n"))
	readUntil(t, sub, "end-42")
	snapshot, _, _ := s.Attach()
	if len(snapshot) > 1024 {
		t.Fatalf("scrollback %d bytes, limit 1024", len(snapshot))
	}
	if !strings.Contains(string(snapshot), "end-42") {
		t.Fatal("scrollback should keep the most recent output")
	}
}

func TestExitClosesSubscribersAndRecordsStatus(t *testing.T) {
	s := bash(t, Options{})
	_, sub, _ := s.Attach()
	_, _ = s.Write([]byte("exit 3\n"))
	for range sub.C { // drains until closed
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session not done after exit")
	}
	var exitErr *exec.ExitError
	if err := s.ExitErr(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("ExitErr = %v, want exit status 3", err)
	}
	if _, _, ok := s.Attach(); ok {
		t.Fatal("Attach after exit should report ok=false")
	}
	if _, err := s.Write([]byte("x")); !errors.Is(err, ErrExited) {
		t.Fatalf("Write after exit: %v", err)
	}
}

func TestBackgroundChildDoesNotKeepSessionAlive(t *testing.T) {
	// The background sleep keeps the PTY open after sh exits.
	cmd := exec.Command("sh", "-c", "sleep 30 & echo started")
	s, err := Start(cmd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session kept alive by a background child")
	}
}

func TestSlowSubscriberIsDroppedWithoutBlockingProcess(t *testing.T) {
	s := bash(t, Options{SubscriberBuffer: 1})
	_, slow, _ := s.Attach() // never read while output floods
	_, _ = s.Write([]byte("seq 1 20000\n"))

	deadline := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-slow.C:
			closed = !ok
		case <-deadline:
			t.Fatal("slow subscriber was not dropped")
		}
	}
	// The process was never blocked: it still runs commands. Check via the
	// scrollback, since any viewer with a 1-chunk buffer may be dropped too.
	_, _ = s.Write([]byte("echo alive-$((6*7))\n"))
	deadline = time.After(5 * time.Second)
	for {
		snapshot, sub, ok := s.Attach()
		if !ok {
			t.Fatal("process exited")
		}
		sub.Close()
		if strings.Contains(string(snapshot), "alive-42") {
			return
		}
		select {
		case <-deadline:
			t.Fatal("process did not produce fresh output after the slow viewer was dropped")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestStopTerminatesProcessGroup(t *testing.T) {
	s := bash(t, Options{})
	_, sub, _ := s.Attach()
	defer sub.Close()
	_, _ = s.Write([]byte("trap '' HUP TERM; echo ready-$((1+1))\n")) // ignore both: forces the SIGKILL path
	readUntil(t, sub, "ready-2")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.Stop(ctx)
	if !s.Exited() {
		t.Fatal("not exited after Stop")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Stop took too long")
	}
}

func TestRedrawRestoresSize(t *testing.T) {
	s := bash(t, Options{Size: Size{Cols: 100, Rows: 30}})
	if err := s.Redraw(); err != nil {
		t.Fatal(err)
	}
	if got := s.Size(); got != (Size{Cols: 100, Rows: 30}) {
		t.Fatalf("size after Redraw = %+v", got)
	}
}

// Snapshot's counter moves only when output arrives (Phase 9 saves the
// scrollback when it changed).
func TestSnapshotCounter(t *testing.T) {
	s := bash(t, Options{})
	_, sub, _ := s.Attach()
	defer sub.Close()
	if _, err := s.Write([]byte("echo snap-$((1+1))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, sub, "snap-2")
	time.Sleep(100 * time.Millisecond)
	data, gen := s.Snapshot()
	if !strings.Contains(string(data), "snap-2") || gen == 0 {
		t.Fatalf("snapshot %q gen %d", data, gen)
	}
	if _, again := s.Snapshot(); again != gen {
		t.Fatalf("counter moved without output: %d -> %d", gen, again)
	}
	if _, err := s.Write([]byte("echo more\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, sub, "more\r\n")
	if _, after := s.Snapshot(); after == gen {
		t.Fatal("counter did not move with output")
	}
}
