//go:build sandbox

package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// TestSandboxedProjectsAreIsolated: a worker of project A sees neither the
// workspace of B's worker nor B's main repository, and its mounts come from
// its own project only (ADR 0046, Phase 14 isolation DoD).
func TestSandboxedProjectsAreIsolated(t *testing.T) {
	f := sandboxFixture(t, false)
	f.opts.MaxAgents = 4
	repoB := newRepo(t)
	gitIn(t, repoB, "config", "user.name", "Ozan Test")
	gitIn(t, repoB, "config", "user.email", "ozan@example.com")
	hf := &hubFixture{f: f}
	hf.h = hf.openHub(t)
	ctx := context.Background()
	var err error
	if hf.a, err = hf.h.Register(ctx, f.repo); err != nil {
		t.Fatal(err)
	}
	if hf.b, err = hf.h.Add(ctx, AddProject{Repo: repoB}); err != nil {
		t.Fatal(err)
	}
	hf.ea, hf.eb = hf.engines(t)
	logA, logB := record(t, hf.ea), record(t, hf.eb)
	a, err := hf.ea.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := hf.eb.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	logA.wait(t, 30*time.Second, agentState(a.ID, hooks.Idle))
	logB.wait(t, 30*time.Second, agentState(b.ID, hooks.Idle))
	if filepath.Dir(a.WorktreePath) == filepath.Dir(b.WorktreePath) {
		t.Fatalf("both projects' workspaces in %s", filepath.Dir(a.WorktreePath))
	}

	name := sandbox.ContainerName(a.ID)
	raw, err := exec.Command("podman", "inspect", "--format", "{{json .Mounts}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	var mounts []struct{ Source, Destination string }
	if err := json.Unmarshal(raw, &mounts); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	bWorkspaces := filepath.Dir(b.WorktreePath)
	sawOwn := false
	for _, m := range mounts {
		for _, forbidden := range []string{bWorkspaces, repoB} {
			if m.Source == forbidden || strings.HasPrefix(m.Source, forbidden+string(filepath.Separator)) {
				t.Errorf("A's worker mounts %s (project B) at %s", m.Source, m.Destination)
			}
		}
		if m.Source == a.WorktreePath {
			sawOwn = true
		}
	}
	if !sawOwn {
		t.Fatalf("A's own workspace %s is not among its mounts %+v", a.WorktreePath, mounts)
	}
	// From inside: B's paths do not exist, and nothing of B is in the mount table.
	script := `for p in "$@"; do [ -e "$p" ] && echo "visible: $p"; done; cat /proc/self/mountinfo; ls -a /workspace`
	out, err := exec.Command("podman", "exec", name, "sh", "-c", script, "sh",
		b.WorktreePath, bWorkspaces, repoB, filepath.Join(repoB, ".git")).CombinedOutput()
	if err != nil {
		t.Fatalf("podman exec: %v %s", err, out)
	}
	if strings.Contains(string(out), "visible: ") {
		t.Errorf("A's worker sees project B:\n%s", out)
	}
	for _, s := range []string{b.ID, hf.eb.Project().Slug} {
		if strings.Contains(string(out), s) {
			t.Errorf("A's worker's mounts or workspace name %q (project B):\n%s", s, out)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		e := hf.ea
		lg := logA
		if id == b.ID {
			e, lg = hf.eb, logB
		}
		exitAgent(t, e, lg, id)
	}
}
