package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitT runs git in dir and returns its trimmed output.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// featureRepo is a repository on main with a feature branch two commits ahead.
func featureRepo(t *testing.T) (repo, main, feature string) {
	t.Helper()
	repo = newRepo(t)
	gitT(t, repo, "config", "user.name", "Ozan")
	gitT(t, repo, "config", "user.email", "ozan@example.com")
	main = gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "switch", "-q", "-c", "agent-office/qa")
	commitIn(t, repo, "a.txt")
	commitIn(t, repo, "b.txt")
	feature = gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "switch", "-q", "main")
	return repo, main, feature
}

func TestSquashMergeIntoCheckedOutBranch(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo, main, feature := featureRepo(t)
	if b, err := g.CurrentBranch(ctx, repo); err != nil || b != "main" {
		t.Fatalf("current branch %q %v", b, err)
	}
	sha, _, err := g.SquashCommit(ctx, repo, main, feature, "feat: a and b\n\nAgent-Office-Goal: g1\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, repo, "rev-parse", "main"); got != main {
		t.Fatal("SquashCommit moved the branch")
	}
	if dirty, err := g.TrackedChanges(ctx, repo); err != nil || dirty {
		t.Fatalf("tracked changes %v %v", dirty, err)
	}
	if err := g.FastForwardWorktree(ctx, repo, sha); err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, repo, "rev-parse", "main"); got != sha {
		t.Fatalf("main at %s, want %s", got, sha)
	}
	if parents := gitT(t, repo, "rev-list", "--parents", "-n1", sha); parents != sha+" "+main {
		t.Fatalf("a squash has one parent, the old main: %q", parents)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("the working tree was not updated")
	}
	if who := gitT(t, repo, "log", "-1", "--format=%an <%ae>|%s", sha); who != "Ozan <ozan@example.com>|feat: a and b" {
		t.Fatalf("author and subject %q", who)
	}
	// A second fast-forward to a commit on an older base is refused.
	if err := g.FastForwardWorktree(ctx, repo, feature); err == nil {
		t.Fatal("a non-fast-forward was accepted")
	}
}

func TestSquashMergeRefusals(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo, main, feature := featureRepo(t)

	// A tracked change refuses; an untracked file does not count.
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := g.TrackedChanges(ctx, repo); dirty {
		t.Fatal("an untracked file counted as a change")
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := g.TrackedChanges(ctx, repo); !dirty {
		t.Fatal("an edited tracked file did not count")
	}
	gitT(t, repo, "checkout", "--", "README.md")

	// A conflict: main changed the same file the feature adds.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "a.txt")
	gitT(t, repo, "commit", "-q", "-m", "conflicting a")
	moved := gitT(t, repo, "rev-parse", "HEAD")
	_, res, err := g.SquashCommit(ctx, repo, moved, feature, "m")
	if !errors.Is(err, ErrMergeConflict) || len(res.Conflicts) != 1 || res.Conflicts[0] != "a.txt" {
		t.Fatalf("conflict: %+v %v", res, err)
	}

	// The compare-and-swap move refuses a branch that is not where it was seen.
	gitT(t, repo, "branch", "release", main)
	sha, _, err := g.SquashCommit(ctx, repo, main, feature, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.MoveBranch(ctx, repo, "release", sha, moved); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("moved branch: %v", err)
	}
	if err := g.MoveBranch(ctx, repo, "release", sha, main); err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, repo, "rev-parse", "release"); got != sha {
		t.Fatal("release did not move")
	}
	for _, bad := range []string{"-f", "@{-1}", "HEAD", "a..b"} {
		if err := g.MoveBranch(ctx, repo, bad, sha, main); err == nil {
			t.Errorf("branch %q accepted", bad)
		}
	}
}

func TestPushBranchToBareRemote(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo, main, feature := featureRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "-q", "--bare")
	gitT(t, repo, "remote", "add", "origin", bare)
	if err := g.PushBranch(ctx, repo, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, bare, "rev-parse", "main"); got != main {
		t.Fatalf("remote main %s", got)
	}
	sha, _, err := g.SquashCommit(ctx, repo, main, feature, "squash")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.FastForwardWorktree(ctx, repo, sha); err != nil {
		t.Fatal(err)
	}
	if err := g.PushBranch(ctx, repo, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, bare, "rev-parse", "main"); got != sha {
		t.Fatalf("remote main %s, want %s", got, sha)
	}
	// Someone else pushed meanwhile: a non-fast-forward is refused, nothing forced.
	other := t.TempDir()
	gitT(t, other, "clone", "-q", "-b", "main", bare, ".")
	commitIn(t, other, "theirs.txt")
	gitT(t, other, "push", "-q", "origin", "main")
	theirs := gitT(t, bare, "rev-parse", "main")
	commitIn(t, repo, "ours.txt")
	if err := g.PushBranch(ctx, repo, "origin", "main"); err == nil {
		t.Fatal("a non-fast-forward push was accepted")
	}
	if got := gitT(t, bare, "rev-parse", "main"); got != theirs {
		t.Fatal("the remote branch was overwritten")
	}
	if err := g.PushBranch(ctx, repo, "-o", "main"); err == nil {
		t.Fatal("a remote name that reads as an option was accepted")
	}
}
