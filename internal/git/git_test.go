package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Add pagination to GET /bookmarks": "add-pagination-to-get-bookmarks",
		"Şifre sıfırlama özelliği ekle":    "sifre-sifirlama-ozelligi-ekle",
		"İstanbul'da ÇALIŞAN Ğ testi":      "istanbul-da-calisan-g-testi",
		"Gözden geçir: ünlü ölçüm":         "gozden-gecir-unlu-olcum",
		"  --Leading/trailing!!  ":         "leading-trailing",
		"emoji 🚀 and 中文 dropped":           "emoji-and-dropped",
		"!!!":                              "",
		"Straße café":                      "strasse-cafe",
		"v2.0 release_notes":               "v2-0-release-notes",
	}
	for in, want := range cases {
		if got := Slug(in, MaxSlugLen); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugLengthAndCharset(t *testing.T) {
	long := strings.Repeat("çok uzun bir görev başlığı ", 10)
	got := Slug(long, MaxSlugLen)
	if len(got) > MaxSlugLen || strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
		t.Fatalf("bad length/edges: %q (%d)", got, len(got))
	}
	for _, r := range got {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("non-ASCII-slug rune %q in %q", r, got)
		}
	}
	if strings.Contains(got, "--") {
		t.Fatalf("double hyphen in %q", got)
	}
}

func TestBranchName(t *testing.T) {
	if got := BranchName("Sayfalama ekle", "a1b2c3d4e5f6"); got != "agent-office/sayfalama-ekle-a1b2c3" {
		t.Errorf("task branch = %q", got)
	}
	if got := BranchName("", "a1b2c3d4e5f6"); got != "agent-office/a1b2c3d4e5f6" {
		t.Errorf("no task = %q", got)
	}
	if got := BranchName("🚀🚀", "a1b2c3d4e5f6"); got != "agent-office/a1b2c3d4e5f6" {
		t.Errorf("unsluggable title = %q", got)
	}
}

// newRepo creates a repository with one commit and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

func commitIn(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-q", "-m", "add " + file}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo := newRepo(t)
	top, err := g.TopLevel(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.HeadCommit(ctx, top)
	if err != nil || len(base) != 40 {
		t.Fatalf("HEAD %q %v", base, err)
	}

	wt := filepath.Join(t.TempDir(), "wt", "agent1")
	branch := BranchName("Şifre ekle", "abcdef123456")
	if err := g.AddWorktree(ctx, top, wt, branch, base); err != nil {
		t.Fatal(err)
	}
	list, err := g.ListWorktrees(ctx, top)
	if err != nil || len(list) != 2 || list[1].Branch != "refs/heads/"+branch {
		t.Fatalf("worktrees %+v %v", list, err)
	}

	if dirty, _ := g.IsDirty(ctx, wt); dirty {
		t.Fatal("fresh worktree reported dirty")
	}
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := g.IsDirty(ctx, wt); !dirty {
		t.Fatal("untracked file not reported as dirty")
	}
	if err := g.RemoveWorktree(ctx, top, wt, false); err == nil {
		t.Fatal("dirty worktree removed without force")
	}

	commitIn(t, wt, "new.txt")
	if n, err := g.CommitsAhead(ctx, top, base, branch); err != nil || n != 1 {
		t.Fatalf("CommitsAhead = %d, %v", n, err)
	}
	if err := g.RemoveWorktree(ctx, top, wt, false); err != nil {
		t.Fatalf("clean worktree: %v", err)
	}
	if ok, err := g.BranchExists(ctx, top, branch); !ok || err != nil {
		t.Fatalf("branch should survive worktree removal: %v %v", ok, err)
	}
	if err := g.DeleteBranch(ctx, top, branch); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.BranchExists(ctx, top, branch); ok || err != nil {
		t.Fatalf("branch still exists: %v %v", ok, err)
	}
}

func TestGuards(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo := newRepo(t)
	if err := g.DeleteBranch(ctx, repo, "main"); err == nil {
		t.Fatal("deleted a branch outside agent-office/")
	}
	for _, bad := range []string{"-rf", "agent-office/a..b", "agent-office/a b", ""} {
		if g.ValidBranch(ctx, bad) {
			t.Errorf("ValidBranch(%q) = true", bad)
		}
	}
	base, _ := g.HeadCommit(ctx, repo)
	if err := g.AddWorktree(ctx, repo, filepath.Join(t.TempDir(), "x"), "-x", base); err == nil {
		t.Fatal("option-like branch accepted")
	}
}

func TestPruneForgetsDeletedFolder(t *testing.T) {
	ctx := context.Background()
	g := Git{}
	repo := newRepo(t)
	base, _ := g.HeadCommit(ctx, repo)
	wt := filepath.Join(t.TempDir(), "gone")
	if err := g.AddWorktree(ctx, repo, wt, BranchPrefix+"gone", base); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := g.Prune(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if list, _ := g.ListWorktrees(ctx, repo); len(list) != 1 {
		t.Fatalf("after prune: %+v", list)
	}
}

func TestEnvScrubsGitDir(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("GIT_DIR", "/nonexistent") // after newRepo, which shells out to git itself
	if _, err := (Git{}).HeadCommit(context.Background(), repo); err != nil {
		t.Fatalf("GIT_DIR leaked into git calls: %v", err)
	}
}

func TestCommitMessages(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	g := Git{}
	gitRun := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	base := gitRun("", "rev-parse", "HEAD")
	gitRun("", "switch", "-q", "-c", "work")
	gitRun("", "commit", "-q", "--allow-empty", "-m", "feat: first\n\nFixes #12\nsecond paragraph")
	first := gitRun("", "rev-parse", "HEAD")
	gitRun("", "commit", "-q", "--allow-empty", "-m", "chore: second")
	second := gitRun("", "rev-parse", "HEAD")

	msgs, err := g.CommitMessages(ctx, repo, base, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[first] != "feat: first\n\nFixes #12\nsecond paragraph\n" || msgs[second] != "chore: second\n" {
		t.Fatalf("messages %q", msgs)
	}

	// git prints a message only up to a NUL, so a NUL cannot forge a record;
	// it could hide a closing keyword, but the bundle import's fsck refuses
	// such commits (nulInCommit), so none reaches a branch the daemon pushes.
	tree := gitRun("", "rev-parse", "HEAD^{tree}")
	raw := "tree " + tree + "\nparent " + second + "\nauthor t <t@example.com> 1 +0000\ncommitter t <t@example.com> 1 +0000\n\n" +
		"innocent\x00" + strings.Repeat("0", 40) + "\nfixes #1\n"
	nul := gitRun(raw, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
	gitRun("", "update-ref", "refs/heads/work", nul)
	msgs, err = g.CommitMessages(ctx, repo, base, "work")
	if err != nil || len(msgs) != 3 || msgs[nul] != "innocent" {
		t.Fatalf("messages %q %v", msgs, err)
	}
	bundle := filepath.Join(t.TempDir(), "b.bundle")
	gitRun("", "bundle", "create", "-q", bundle, second+"..work")
	dst := newRepo(t)
	fetch := exec.Command("git", "-c", "transfer.fsckObjects=true", "fetch", "-q", repo, second)
	fetch.Dir = dst
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Fatalf("fetch base: %v %s", err, out)
	}
	imp := exec.Command("git", "-c", "transfer.fsckObjects=true", "fetch", "-q", "--no-tags", bundle, "+refs/heads/work:refs/in/work")
	imp.Dir = dst
	if out, err := imp.CombinedOutput(); err == nil || !strings.Contains(string(out), "nulInCommit") {
		t.Fatalf("a commit with a NUL was imported: %v %s", err, out)
	}
}

func TestIsCommitHash(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("a", 40): true, strings.Repeat("0", 64): true, strings.Repeat("A", 40): false,
		strings.Repeat("a", 39): false, "": false, strings.Repeat("g", 40): false,
	} {
		if isCommitHash(s) != want {
			t.Errorf("isCommitHash(%q) = %v", s, !want)
		}
	}
}
