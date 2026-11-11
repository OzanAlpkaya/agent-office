package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

func TestPackFlags(t *testing.T) {
	for _, k := range []string{"AGENT_OFFICE_PACK", "AGENT_OFFICE_PACKS_DIR", "AGENT_OFFICE_SECRETS_DIR"} {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	c, err := parseServe([]string{"--repo", "/tmp"})
	if err != nil || c.pack != "" || c.packsDir != "/cfg/agent-office/packs" || c.secretsDir != "/cfg/agent-office/secrets" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	t.Setenv("AGENT_OFFICE_PACK", "go")
	if c, err := parseServe([]string{"--repo", "/tmp"}); err != nil || c.pack != "go" {
		t.Fatalf("env: %+v %v", c, err)
	}
	if _, err := parseServe([]string{"--repo", "/tmp", "--sandbox", "host"}); err == nil || !strings.Contains(err.Error(), "--pack needs sandboxed agents") {
		t.Fatalf("host mode with a pack: %v", err)
	}
}

func writeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func TestPackOptions(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(repo, "go.mod"), "module x\n", 0o644)
	packs, secrets := filepath.Join(root, "packs"), filepath.Join(root, "secrets")
	cfg := packSpec{label: "--pack", repo: repo, packsDir: packs, secretsDir: secrets, egress: []string{"extra.example.com"}}

	// No pack: base image, the Phase 3 default allowlist plus extras, a hint for the repository.
	sp, warnings, err := packOptions(cfg)
	if err != nil || sp.pack != nil || sp.image != sandbox.BaseImage() ||
		strings.Join(sp.hosts, ",") != strings.Join(append(append([]string{}, sandbox.DefaultEgressAllow...), "extra.example.com"), ",") {
		t.Fatalf("%+v %v", sp, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "has go.mod") || !strings.Contains(warnings[0], "pack to go") {
		t.Fatalf("hint %v", warnings)
	}

	// The go pack: its image and only its hosts plus extras.
	cfg.name = "go"
	sp, warnings, err = packOptions(cfg)
	if err != nil || sp.pack == nil || !strings.HasPrefix(sp.image, "localhost/agent-office-pack-go:") ||
		strings.Join(sp.hosts, ",") != "proxy.golang.org,sum.golang.org,storage.googleapis.com,extra.example.com" || len(warnings) != 0 {
		t.Fatalf("%+v %v %v", sp, warnings, err)
	}

	cfg.name = "rust"
	if _, _, err := packOptions(cfg); err == nil || !strings.Contains(err.Error(), `--pack: unknown stack pack "rust"`) {
		t.Fatalf("unknown pack: %v", err)
	}

	// A user pack with a required and an optional secret.
	writeFile(t, filepath.Join(packs, "svc", "pack.yaml"), "schema: 1\nname: svc\ndescription: A service.\nenv:\n"+
		"  - {name: SVC_KEY, required: true}\n  - {name: SVC_OPT}\n", 0o600)
	cfg.name = "svc"
	if _, _, err := packOptions(cfg); err == nil || !strings.Contains(err.Error(), "stack pack svc needs SVC_KEY") {
		t.Fatalf("missing required secret: %v", err)
	}
	writeFile(t, filepath.Join(secrets, "SVC_KEY"), "value\n", 0o600)
	sp, warnings, err = packOptions(cfg)
	if err != nil || sp.image != sandbox.BaseImage() || len(sp.hosts) != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "optional secret SVC_OPT") {
		t.Fatalf("%+v %v %v", sp, warnings, err)
	}
}

func TestPackCommand(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	if code := packCommand([]string{"list", "--packs-dir", filepath.Join(root, "none")}, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "go    builtin") || !strings.Contains(out.String(), "node  builtin") {
		t.Fatalf("list: %d %s %s", code, out.String(), errb.String())
	}
	bad := filepath.Join(root, "bad")
	writeFile(t, filepath.Join(bad, "pack.yaml"), "schema: 1\nname: bad\negress:\n  allow: ['*']\n", 0o600)
	out.Reset()
	if code := packCommand([]string{"check", bad}, &out, &errb); code != 1 ||
		!strings.Contains(out.String(), "description: missing") || !strings.Contains(out.String(), `egress.allow[0]: "*" is not a host name`) {
		t.Fatalf("check: %d %s", code, out.String())
	}
	if code := packCommand(nil, &out, &errb); code != 2 {
		t.Fatal("no subcommand")
	}
}
