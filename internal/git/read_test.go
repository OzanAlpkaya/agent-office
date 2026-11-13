package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadTreeAndBlob(t *testing.T) {
	repo := newRepo(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0, 1, 2, 0xff}, 64)...)
	if err := os.MkdirAll(filepath.Join(repo, "assets", "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "assets", "renders", "ball.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "assets", "notes.txt"), []byte("bounce\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "assets"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	g, ctx := Git{}, context.Background()
	head, err := g.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	top, more, err := g.ListTree(ctx, repo, head, "", false, 100)
	if err != nil || more || len(top) != 2 || top[0].Path != "README.md" || top[1].Path != "assets" || top[1].Type != "tree" {
		t.Fatalf("top %+v %v %v", top, more, err)
	}
	all, _, err := g.ListTree(ctx, repo, head, "assets", true, 100)
	if err != nil || len(all) != 2 || all[1].Path != "assets/renders/ball.png" || all[1].Size != int64(len(png)) {
		t.Fatalf("assets %+v %v", all, err)
	}
	if few, more, _ := g.ListTree(ctx, repo, head, "", true, 1); len(few) != 1 || !more {
		t.Fatalf("limit: %+v %v", few, more)
	}
	if _, _, err := g.ListTree(ctx, repo, head, "assets/notes.txt", false, 10); err == nil {
		t.Fatal("listed a file")
	}
	data, size, err := g.ReadBlob(ctx, repo, head, "assets/renders/ball.png", 1<<20)
	if err != nil || !bytes.Equal(data, png) || size != int64(len(png)) {
		t.Fatalf("blob %d bytes, size %d, %v", len(data), size, err)
	}
	if data, size, err := g.ReadBlob(ctx, repo, head, "assets/renders/ball.png", 10); err != nil || data != nil || size != int64(len(png)) {
		t.Fatalf("too large: %v %d %v", data, size, err)
	}
	if _, _, err := g.ReadBlob(ctx, repo, head, "nope.png", 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := g.ReadBlob(ctx, repo, head, "assets", 100); err == nil {
		t.Fatal("read a directory")
	}
	for in, want := range map[string]string{"": "", ".": "", "./a/b": "a/b", "a/../b": "b", "a//b/": "a/b"} {
		if got, err := CleanRepoPath(in); err != nil || got != want {
			t.Errorf("CleanRepoPath(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"/etc/passwd", "..", "../x", "a/../../x", "a\nb"} {
		if _, err := CleanRepoPath(bad); err == nil {
			t.Errorf("CleanRepoPath(%q) accepted", bad)
		}
	}
}
