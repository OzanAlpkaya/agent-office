package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Podman runs the podman CLI. The zero value uses "podman" from PATH and a
// 60 s timeout per call (Run uses the caller's context only).
type Podman struct {
	Bin     string
	Timeout time.Duration
}

// ErrOutputTooLarge means a helper wrote more than the caller's limit.
var ErrOutputTooLarge = errors.New("sandbox: helper output larger than the limit")

// ErrUnsafePath means a path given to RemoveAll or Chown failed validation.
var ErrUnsafePath = errors.New("sandbox: unsafe path")

const maxStderr = 8 << 10

func (p Podman) bin() string {
	if p.Bin == "" {
		return "podman"
	}
	return p.Bin
}

func (p Podman) call(ctx context.Context, args ...string) (string, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out bytes.Buffer
	err := p.Run(ctx, args, &out, 1<<20)
	return strings.TrimSpace(out.String()), err
}

// Run runs podman with args (e.g. from HelperArgs) and streams its stdout to
// stdout, failing with ErrOutputTooLarge after max bytes. Stderr is kept, up
// to a bound, for the error message.
func (p Podman) Run(ctx context.Context, args []string, stdout io.Writer, max int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin(), args...)
	cmd.Env = podmanEnv()
	lw := &limitWriter{w: stdout, n: max, cancel: cancel}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = lw, &limitWriter{w: &stderr, n: maxStderr}
	err := cmd.Run()
	if lw.exceeded {
		return ErrOutputTooLarge
	}
	if err != nil {
		name := "podman"
		if len(args) > 0 {
			name += " " + args[0]
		}
		return fmt.Errorf("sandbox: %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// podmanEnv is the daemon's environment without variables that would point
// podman or the container at host credentials.
func podmanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if credentialEnv[k] || k == "CLAUDE_CODE_OAUTH_TOKEN" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type limitWriter struct {
	w        io.Writer
	n        int64
	exceeded bool
	cancel   func()
}

func (l *limitWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > l.n {
		l.exceeded = true
		if l.cancel != nil {
			l.cancel() // stop the process; a discarding writer would let it run on
			return 0, ErrOutputTooLarge
		}
		b = b[:l.n] // stderr: keep what fits, drop the rest
	}
	l.n -= int64(len(b))
	if len(b) == 0 {
		return 0, nil
	}
	return l.w.Write(b)
}

// Command returns an unstarted podman command (e.g. a worker for a PTY), with
// credential variables removed from its environment.
func (p Podman) Command(args []string) *exec.Cmd {
	cmd := exec.Command(p.bin(), args...)
	cmd.Env = podmanEnv()
	return cmd
}

// RunContainer runs a named container like Run. If the run fails, exceeds
// max or is cancelled, the container is removed by name: killing the podman
// client leaves the container running under conmon.
func (p Podman) RunContainer(ctx context.Context, name string, args []string, stdout io.Writer, max int64) error {
	err := p.Run(ctx, args, stdout, max)
	if err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rerr := p.Remove(rctx, name); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}

// Version returns the podman client version.
func (p Podman) Version(ctx context.Context) (string, error) {
	return p.call(ctx, "version", "--format", "{{.Client.Version}}")
}

// ImageExists reports whether ref exists in local storage.
func (p Podman) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := p.call(ctx, "image", "exists", "--", ref)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Container is an agent-office container found by its labels.
type Container struct {
	Name     string
	Instance string
	AgentID  string
	Role     string // worker | helper
}

// ListAgentContainers lists every container (running or not) that carries
// agent-office's labels, for startup reconciliation.
func (p Podman) ListAgentContainers(ctx context.Context) ([]Container, error) {
	out, err := p.call(ctx, "ps", "--all", "--filter", "label="+LabelRole, "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
	}
	if out != "" {
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			return nil, fmt.Errorf("sandbox: podman ps output: %w", err)
		}
	}
	var list []Container
	for _, r := range raw {
		if len(r.Names) == 0 {
			continue
		}
		list = append(list, Container{Name: r.Names[0], Instance: r.Labels[LabelInstance],
			AgentID: r.Labels[LabelAgent], Role: r.Labels[LabelRole]})
	}
	return list, nil
}

// Remove stops (5 s grace) and removes a container; a missing one is fine.
func (p Podman) Remove(ctx context.Context, name string) error {
	if !strings.HasPrefix(name, containerPrefix) {
		return fmt.Errorf("sandbox: refusing to remove container %q outside %s*", name, containerPrefix)
	}
	_, err := p.call(ctx, "rm", "--force", "--ignore", "--time", "5", "--", name)
	return err
}

// Chown hands paths (recursively, symlinks not followed) to the container
// user, from inside podman's user namespace (ADR 0014). Each path must lie
// strictly inside root.
func (p Podman) Chown(ctx context.Context, root string, paths ...string) error {
	for _, path := range paths {
		if err := checkInside(root, path); err != nil {
			return err
		}
	}
	_, err := p.call(ctx, append([]string{"unshare", "chown", "-R", "--no-dereference", ContainerUser, "--"}, paths...)...)
	return err
}

// RemoveAll deletes path, which may contain files owned by the container
// user, from inside podman's user namespace. That runs as root within the
// namespace, so path must lie strictly inside root (never root itself), with
// symlinks in its parents resolved, and the removal stays on one file system.
func (p Podman) RemoveAll(ctx context.Context, root, path string) error {
	if err := checkInside(root, path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err := p.call(ctx, "unshare", "rm", "-rf", "--one-file-system", "--", path)
	return err
}

// checkInside requires root and path to be clean absolute paths, root an
// existing directory, and path strictly below it after resolving symlinks in
// path's parents.
func checkInside(root, path string) error {
	for _, s := range []string{root, path} {
		if !filepath.IsAbs(s) || filepath.Clean(s) != s || s == "/" {
			return fmt.Errorf("%w: %q must be a clean absolute path", ErrUnsafePath, s)
		}
	}
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: root %s: %v", ErrUnsafePath, root, err)
	}
	if fi, err := os.Stat(rr); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: root %s is not a directory", ErrUnsafePath, root)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnsafePath, path, err)
	}
	rel, err := filepath.Rel(rr, filepath.Join(parent, filepath.Base(path)))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is not inside %s", ErrUnsafePath, path, root)
	}
	return nil
}
