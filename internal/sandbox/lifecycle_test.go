package sandbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckInside(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	inside := filepath.Join(root, "proj", "abc")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	ok := []string{inside, filepath.Join(root, "proj"), filepath.Join(root, "not-yet-created"), link}
	for _, p := range ok {
		if err := checkInside(root, p); err != nil {
			t.Errorf("checkInside(%s): %v", p, err)
		}
	}
	bad := []string{root, outside, "/", filepath.Join(root, ".."), filepath.Join(root, "proj", "..", ".."),
		"relative/path", root + "/proj/", filepath.Join(link, "x"), filepath.Dir(root)}
	for _, p := range bad {
		if err := checkInside(root, p); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("checkInside(%q) = %v, want ErrUnsafePath", p, err)
		}
	}
	if err := checkInside(filepath.Join(root, "missing"), inside); !errors.Is(err, ErrUnsafePath) {
		t.Errorf("missing root accepted: %v", err)
	}
	if err := checkInside("/", "/etc"); !errors.Is(err, ErrUnsafePath) {
		t.Errorf("root / accepted: %v", err)
	}
}

func TestLimitWriter(t *testing.T) {
	var buf bytes.Buffer
	cancelled := false
	lw := &limitWriter{w: &buf, n: 5, cancel: func() { cancelled = true }}
	if n, err := lw.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := lw.Write([]byte("defg")); !errors.Is(err, ErrOutputTooLarge) || !lw.exceeded || !cancelled {
		t.Fatalf("over limit: %v exceeded=%v cancelled=%v", err, lw.exceeded, cancelled)
	}
	var errBuf bytes.Buffer
	ew := &limitWriter{w: &errBuf, n: 4}
	if _, err := ew.Write([]byte("123456")); err != nil || errBuf.String() != "1234" {
		t.Fatalf("stderr truncation: %v %q", err, errBuf.String())
	}
}

func TestRemoveRefusesForeignContainers(t *testing.T) {
	if err := (Podman{Bin: "/nonexistent"}).Remove(t.Context(), "postgres"); err == nil {
		t.Fatal("removing a container outside ao-* accepted")
	}
}
