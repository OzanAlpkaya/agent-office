package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxGit runs git in dir the way the sandbox does: the test stands in for
// the container, so it may touch the agent repository directly.
func sandboxGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ozan", "GIT_AUTHOR_EMAIL=ozan@example.com",
		"GIT_COMMITTER_NAME=Ozan", "GIT_COMMITTER_EMAIL=ozan@example.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	return out
}

// agentClone creates a main repo (with an extra branch and a tag the agent
// must not see) and a shared clone under an untrusted root.
func agentClone(t *testing.T) (g Git, repo, clone, branch, base string) {
	t.Helper()
	ctx := context.Background()
	repo = newRepo(t)
	sandboxGit(t, repo, "branch", "other")
	sandboxGit(t, repo, "tag", "v1")
	root := t.TempDir()
	g = Git{UntrustedRoots: []string{root}}
	base, err := g.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	branch = BranchName("Sayfalama ekle", "abcdef123456")
	clone = filepath.Join(root, "proj", "abcdef123456")
	if err := g.CreateSharedClone(ctx, repo, clone, branch, base); err != nil {
		t.Fatal(err)
	}
	return g, repo, clone, branch, base
}

func TestCreateSharedCloneLayout(t *testing.T) {
	g, repo, clone, branch, base := agentClone(t)

	if refs := strings.TrimSpace(string(sandboxGit(t, clone, "for-each-ref", "--format=%(refname)"))); refs != "refs/heads/"+branch {
		t.Fatalf("clone refs = %q, want only the agent branch", refs)
	}
	if remotes := strings.TrimSpace(string(sandboxGit(t, clone, "remote"))); remotes != "" {
		t.Fatalf("clone has remotes: %q", remotes)
	}
	if head := strings.TrimSpace(string(sandboxGit(t, clone, "rev-parse", "HEAD"))); head != base {
		t.Fatalf("HEAD %s, want %s", head, base)
	}
	if _, err := os.Stat(filepath.Join(clone, "README.md")); err != nil {
		t.Fatalf("working tree not checked out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "hooks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hook templates copied into the clone: %v", err)
	}
	objects, err := g.ObjectsDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	alt, _ := os.ReadFile(filepath.Join(clone, ".git", "objects", "info", "alternates"))
	if strings.TrimSpace(string(alt)) != objects || !filepath.IsAbs(objects) {
		t.Fatalf("alternates %q, want %q", alt, objects)
	}
	// Objects are borrowed, not copied.
	if c := string(sandboxGit(t, clone, "count-objects", "-v")); !strings.Contains(c, "count: 0\n") || !strings.Contains(c, "in-pack: 0\n") {
		t.Fatalf("clone has its own objects:\n%s", c)
	}
	if dirty := sandboxGit(t, clone, StatusArgs()...); len(dirty) != 0 {
		t.Fatalf("fresh clone not clean: %q", dirty)
	}
}

func TestCreateSharedCloneRefusesAndCleansUp(t *testing.T) {
	ctx := context.Background()
	g, repo, clone, branch, base := agentClone(t)
	if err := g.CreateSharedClone(ctx, repo, clone, branch+"-2", base); err == nil {
		t.Fatal("existing path accepted")
	}
	dir := filepath.Join(t.TempDir(), "x")
	for name, call := range map[string]func() error{
		"bad base": func() error {
			return g.CreateSharedClone(ctx, repo, dir, branch, "0123456789abcdef0123456789abcdef01234567")
		},
		"non-agent ref":  func() error { return g.CreateSharedClone(ctx, repo, dir, "main2", base) },
		"option branch":  func() error { return g.CreateSharedClone(ctx, repo, dir, "-x", base) },
		"relative path":  func() error { return g.CreateSharedClone(ctx, repo, "rel/x", branch, base) },
		"option as base": func() error { return g.CreateSharedClone(ctx, repo, dir, branch, "--all") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: left %s behind", name, dir)
		}
	}
}

func TestCreateSharedCloneCleansUpAfterPartialSetup(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A git that fails at a late setup step, after init created the clone.
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n[ \"$1\" = symbolic-ref ] && exit 1\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g := Git{Bin: wrapper}
	base, _ := g.HeadCommit(ctx, repo)
	dir := filepath.Join(t.TempDir(), "clone")
	if err := g.CreateSharedClone(ctx, repo, dir, BranchPrefix+"x-abcdef", base); err == nil {
		t.Fatal("failing setup reported success")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial clone left behind: %v", err)
	}
}

func TestObjectsDirRefusesAlternates(t *testing.T) {
	_, _, clone, _, _ := agentClone(t)
	// The clone itself uses alternates, so it is a handy example (no guard: plain Git{}).
	if _, err := (Git{}).ObjectsDir(context.Background(), clone); !errors.Is(err, ErrRepoHasAlternates) {
		t.Fatalf("want ErrRepoHasAlternates, got %v", err)
	}
}

func TestUntrustedRootsAreRefused(t *testing.T) {
	ctx := context.Background()
	g, repo, clone, branch, _ := agentClone(t)
	link := filepath.Join(t.TempDir(), "innocent")
	if err := os.Symlink(clone, link); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"status in clone":    func() error { _, err := g.IsDirty(ctx, clone); return err },
		"subdir of clone":    func() error { _, err := g.TopLevel(ctx, filepath.Join(clone, ".git")); return err },
		"symlink into clone": func() error { _, err := g.HeadCommit(ctx, link); return err },
		"path arg in clone": func() error {
			_, err := g.ImportBundle(ctx, repo, filepath.Join(clone, "x.bundle"), branch)
			return err
		},
		"option value in clone": func() error {
			_, err := g.run(ctx, repo, "--git-dir="+filepath.Join(clone, ".git"), "status")
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUntrusted) {
			t.Errorf("%s: want ErrUntrusted, got %v", name, err)
		}
	}
	if _, err := g.HeadCommit(ctx, repo); err != nil {
		t.Fatalf("trusted repo refused: %v", err)
	}
}

// writeBundle runs BundleArgs inside the "sandbox" and stores stdout in a
// daemon-owned file, as the sandbox helper will.
func writeBundle(t *testing.T, clone, base, branch string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "handoff.bundle")
	if err := os.WriteFile(p, sandboxGit(t, clone, BundleArgs(base, branch)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBundleHandoffRoundTrip(t *testing.T) {
	ctx := context.Background()
	g, repo, clone, branch, base := agentClone(t)

	// The agent commits, and also plants config that would run for anyone
	// who runs git in its repository.
	marker := filepath.Join(t.TempDir(), "PWNED")
	sandboxGit(t, clone, "config", "core.fsmonitor", "touch "+marker)
	if err := os.WriteFile(filepath.Join(clone, "page.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandboxGit(t, clone, "add", "page.go")
	sandboxGit(t, clone, "commit", "-q", "-m", "feat: paginate",
		"-m", "Co-Authored-By: worker-1 (agent-office) <noreply@agent-office.invalid>")
	if n := strings.TrimSpace(string(sandboxGit(t, clone, CountArgs(base, branch)...))); n != "1" {
		t.Fatalf("count = %s", n)
	}
	head := strings.TrimSpace(string(sandboxGit(t, clone, "rev-parse", "HEAD")))
	// Control: the trap is live. Plain git status in the clone runs it.
	sandboxGit(t, clone, StatusArgs()...)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control failed, core.fsmonitor did not run: %v", err)
	}
	bundle := writeBundle(t, clone, base, branch) // inside the "sandbox" too
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	got, err := g.ImportBundle(ctx, repo, bundle, branch)
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Fatalf("imported %s, want %s", got, head)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("importing ran agent-written git config on the host")
	}
	if n, err := g.CommitsAhead(ctx, repo, base, branch); err != nil || n != 1 {
		t.Fatalf("main repo branch: %d %v", n, err)
	}

	commits, err := g.BranchCommits(ctx, repo, base, branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Hash != head || commits[0].AuthorName != "Ozan" ||
		commits[0].AuthorEmail != "ozan@example.com" ||
		len(commits[0].CoAuthors) != 1 || commits[0].CoAuthors[0] != "worker-1 (agent-office) <noreply@agent-office.invalid>" {
		t.Fatalf("commits %+v", commits)
	}

	// The agent amends its own branch: a forced update of that branch is fine.
	sandboxGit(t, clone, "commit", "-q", "--amend", "-m", "feat: paginate (v2)")
	if _, err := g.ImportBundle(ctx, repo, writeBundle(t, clone, base, branch), branch); err != nil {
		t.Fatalf("amended branch: %v", err)
	}
	if cs, _ := g.BranchCommits(ctx, repo, base, branch); len(cs) != 1 || len(cs[0].CoAuthors) != 0 {
		t.Fatalf("after amend: %+v", cs) // the amend dropped the trailer: the import check will flag it
	}
}

func TestImportBundleRejects(t *testing.T) {
	ctx := context.Background()
	g, repo, clone, branch, base := agentClone(t)
	commitIn(t, clone, "a.txt")
	good := writeBundle(t, clone, base, branch)

	// A bundle that also carries a second ref (e.g. an attempt to move main).
	sandboxGit(t, clone, "branch", "main")
	two := filepath.Join(t.TempDir(), "two.bundle")
	if err := os.WriteFile(two, sandboxGit(t, clone, "bundle", "create", "-", base+"..refs/heads/"+branch, base+"..refs/heads/main"), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(t.TempDir(), "junk.bundle")
	if err := os.WriteFile(junk, []byte("# v2 git bundle\nnot really\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The user is on another branch, so git's own "refusing to fetch into the
	// current branch" does not protect main; only ImportBundle's checks do.
	sandboxGit(t, repo, "switch", "-q", "other")
	// A bundle whose only ref is main: an attempt to move the user's branch.
	sandboxGit(t, clone, "switch", "-q", "main")
	commitIn(t, clone, "evil.txt")
	onlyMain := filepath.Join(t.TempDir(), "main.bundle")
	if err := os.WriteFile(onlyMain, sandboxGit(t, clone, "bundle", "create", "-", base+"..refs/heads/main"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ bundle, branch string }{
		"main-only bundle": {onlyMain, "main"},
		"two refs":         {two, branch},
		"other agent ref":  {good, BranchPrefix + "someone-else"},
		"non-agent branch": {good, "main"},
		"corrupt bundle":   {junk, branch},
		"relative path":    {"handoff.bundle", branch},
		"missing bundle":   {filepath.Join(t.TempDir(), "nope"), branch},
	}
	for name, c := range cases {
		if _, err := g.ImportBundle(ctx, repo, c.bundle, c.branch); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ok, _ := g.BranchExists(ctx, repo, branch); ok {
		t.Fatal("a rejected import created the branch")
	}
	if main := strings.TrimSpace(string(sandboxGit(t, repo, "rev-parse", "main"))); main != base {
		t.Fatal("main moved")
	}
	if _, err := g.ImportBundle(ctx, repo, good, branch); err != nil {
		t.Fatalf("good bundle after rejects: %v", err)
	}
}

func TestProtectBaseSurvivesGC(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	sandboxGit(t, repo, "switch", "-q", "-c", "feature")
	commitIn(t, repo, "f.txt")
	base := strings.TrimSpace(string(sandboxGit(t, repo, "rev-parse", "HEAD")))
	root := t.TempDir()
	g := Git{UntrustedRoots: []string{root}}
	clone := filepath.Join(root, "c")
	branch := BranchPrefix + "x-abcdef"
	if err := g.CreateSharedClone(ctx, repo, clone, branch, base); err != nil {
		t.Fatal(err)
	}
	if err := g.ProtectBase(ctx, repo, "abcdef123456", base); err != nil {
		t.Fatal(err)
	}
	// The user deletes the branch the agent started from and collects garbage.
	sandboxGit(t, repo, "switch", "-q", "main")
	sandboxGit(t, repo, "branch", "-D", "feature")
	sandboxGit(t, repo, "reflog", "expire", "--expire=now", "--all")
	sandboxGit(t, repo, "gc", "--quiet", "--prune=now")
	sandboxGit(t, clone, "fsck", "--no-dangling") // fails if borrowed objects are gone

	if err := g.UnprotectBase(ctx, repo, "abcdef123456"); err != nil {
		t.Fatal(err)
	}
	if err := g.UnprotectBase(ctx, repo, "abcdef123456"); err != nil {
		t.Fatalf("second unprotect: %v", err)
	}
	if err := g.ProtectBase(ctx, repo, "../evil", base); err == nil {
		t.Fatal("bad agent id accepted")
	}
}

func TestParseStatus(t *testing.T) {
	ok := map[string]int{
		"":                              0,
		" M a.go\x00":                   1,
		"?? new file.txt\x00 D old\x00": 2,
		"R  new.go\x00old.go\x00":       1,
		"A  x\x00R  b\x00a\x00?? c\x00": 3,
	}
	for in, want := range ok {
		if n, err := ParseStatus([]byte(in)); err != nil || n != want {
			t.Errorf("ParseStatus(%q) = %d, %v; want %d", in, n, err, want)
		}
	}
	bad := []string{" M a.go", "XY a\x00", "M a\x00", "R  only-new\x00", "\x00", strings.Repeat("?? x\x00", MaxStatusBytes/5+1)}
	for _, in := range bad {
		if _, err := ParseStatus([]byte(in)); err == nil {
			t.Errorf("ParseStatus(%.20q) accepted", in)
		}
	}
}

func TestUserIdentity(t *testing.T) {
	repo := newRepo(t)
	sandboxGit(t, repo, "config", "user.name", "Ozan Test")
	sandboxGit(t, repo, "config", "user.email", "ozan@example.com")
	n, e, err := (Git{}).UserIdentity(context.Background(), repo)
	if err != nil || n != "Ozan Test" || e != "ozan@example.com" {
		t.Fatalf("identity %q %q %v", n, e, err)
	}
}

func TestImportBundleFscksObjects(t *testing.T) {
	ctx := context.Background()
	g, repo, clone, branch, base := agentClone(t)
	// A commit with a malformed author line, written directly by the "agent".
	tree := strings.TrimSpace(string(sandboxGit(t, clone, "rev-parse", "HEAD^{tree}")))
	raw := "tree " + tree + "\nparent " + base + "\nauthor Evil <evil@example.com 0 +0000\n" +
		"committer Evil <evil@example.com> 0 +0000\n\nmalformed\n"
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	cmd.Dir, cmd.Stdin = clone, strings.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sandboxGit(t, clone, "update-ref", "refs/heads/"+branch, strings.TrimSpace(string(out)))
	if _, err := g.ImportBundle(ctx, repo, writeBundle(t, clone, base, branch), branch); err == nil {
		t.Fatal("malformed commit imported")
	}
	if ok, _ := g.BranchExists(ctx, repo, branch); ok {
		t.Fatal("branch created from a malformed bundle")
	}
}
