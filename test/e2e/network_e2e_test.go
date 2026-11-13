//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// cutProxy is a CONNECT proxy in front of the daemon's upstream
// (HTTPS_PROXY): Cut closes every tunnel and refuses new ones, as a network
// drop does; Restore lets traffic through again.
type cutProxy struct {
	l        net.Listener
	down     atomic.Bool
	mu       sync.Mutex
	open     map[net.Conn]bool
	received atomic.Int64 // bytes from upstream to the daemon, all tunnels
}

func newCutProxy(t *testing.T) *cutProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{l: l, open: map[net.Conn]bool{}}
	t.Cleanup(func() { l.Close(); p.Cut() })
	go p.serve()
	return p
}

func (p *cutProxy) URL() string { return "http://" + p.l.Addr().String() }

func (p *cutProxy) Cut() {
	p.down.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.open {
		c.Close()
	}
}

func (p *cutProxy) Restore() { p.down.Store(false) }

func (p *cutProxy) serve() {
	for {
		c, err := p.l.Accept()
		if err != nil {
			return
		}
		go p.tunnel(c)
	}
}

func (p *cutProxy) tunnel(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect || p.down.Load() {
		return // a dropped network: the connection just fails
	}
	up, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
	if err != nil {
		fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	p.mu.Lock()
	if p.down.Load() {
		p.mu.Unlock()
		return
	}
	p.open[c], p.open[up] = true, true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.open, c)
		delete(p.open, up)
		p.mu.Unlock()
	}()
	fmt.Fprint(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := up.Read(buf)
			if n > 0 {
				p.received.Add(int64(n))
				if _, werr := c.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}

// TestNetworkDropEndToEnd (Phase 20, ADR 0067): a sandboxed haiku agent's
// network is cut while its answer streams. Claude Code ends the turn with
// "Connection lost mid-response"; agent-office shows it waiting for the
// network, and once the network is back resumes it without any input, and
// the turn finishes.
func TestNetworkDropEndToEnd(t *testing.T) {
	src := os.Getenv("AO_E2E_REPO")
	if src == "" {
		t.Skip("AO_E2E_REPO not set; this test runs a real Claude Code session")
	}
	for _, bin := range []string{"claude", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	tokenPath := os.Getenv(sandbox.TokenFileEnv)
	if tokenPath == "" {
		var err error
		if tokenPath, err = sandbox.DefaultTokenFile(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sandbox.ReadTokenFile(tokenPath); err != nil {
		t.Skipf("no usable OAuth token file: %v", err)
	}
	model := os.Getenv("AO_E2E_MODEL")
	if model == "" {
		model = "haiku"
	}
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", data).Run() })
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", gitOut(t, src, "rev-parse", "--show-toplevel"), repo).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	bin := filepath.Join(tmp, "agent-office")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	proxy := newCutProxy(t)
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		env: []string{"HTTPS_PROXY=" + proxy.URL(), "https_proxy=" + proxy.URL(), "NO_PROXY=127.0.0.1,localhost",
			"no_proxy=127.0.0.1,localhost"},
		args: []string{"serve", "--repo", repo, "--addr", "127.0.0.1:" + port, "--data-dir", data,
			"--model", model, "--log-level", "debug", "--sandbox", "podman", "--pack", e2ePack()}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var agentID string
	t.Cleanup(func() {
		if d.cmd != nil && agentID != "" {
			d.do("POST", "/api/agents/"+agentID+"/stop", nil)
		}
		d.signal(syscall.SIGTERM)
		if agentID != "" {
			_ = exec.Command("podman", "rm", "-f", "ao-"+agentID).Run()
		}
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 80))
			}
		}
	})
	d.start()
	w := watch(t, ctx, d.base)

	var task taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E network drop",
		"description": "Write a story of about 1500 words about a lighthouse keeper, directly as your answer. Do not use " +
			"any tools and do not create files. If your answer is cut off, continue it from where it stopped."}, 201, &task)
	var a agentInfo
	d.json("POST", "/api/agents", map[string]string{"task_id": task.ID}, 201, &a)
	agentID = a.ID

	// Cut once the answer streams: the agent works and the upstream has sent
	// a good part of a response (the startup requests are small).
	w.waitFor(t, a.ID, "the turn to start", 3*time.Minute, func(h []string) bool {
		return len(h) > 0 && h[len(h)-1] == "working"
	})
	start := proxy.received.Load()
	deadline := time.Now().Add(2 * time.Minute)
	for proxy.received.Load()-start < 24<<10 {
		if time.Now().After(deadline) {
			t.Fatalf("the answer never streamed (%d bytes)", proxy.received.Load()-start)
		}
		time.Sleep(100 * time.Millisecond)
	}
	mark := w.mark(a.ID)
	proxy.Cut()
	t.Log("network cut")
	// Mid-response, Claude Code gives up at once; before it, it retries for
	// about two minutes first. Either way the agent ends up waiting.
	w.waitFor(t, a.ID, "the agent to wait for the network", 4*time.Minute, func(h []string) bool {
		return len(h) > mark && contains(h[mark:], "waiting_network")
	})
	time.Sleep(20 * time.Second)
	if h := w.snapshot(a.ID); h[len(h)-1] != "waiting_network" {
		t.Fatalf("while the network is down: %v", h[mark:])
	}
	mark = w.mark(a.ID)
	proxy.Restore()
	t.Log("network back")
	// Resumed without input: working again, then the turn finishes.
	w.waitFor(t, a.ID, "the resumed turn to finish", 5*time.Minute, func(h []string) bool {
		n := len(h)
		return n > mark && contains(h[mark:], "working") && h[n-1] == "idle"
	})
	logData, _ := os.ReadFile(d.logPath)
	if !strings.Contains(string(logData), "resumed after the network came back") {
		t.Fatalf("the daemon did not resume it; log:\n%s", tail(string(logData), 40))
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
