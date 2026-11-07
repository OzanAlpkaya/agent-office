package sandbox

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unixEcho serves a unix socket that answers every connection with prefix
// followed by what it received.
func unixEcho(t *testing.T, prefix string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c)
				io.WriteString(c, prefix+string(b))
			}()
		}
	}()
	return sock
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func initCfg(t *testing.T, cmd ...string) (InitConfig, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return InitConfig{
		EgressAddr: freeAddr(t), EgressSocket: unixEcho(t, "egress:"),
		APIAddr: freeAddr(t), APISocket: unixEcho(t, "api:"),
		Home: filepath.Join(t.TempDir(), "home"), Command: cmd,
		Env: []string{"PATH=" + os.Getenv("PATH"), "MARK=child-env"}, Stdout: &out, Stderr: &out,
	}, &out
}

// roundTrip sends msg to addr, half-closes, and returns the reply.
func roundTrip(t *testing.T, addr, msg string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, msg)
	c.(*net.TCPConn).CloseWrite()
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestRunInitBridgesWhileChildRuns(t *testing.T) {
	cfg, _ := initCfg(t, "sleep", "30")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() { code, _ := RunInit(ctx, cfg); result <- code }()

	var reply string
	for i := 0; i < 50; i++ { // the bridges come up before the child
		if c, err := net.Dial("tcp", cfg.EgressAddr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reply = roundTrip(t, cfg.EgressAddr, "CONNECT x"); reply != "egress:CONNECT x" {
		t.Fatalf("egress bridge reply %q", reply)
	}
	if reply = roundTrip(t, cfg.APIAddr, "POST /v1/messages"); reply != "api:POST /v1/messages" {
		t.Fatalf("api bridge reply %q", reply)
	}
	if fi, err := os.Stat(cfg.Home); err != nil || !fi.IsDir() {
		t.Fatalf("home not created: %v", err)
	}
	cancel() // SIGTERM to the child
	select {
	case code := <-result:
		if code != 128+15 {
			t.Fatalf("exit code %d, want 143 (SIGTERM)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not stopped on cancel")
	}
}

func TestRunInitPassesExitCodeAndEnv(t *testing.T) {
	cfg, out := initCfg(t, "sh", "-c", `echo "$MARK"; exit 7`)
	code, err := RunInit(context.Background(), cfg)
	if err != nil || code != 7 {
		t.Fatalf("code %d err %v", code, err)
	}
	if strings.TrimSpace(out.String()) != "child-env" {
		t.Fatalf("child output %q", out.String())
	}
}

func TestRunInitChildHandlesTerm(t *testing.T) {
	cfg, _ := initCfg(t, "sh", "-c", `trap 'exit 9' TERM; sleep 30 & wait`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	if code, err := RunInit(ctx, cfg); err != nil || code != 9 {
		t.Fatalf("code %d err %v, want the child's own exit 9", code, err)
	}
	// The background sleep keeps the output pipe open; WaitDelay bounds that.
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("RunInit waited %v for a grandchild", d)
	}
}

func TestRunInitCleanExitWithLingeringGrandchild(t *testing.T) {
	cfg, _ := initCfg(t, "sh", "-c", "sleep 30 & exit 0")
	if code, err := RunInit(context.Background(), cfg); err != nil || code != 0 {
		t.Fatalf("code %d err %v, want a clean 0", code, err)
	}
}

func TestRunInitRefusesWithoutBridges(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cases := map[string]func(c *InitConfig){
		"no egress socket":   func(c *InitConfig) { c.EgressSocket = "" },
		"missing api socket": func(c *InitConfig) { c.APISocket = filepath.Join(t.TempDir(), "nope.sock") },
		"address in use": func(c *InitConfig) {
			l, err := net.Listen("tcp", c.APIAddr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { l.Close() })
		},
		"no command": func(c *InitConfig) { c.Command = nil },
	}
	for name, mutate := range cases {
		cfg, _ := initCfg(t, "touch", marker)
		mutate(&cfg)
		if _, err := RunInit(context.Background(), cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: child started without its bridges", name)
		}
	}
}

func TestInitFromEnvDropsSocketVars(t *testing.T) {
	t.Setenv("AGENT_OFFICE_EGRESS_SOCKET", "/run/agent-office/egress.sock")
	t.Setenv("AGENT_OFFICE_API_SOCKET", "/run/agent-office/api.sock")
	t.Setenv("AGENT_OFFICE_SOCKET", "/run/agent-office/hook.sock")
	cfg := InitFromEnv([]string{"claude"})
	if cfg.EgressSocket != "/run/agent-office/egress.sock" || cfg.APISocket != "/run/agent-office/api.sock" ||
		cfg.EgressAddr != EgressAddr || cfg.APIAddr != APIAddr {
		t.Fatalf("config %+v", cfg)
	}
	env := strings.Join(cfg.Env, "\n")
	if strings.Contains(env, "AGENT_OFFICE_EGRESS_SOCKET") || strings.Contains(env, "AGENT_OFFICE_API_SOCKET") ||
		!strings.Contains(env, "AGENT_OFFICE_SOCKET=/run/agent-office/hook.sock") {
		t.Fatal("child env: socket vars for init must be dropped, the hook socket kept")
	}
}
