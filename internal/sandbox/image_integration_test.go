//go:build sandbox

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// workerRun starts a real worker container (WorkerArgs + sandbox-init) in a
// PTY, with host-side egress and API proxies on unix sockets, and a shell
// script in place of the agent CLI. It returns the terminal output and the
// proxy events.
func workerRun(t *testing.T, script string) (string, []Event) {
	t.Helper()
	p := Podman{}
	if ok, err := p.ImageExists(context.Background(), WorkerImage()); err != nil || !ok {
		t.Skipf("worker image %s missing: run make worker-image (%v)", WorkerImage(), err)
	}
	self, err := filepath.Abs("../../bin/agent-office")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("file", self).Output(); err != nil || !strings.Contains(string(out), "statically linked") {
		t.Skipf("needs a static bin/agent-office: run make build (%s)", out)
	}

	root := t.TempDir()
	dir := func(name string, mode os.FileMode) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, mode); err != nil {
			t.Fatal(err)
		}
		return d
	}
	ws, cfgDir, objects, socks := dir("ws", 0o755), dir("config", 0o755), dir("objects", 0o755), dir("socks", 0o700)
	hooks := filepath.Join(root, "git-hooks")
	if err := WriteGitHooks(hooks); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settings, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	allow, _ := ParseAllowlist(DefaultEgressAllow)
	egress := &EgressProxy{Allow: allow, OnEvent: rec.add, Log: slog.New(slog.DiscardHandler)}
	api := &APIProxy{Token: Secret{v: "fake-token-never-sent"}, OnEvent: rec.add, Log: slog.New(slog.DiscardHandler),
		Upstream: &urlNowhere}
	listen := func(name string) string {
		path := filepath.Join(socks, name)
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		if name == "egress.sock" {
			go func() { _ = egress.Serve(l) }()
		} else if name == "api.sock" {
			go func() { _ = http.Serve(l, api.Handler()) }()
		}
		return path
	}
	hookSock, egressSock, apiSock := listen("hook.sock"), listen("egress.sock"), listen("api.sock")
	if err := p.Chown(context.Background(), root, ws, cfgDir, hookSock, egressSock, apiSock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, d := range []string{ws, cfgDir} {
			if err := p.RemoveAll(context.Background(), root, d); err != nil {
				t.Errorf("cleanup %s: %v", d, err)
			}
		}
	})

	args, err := WorkerArgs(WorkerSpec{
		AgentID: "e2e0123456ab", Image: WorkerImage(), Workspace: ws, ObjectsDir: objects, ConfigDir: cfgDir,
		HookSocket: hookSock, EgressSocket: egressSock, APISocket: apiSock,
		AgentOffice: self, Claude: "/bin/true", Settings: settings, GitHooks: hooks,
		Git:     GitIdentity{Name: "Ozan", Email: "ozan@example.com", CoAuthor: CoAuthor("worker-e2e")},
		Command: []string{"sh", "-c", script},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), ContainerName("e2e0123456ab")) })
	cmd := exec.Command("podman", args...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 200, Rows: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&out, f); close(done) }()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		<-done
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatalf("worker did not finish; output so far:\n%s", out.String())
	}
	text := strings.ReplaceAll(out.String(), "\r\n", "\n")
	t.Logf("worker output:\n%s", text)
	return text, rec.all()
}

var urlNowhere = *AnthropicAPI // never contacted: the test only sends rejected requests

func expectLines(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w+"\n") {
			t.Errorf("missing %q in worker output:\n%s", w, out)
		}
	}
}

func TestWorkerImageAndInit(t *testing.T) {
	script := `
echo "uid=$(id -u) gid=$(id -g)"
grep -E '^(CapEff|NoNewPrivs):' /proc/self/status | tr -s '\t ' ' '
echo "net=$(ls /sys/class/net | tr '\n' ' ')"
go version | cut -d' ' -f3; go env CGO_ENABLED GOTOOLCHAIN
git --version >/dev/null && echo git=ok
rg --version >/dev/null && echo rg=ok
command -v ssh >/dev/null && echo ssh=present || echo ssh=absent
test -s /etc/ssl/certs/ca-certificates.crt && echo certs=ok
test -w / && echo rootfs=writable || echo rootfs=ro
echo "token=$CLAUDE_CODE_OAUTH_TOKEN"
test -d "$CLAUDE_CODE_TMPDIR" && test -O "$CLAUDE_CODE_TMPDIR" && echo tmpdir=own
env | grep -c '^AGENT_OFFICE_\(EGRESS\|API\)_SOCKET=' | sed 's/^/init-vars-leaked=/'
cd /workspace && git init -q -b main && git commit -q --allow-empty -m 'feat: x' \
  && git log -1 --format='author=%an <%ae>%n%(trailers:key=Co-authored-by)' | sed '/^$/d'
curl -s -o /dev/null -w 'api-models=%{http_code}\n' http://127.0.0.1:8080/v1/models
curl -s -o /dev/null -w 'api-noauth=%{http_code}\n' -X POST http://127.0.0.1:8080/v1/messages
`
	out, events := workerRun(t, script)
	expectLines(t, out,
		"uid=1000 gid=1000", "CapEff: 0000000000000000", "NoNewPrivs: 1", "net=lo ",
		"go1.27.3", "0", "local", "git=ok", "rg=ok", "ssh=absent", "certs=ok", "rootfs=ro",
		"token="+Placeholder, "tmpdir=own", "init-vars-leaked=0",
		"author=Ozan <ozan@example.com>", "Co-Authored-By: worker-e2e (agent-office) <noreply@agent-office.invalid>",
		"api-models=403", "api-noauth=401")
	kinds := map[EventKind]string{}
	for _, e := range events {
		kinds[e.Kind] = e.Detail
	}
	if kinds[APIRejected] != "GET /v1/models" || kinds[APIAuth] != "POST /v1/messages" {
		t.Fatalf("events %+v", events)
	}
}

// TestWorkerEgress needs internet access: AO_SANDBOX_NET=1 make sandbox-test.
func TestWorkerEgress(t *testing.T) {
	if os.Getenv("AO_SANDBOX_NET") != "1" {
		t.Skip("set AO_SANDBOX_NET=1 to test HTTPS through the egress proxy (needs internet)")
	}
	script := `
curl -sS -o /dev/null -w 'proxy.golang.org=%{http_code}\n' https://proxy.golang.org/github.com/google/uuid/@v/list
curl -sS -o /dev/null -w 'registry.npmjs.org=%{http_code}\n' https://registry.npmjs.org/left-pad
curl -sS -o /dev/null https://github.com/ 2>/dev/null && echo github=reachable || echo github=blocked
cd /tmp && mkdir m && cd m && go mod init example.com/m >/dev/null 2>&1 \
  && go get github.com/google/uuid@v1.6.0 >/dev/null 2>&1 && echo go-get=ok || echo go-get=failed
git ls-remote https://github.com/golang/go >/dev/null 2>&1 && echo git-github=reachable || echo git-github=blocked
`
	out, events := workerRun(t, script)
	expectLines(t, out, "proxy.golang.org=200", "registry.npmjs.org=200", "github=blocked", "go-get=ok", "git-github=blocked")
	denied := 0
	for _, e := range events {
		if e.Kind == EgressDenied && e.Detail == "github.com:443" {
			denied++
		}
	}
	if denied < 2 {
		t.Fatalf("want github.com:443 denials as events, got %+v", events)
	}
}
