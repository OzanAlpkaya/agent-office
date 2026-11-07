package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPush(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo := newRepo(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitOut(t, repo, "init", "-q", "--bare", bare)
	gitOut(t, repo, "remote", "add", "origin", bare)
	if u, err := g.RemoteURL(ctx, repo, "origin"); err != nil || u != bare {
		t.Fatalf("remote url %q %v", u, err)
	}
	branch := BranchPrefix + "x-abc123"
	gitOut(t, repo, "branch", branch)
	gitOut(t, repo, "tag", "v1")
	if err := g.Push(ctx, repo, "origin", branch); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, bare, "rev-parse", branch); got != gitOut(t, repo, "rev-parse", branch) {
		t.Fatal("branch not pushed")
	}
	if tags := gitOut(t, bare, "tag"); tags != "" {
		t.Fatalf("tags pushed: %q", tags)
	}

	// A fast-forward is pushed; a rewritten branch is refused (never forced).
	gitOut(t, repo, "checkout", "-q", branch)
	commitIn(t, repo, "a.txt")
	if err := g.Push(ctx, repo, "origin", branch); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	gitOut(t, repo, "reset", "-q", "--hard", "HEAD~1")
	commitIn(t, repo, "b.txt")
	if err := g.Push(ctx, repo, "origin", branch); err == nil {
		t.Fatal("non-fast-forward push accepted")
	}

	for _, bad := range []struct{ remote, branch string }{
		{"origin", "main"}, {"--upload-pack=x", branch}, {"origin", BranchPrefix + "a..b"}, {"", branch},
	} {
		if err := g.Push(ctx, repo, bad.remote, bad.branch); err == nil {
			t.Errorf("push %q %q accepted", bad.remote, bad.branch)
		}
	}
	if _, err := g.RemoteURL(ctx, repo, "-v"); err == nil {
		t.Error("option as remote name accepted")
	}
	if h, err := g.RevParse(ctx, repo, branch); err != nil || h != gitOut(t, repo, "rev-parse", branch) {
		t.Fatalf("rev-parse %q %v", h, err)
	}
	if _, err := g.RevParse(ctx, repo, "--all"); err == nil {
		t.Error("option as revision accepted")
	}
}

func TestPushRefusesUntrustedRepo(t *testing.T) {
	repo := newRepo(t)
	g := Git{UntrustedRoots: []string{filepath.Dir(repo)}}
	if err := g.Push(context.Background(), repo, "origin", BranchPrefix+"x"); err == nil {
		t.Fatal("push from an agent repository accepted")
	}
}

func TestChangedFiles(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	commitIn(t, repo, "a.txt")
	files, err := Git{}.ChangedFiles(ctx, repo, base, "HEAD")
	if err != nil || len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("%v %v", files, err)
	}
	if _, err := (Git{}).ChangedFiles(ctx, repo, "--output=/tmp/x", "HEAD"); err == nil {
		t.Fatal("option as revision accepted")
	}
}
