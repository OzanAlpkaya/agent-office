package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeCommit commits content to file on the current branch.
func writeCommit(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", file)
	gitOut(t, dir, "commit", "-q", "-m", "edit "+file)
	return gitOut(t, dir, "rev-parse", "HEAD")
}

func TestCommitFileLeavesTheCheckoutAlone(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	gitOut(t, repo, "config", "user.name", "Ozan")
	gitOut(t, repo, "config", "user.email", "o@example.com")
	g := Git{}
	head := gitOut(t, repo, "rev-parse", "HEAD")
	c, err := g.CommitFile(ctx, repo, head, "docs/specs/tags.md", []byte("# Tags\n"), "docs: spec\n\nTrailer: x", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, repo, "show", c+":docs/specs/tags.md"); got != "# Tags" {
		t.Fatalf("file %q", got)
	}
	if got := gitOut(t, repo, "show", c+":README.md"); got != "hi" {
		t.Fatalf("the base tree is kept: %q", got)
	}
	if p := gitOut(t, repo, "rev-parse", c+"^"); p != head {
		t.Fatalf("parent %s", p)
	}
	if a := gitOut(t, repo, "log", "-1", "--format=%an <%ae>", c); a != "Ozan <o@example.com>" {
		t.Fatalf("author %q", a)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" || gitOut(t, repo, "rev-parse", "HEAD") != head {
		t.Fatalf("checkout changed: %q", st)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !os.IsNotExist(err) {
		t.Fatal("the file reached the working tree")
	}
	// A second version on top replaces the file.
	c2, err := g.CommitFile(ctx, repo, c, "docs/specs/tags.md", []byte("# Tags v2\n"), "docs: spec v2", t.TempDir())
	if err != nil || gitOut(t, repo, "show", c2+":docs/specs/tags.md") != "# Tags v2" {
		t.Fatalf("v2: %v", err)
	}
	// Branches: create only if absent, move only from the expected head.
	b := BranchPrefix + "spec-tags-1"
	if err := g.SetBranch(ctx, repo, b, c, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.SetBranch(ctx, repo, b, c2, ""); err == nil {
		t.Fatal("created over an existing branch")
	}
	if err := g.SetBranch(ctx, repo, b, c2, head); err == nil {
		t.Fatal("moved from the wrong head")
	}
	if err := g.SetBranch(ctx, repo, b, c2, c); err != nil {
		t.Fatal(err)
	}
	if err := g.SetBranch(ctx, repo, "main", c2, ""); err == nil {
		t.Fatal("set a branch outside agent-office/")
	}
	for _, bad := range []string{"/abs", "../up", "a/../b", ""} {
		if _, err := g.CommitFile(ctx, repo, head, bad, nil, "m", t.TempDir()); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
}

func TestMergeTree(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	gitOut(t, repo, "config", "user.name", "t")
	gitOut(t, repo, "config", "user.email", "t@example.com")
	writeCommit(t, repo, "handler.go", "line 1\nline 2\nline 3\n")
	gitOut(t, repo, "branch", "a")
	gitOut(t, repo, "branch", "b")
	gitOut(t, repo, "branch", "c")
	gitOut(t, repo, "checkout", "-q", "a")
	a := writeCommit(t, repo, "handler.go", "line 1\nline 2 from a\nline 3\n")
	gitOut(t, repo, "checkout", "-q", "b")
	b := writeCommit(t, repo, "handler.go", "line 1\nline 2 from b\nline 3\n")
	writeCommit(t, repo, "other.go", "x\n")
	gitOut(t, repo, "checkout", "-q", "c")
	c := writeCommit(t, repo, "new.go", "c\n")
	gitOut(t, repo, "checkout", "-q", "main")

	g := Git{}
	res, err := g.MergeTree(ctx, repo, a, b)
	if err != nil || res.Clean || !slices.Equal(res.Conflicts, []string{"handler.go"}) {
		t.Fatalf("two branches editing the same lines: %+v %v", res, err)
	}
	res, err = g.MergeTree(ctx, repo, a, c)
	if err != nil || !res.Clean || len(res.Conflicts) != 0 || len(res.Tree) != 40 {
		t.Fatalf("independent branches: %+v %v", res, err)
	}
	// A clean merge becomes a commit with both parents.
	m, err := g.CommitTree(ctx, repo, res.Tree, []string{a, c}, "agent-office: base")
	if err != nil {
		t.Fatal(err)
	}
	if ps := gitOut(t, repo, "log", "-1", "--format=%P", m); ps != a+" "+c {
		t.Fatalf("parents %q", ps)
	}
	if ok, err := g.IsAncestor(ctx, repo, a, m); err != nil || !ok {
		t.Fatalf("ancestor %v %v", ok, err)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("merge-tree touched the checkout: %q", st)
	}
	if _, err := g.MergeTree(ctx, repo, "--output=x", b); err == nil {
		t.Fatal("an option as a revision")
	}
}
