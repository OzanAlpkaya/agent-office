package sandbox

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// secretsSocket serves vals once on a fresh socket, like the engine does.
func secretsSocket(t *testing.T, vals map[string]secret.Secret) (string, chan error) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "secrets.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	done := make(chan error, 1)
	go func() { done <- ServeSecretsOnce(l, vals) }()
	return sock, done
}

func TestSecretsServedOnceToInit(t *testing.T) {
	sock, done := secretsSocket(t, map[string]secret.Secret{"NPM_TOKEN_X": secret.New("canary-npm"), "DB_URL": secret.New("postgres://t@db/x")})
	cfg, out := initCfg(t, "sh", "-c", `echo "npm=$NPM_TOKEN_X db=$DB_URL mark=$MARK"`)
	cfg.SecretsSocket = sock
	cfg.Env = append(cfg.Env, "DB_URL=overridden-by-the-secret")
	if code, err := RunInit(context.Background(), cfg); err != nil || code != 0 {
		t.Fatalf("code %d err %v out %s", code, err, out)
	}
	if got := strings.TrimSpace(out.String()); got != "npm=canary-npm db=postgres://t@db/x mark=child-env" {
		t.Fatalf("child env: %q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Exactly once: the listener is gone after the first answer.
	if _, err := fetchSecrets(sock); err == nil {
		t.Fatal("secrets served twice")
	}
}

func TestInitFailsWithoutItsSecrets(t *testing.T) {
	cfg, out := initCfg(t, "sh", "-c", "echo started")
	cfg.SecretsSocket = filepath.Join(t.TempDir(), "missing.sock")
	if _, err := RunInit(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("want a secrets error, got %v", err)
	}
	if strings.Contains(out.String(), "started") {
		t.Fatal("the child started without its secrets")
	}
}

func TestFetchSecretsRefusesBadReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"not json":     "hello",
		"no env":       `{}`,
		"reserved":     `{"env":{"HTTPS_PROXY":"http://evil"}}`,
		"bad name":     `{"env":{"A-B":"x"}}`,
		"newline":      `{"env":{"A_B":"x\ny"}}`,
		"agent-office": `{"env":{"AGENT_OFFICE_SOCKET":"/x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			sock := filepath.Join(t.TempDir(), "s.sock")
			l, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go func() {
				c, err := l.Accept()
				if err == nil {
					_, _ = c.Write([]byte(reply))
					c.Close()
				}
			}()
			if _, err := fetchSecrets(sock); err == nil {
				t.Fatalf("accepted %s", reply)
			}
		})
	}
}

func TestServeSecretsOnceStopsWhenClosed(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ServeSecretsOnce(l, nil) }()
	l.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeSecretsOnce did not return")
	}
}

func TestWorkerArgsSecretsSocket(t *testing.T) {
	s := workerSpec()
	args, err := WorkerArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, secretsSocketEnv+"="+SecretsSocket) || strings.Contains(strings.Join(args, " "), "secrets.sock") {
		t.Fatal("no secrets socket without secrets")
	}
	s.SecretsSocket = "/run/user/1000/ao/a/secrets.sock"
	if args, err = WorkerArgs(s); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(flagValues(args, "-v"), s.SecretsSocket+":"+SecretsSocket) ||
		!slices.Contains(flagValues(args, "-e"), secretsSocketEnv+"="+SecretsSocket) {
		t.Fatalf("secrets socket not mounted: %v", args)
	}
	s.Skills = "/run/user/1000/ao/a/skills"
	if args, err = WorkerArgs(s); err != nil || !slices.Contains(flagValues(args, "-v"), s.Skills+":"+SkillsDir+":ro") {
		t.Fatalf("skills not mounted read-only: %v %v", args, err)
	}
	s.Env = []string{secretsSocketEnv + "=/elsewhere"}
	if _, err := WorkerArgs(s); err == nil {
		t.Fatal("callers may not set the secrets socket variable")
	}
}

func TestReservedEnv(t *testing.T) {
	for _, n := range []string{"HOME", "PATH", "GITHUB_TOKEN", "GH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR",
		"AGENT_OFFICE_X", "GIT_DIR", "SSH_AUTH_SOCK", "LD_PRELOAD", "HTTPS_PROXY", "NODE_OPTIONS", secretsSocketEnv} {
		if !ReservedEnv(n) {
			t.Errorf("%s not reserved", n)
		}
	}
	for _, n := range []string{"GOFLAGS", "GOPROXY", "CGO_ENABLED", "BASH_ENV", "ENV", "MCP_TIMEOUT", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"DISABLE_AUTOUPDATER", "npm_config_registry", "NPM_CONFIG_REGISTRY", "XDG_CONFIG_HOME", "LC_ALL", "REQUESTS_CA_BUNDLE", "NODE_PATH"} {
		if !ReservedEnv(n) {
			t.Errorf("%s not reserved", n)
		}
	}
	for _, n := range []string{"NPM_TOKEN", "NODE_AUTH_TOKEN", "DATABASE_URL", "GOOGLE_API_KEY"} {
		if ReservedEnv(n) {
			t.Errorf("%s reserved", n)
		}
	}
}

func TestPackImageTag(t *testing.T) {
	cf := BuildFile{Data: []byte("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n")}
	files := map[string]BuildFile{"Containerfile": cf, "B.Containerfile": cf, "a.txt": {Data: []byte("a")}}
	tag := PackImage("go", "Containerfile", files)
	if !strings.HasPrefix(tag, "localhost/agent-office-pack-go:") || len(tag) != len("localhost/agent-office-pack-go:")+12 {
		t.Fatal(tag)
	}
	if PackImage("go", "Containerfile", files) != tag {
		t.Fatal("tag not stable")
	}
	with := func(name string, f BuildFile) map[string]BuildFile {
		m := map[string]BuildFile{"Containerfile": cf, "B.Containerfile": cf}
		m[name] = f
		return m
	}
	for _, other := range []string{
		PackImage("go", "Containerfile", with("a.txt", BuildFile{Data: []byte("b")})),             // content
		PackImage("go", "Containerfile", with("b.txt", BuildFile{Data: []byte("a")})),             // name
		PackImage("go", "Containerfile", with("a.txt", BuildFile{Data: []byte("a"), Exec: true})), // exec bit
		PackImage("go", "B.Containerfile", files),                                                 // which Containerfile
		PackImage("node", "Containerfile", files),
	} {
		if other == tag {
			t.Fatal("a build input changed but the tag did not")
		}
	}
	if !strings.HasPrefix(BaseImage(), "localhost/agent-office-base:") {
		t.Fatal(BaseImage())
	}
}
