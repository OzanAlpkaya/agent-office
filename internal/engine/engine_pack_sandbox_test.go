//go:build sandbox

package engine

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// usePack sets p on the fixture the way the daemon does: its image (if it
// has a toolchain), its allowlist and the secrets dir.
func usePack(t *testing.T, f *fixture, p *pack.Pack, secretsDir string) {
	t.Helper()
	files, err := p.BuildFiles()
	if err != nil {
		t.Fatal(err)
	}
	s := f.opts.Sandbox
	if files != nil {
		s.Image = sandbox.PackImage(p.Name, files)
		if ok, err := s.Podman.ImageExists(context.Background(), s.Image); err != nil || !ok {
			t.Skipf("pack image %s missing: run make worker-image PACK=%s (%v)", s.Image, p.Name, err)
		}
	}
	if s.Allow, err = sandbox.ParseAllowlist(p.Egress); err != nil {
		t.Fatal(err)
	}
	s.AllowNames, s.Pack, s.SecretsDir = p.Egress, p, secretsDir
}

// TestSandboxedPackLaunch is the Phase 6 DoD at launch (ADR 0023): the pack's
// skills reach the agent's config dir, its MCP servers its MCP config, and
// its secret reaches the agent CLI's environment once and nothing else: not
// sandbox-init, podman inspect, the runtime dir, logs or the database. Leads
// get no secrets; a missing required secret refuses the start.
func TestSandboxedPackLaunch(t *testing.T) {
	f := sandboxFixture(t, false)
	root := t.TempDir()
	dir := filepath.Join(root, "packs", "demo")
	for name, data := range map[string]string{
		"pack.yaml": "schema: 1\nname: demo\ndescription: Engine test pack.\negress:\n  allow: [example.com]\n" +
			"env:\n  - {name: DEMO_SECRET, required: true}\n  - {name: DEMO_OPT}\nskills: [demo-skill]\n" +
			"mcp_servers:\n  docs: {url: 'https://example.com/mcp'}\n  tool: {command: /usr/bin/true, args: [mcp]}\n",
		"skills/demo-skill/SKILL.md": "---\nname: demo-skill\ndescription: A test skill.\n---\nUse it.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(root, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := "demo-canary-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.WriteFile(filepath.Join(secrets, "DEMO_SECRET"), []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	usePack(t, f, p, secrets)
	var logs lockedBuffer
	f.opts.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()

	task, _ := e.CreateTask(ctx, TaskInput{Title: "Use the pack"})
	a, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
	typeLine(t, e, a.ID, `sh echo "secret=$DEMO_SECRET opt=${DEMO_OPT:-unset} sock=${AGENT_OFFICE_SECRETS_SOCKET:-unset}"`)
	screen(t, e, a.ID, "sh: secret="+canary+" opt=unset sock=unset\n")
	typeLine(t, e, a.ID, `sh grep -c "$DEMO_SECRET" /proc/1/environ /proc/1/cmdline | tr '\n' ' '; echo`)
	screen(t, e, a.ID, "sh: /proc/1/environ:0 /proc/1/cmdline:0 \n")
	typeLine(t, e, a.ID, `sh curl -s --unix-socket /run/agent-office/secrets.sock http://x/ >/dev/null; echo "second-fetch=$?"`)
	screen(t, e, a.ID, "sh: second-fetch=7\n") // connection refused: served once
	typeLine(t, e, a.ID, `sh head -2 /config/skills/demo-skill/SKILL.md | tail -1`)
	screen(t, e, a.ID, "sh: name: demo-skill\n")
	typeLine(t, e, a.ID, `sh grep -c -e '"url": "https://example.com/mcp"' -e '"command": "/usr/bin/true"' -e '"agent-office"' /opt/agent-office/mcp.json`)
	screen(t, e, a.ID, "sh: 3\n")

	inspect, err := exec.Command("podman", "inspect", sandbox.ContainerName(a.ID)).Output()
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "podman inspect", inspect, canary)
	noToken(t, "the runtime dir", hostFiles(t, filepath.Join(f.opts.RuntimeDir, "agents", a.ID), ""), canary)

	st := e.Status()
	if st.Pack == nil || st.Pack.Name != "demo" || len(st.Pack.Secrets) != 2 || !st.Pack.Secrets[0].Present ||
		st.Pack.Secrets[1].Present || st.Pack.MCPServers[0] != "docs" || strings.Join(st.Egress, ",") != "example.com" {
		t.Fatalf("status %+v", st.Pack)
	}

	// A lead plans; it gets no secrets.
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship it"})
	lead, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleLead})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 30*time.Second, agentState(lead.ID, hooks.Idle))
	typeLine(t, e, lead.ID, `sh echo "lead-secret=${DEMO_SECRET:-none}"; test -e /run/agent-office/secrets.sock && echo lead-sock=yes || echo lead-sock=no`)
	screen(t, e, lead.ID, "sh: lead-secret=none\n")
	screen(t, e, lead.ID, "sh: lead-sock=no\n")
	exitAgent(t, e, log, lead.ID)
	exitAgent(t, e, log, a.ID)

	// A missing required secret refuses the start before any state exists.
	if err := os.Remove(filepath.Join(secrets, "DEMO_SECRET")); err != nil {
		t.Fatal(err)
	}
	before := len(e.List())
	if _, err := e.Start(ctx, StartRequest{}); err == nil || !strings.Contains(err.Error(), "stack pack demo needs DEMO_SECRET") {
		t.Fatalf("start without the required secret: %v", err)
	}
	if len(e.List()) != before {
		t.Fatal("a refused start created an agent")
	}

	e.Shutdown()
	noToken(t, "the logs", logs.Bytes(), canary)
	db, err := os.ReadFile(f.dbp)
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "the database", db, canary)
	if !strings.Contains(string(logs.Bytes()), "secrets=[DEMO_SECRET]") {
		t.Error("the start log should name the secrets the agent got")
	}
}

// TestSandboxedPackToolchains runs the built-in packs' images (ADR 0023): the
// go pack has cgo, so go test -race works; the node pack has node and npm.
// Needs make worker-image PACK=go and PACK=node; no network.
func TestSandboxedPackToolchains(t *testing.T) {
	cases := []struct {
		pack  string
		lines []string
		want  []string
	}{
		{"go", []string{
			`sh go version | cut -d' ' -f3; go env CGO_ENABLED GOTOOLCHAIN`,
			`sh mkdir -p /tmp/m && cd /tmp/m && printf 'module m\n\ngo 1.27\n' > go.mod && printf 'package m\n\nimport "testing"\n\nfunc TestX(t *testing.T) {\n\tdone := make(chan int)\n\tgo func() { done <- 1 }()\n\t<-done\n}\n' > m_test.go && go test -race -count=1 ./... >/dev/null && echo race=ok`,
			`sh test -s /config/skills/go-sandbox/SKILL.md && echo skill=ok`,
		}, []string{"sh: go1.27.3\n", "sh: 1\n", "sh: local\n", "sh: race=ok\n", "sh: skill=ok\n"}},
		{"node", []string{
			`sh node -e 'console.log("node=" + (1 + 1))'; npm --version >/dev/null && echo npm=ok`,
			`sh test -s /config/skills/node-sandbox/SKILL.md && echo skill=ok`,
		}, []string{"sh: node=2\n", "sh: npm=ok\n", "sh: skill=ok\n"}},
	}
	for _, c := range cases {
		t.Run(c.pack, func(t *testing.T) {
			f := sandboxFixture(t, false)
			p, err := pack.Find(c.pack, "")
			if err != nil {
				t.Fatal(err)
			}
			usePack(t, f, p, "")
			e := f.engine(t)
			log := record(t, e)
			a, err := e.Start(context.Background(), StartRequest{})
			if err != nil {
				t.Fatal(err)
			}
			log.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
			for _, l := range c.lines {
				typeLine(t, e, a.ID, l)
			}
			for _, w := range c.want {
				screen(t, e, a.ID, w)
			}
			exitAgent(t, e, log, a.ID)
		})
	}
}
