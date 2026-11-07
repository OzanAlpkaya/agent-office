package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// InitConfig is what `agent-office sandbox-init` needs inside a worker
// (ADR 0016). FromEnv fills it from the variables WorkerArgs sets.
type InitConfig struct {
	EgressAddr, EgressSocket string   // loopback address bridged to the egress socket
	APIAddr, APISocket       string   // loopback address bridged to the API socket
	SecretsSocket            string   // optional: stack pack secrets for the child's env (ADR 0023)
	Dirs                     []string // created if missing (HOME, CLAUDE_CODE_TMPDIR; on the tmpfs)
	Command                  []string
	Env                      []string // the child's environment
	Stdin                    io.Reader
	Stdout, Stderr           io.Writer
}

// InitFromEnv builds the in-container config from the worker environment.
// The socket variables are only for sandbox-init and are not passed on.
func InitFromEnv(command []string) InitConfig {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "AGENT_OFFICE_EGRESS_SOCKET" && k != "AGENT_OFFICE_API_SOCKET" && k != secretsSocketEnv {
			env = append(env, kv)
		}
	}
	return InitConfig{
		EgressAddr: EgressAddr, EgressSocket: os.Getenv("AGENT_OFFICE_EGRESS_SOCKET"),
		APIAddr: APIAddr, APISocket: os.Getenv("AGENT_OFFICE_API_SOCKET"), SecretsSocket: os.Getenv(secretsSocketEnv),
		Dirs: nonEmpty(os.Getenv("HOME"), os.Getenv("CLAUDE_CODE_TMPDIR")), Command: command, Env: env,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}
}

// RunInit starts both bridges, then runs the command as a child with the
// terminal inherited, and returns its exit code. Cancelling ctx sends SIGTERM
// to the child (the caller maps SIGTERM/SIGHUP to cancellation); SIGINT and
// SIGQUIT must be ignored by the caller, since the tty delivers them to the
// child directly. A bridge that cannot start is an error before the child
// runs, so an agent never starts without its proxies; the same holds for
// secrets that cannot be fetched.
func RunInit(ctx context.Context, cfg InitConfig) (int, error) {
	if len(cfg.Command) == 0 {
		return 0, errors.New("sandbox-init: no command")
	}
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, b := range []struct{ addr, sock string }{{cfg.EgressAddr, cfg.EgressSocket}, {cfg.APIAddr, cfg.APISocket}} {
		if b.sock == "" {
			return 0, fmt.Errorf("sandbox-init: no socket for %s", b.addr)
		}
		if _, err := os.Stat(b.sock); err != nil {
			return 0, fmt.Errorf("sandbox-init: socket for %s: %w", b.addr, err)
		}
		l, err := net.Listen("tcp", b.addr)
		if err != nil {
			return 0, fmt.Errorf("sandbox-init: bridge %s: %w", b.addr, err)
		}
		listeners = append(listeners, l)
		go Bridge(l, b.sock)
	}
	for _, d := range cfg.Dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return 0, fmt.Errorf("sandbox-init: %w", err)
		}
	}

	env := cfg.Env
	if cfg.SecretsSocket != "" {
		secrets, err := fetchSecrets(cfg.SecretsSocket)
		if err != nil {
			return 0, err
		}
		env = withEnv(env, secrets)
	}

	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = cfg.Stdin, cfg.Stdout, cfg.Stderr
	// A background grandchild can keep inherited pipes open after the child
	// exits; do not wait for it beyond this.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("sandbox-init: start %s: %w", cfg.Command[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			return exitCode(err)
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			return exitCode(<-done)
		}
	}
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func exitCode(err error) (int, error) {
	if err == nil || errors.Is(err, exec.ErrWaitDelay) { // exited 0; only a grandchild held the pipes
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}

// Bridge accepts TCP connections on l and pipes each to the unix socket sock.
func Bridge(l net.Listener, sock string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.DialTimeout("unix", sock, 5*time.Second)
			if err != nil {
				return
			}
			defer u.Close()
			splice(c, u)
		}()
	}
}
