//go:build sandbox

// Integration tests against real rootless Podman: make sandbox-test.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testImage needs only /bin/sh and coreutils.
const defaultTestImage = "docker.io/library/debian:trixie-slim@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392"

func podmanOrSkip(t *testing.T) (Podman, string) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}
	img := os.Getenv("AO_SANDBOX_TEST_IMAGE")
	if img == "" {
		img = defaultTestImage
	}
	p := Podman{}
	ok, err := p.ImageExists(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("test image %s not present; pull it or set AO_SANDBOX_TEST_IMAGE", img)
	}
	return p, img
}

// workspace creates a root and a workspace in it, handed to the container
// user, and removes it again through podman unshare.
func workspace(t *testing.T, p Podman) (root, ws string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "workspaces")
	ws = filepath.Join(root, "abcdef123456")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.Chown(context.Background(), root, ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.RemoveAll(context.Background(), root, ws); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return root, ws
}

func helper(t *testing.T, img, ws string, cmd ...string) (string, []string) {
	t.Helper()
	name, args, err := HelperArgs(HelperSpec{Instance: "0123456789ab", AgentID: "abcdef123456", Image: img, Workspace: ws,
		ObjectsDir: ws, Git: GitIdentity{Name: "Test", Email: "t@example.com"}, Command: cmd}) // ObjectsDir: any existing dir
	if err != nil {
		t.Fatal(err)
	}
	return name, args
}

// gone waits until no container called name exists.
func gone(t *testing.T, p Podman, name string) bool {
	t.Helper()
	for i := 0; i < 50; i++ {
		list, err := p.ListAgentContainers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range list {
			found = found || c.Name == name
		}
		if !found {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestPodmanVersion(t *testing.T) {
	p, _ := podmanOrSkip(t)
	v, err := p.Version(context.Background())
	if err != nil || v == "" {
		t.Fatalf("version %q %v", v, err)
	}
	t.Logf("podman %s", v)
}

func TestHelperRunsIsolatedAsSubordinateUID(t *testing.T) {
	p, img := podmanOrSkip(t)
	_, ws := workspace(t, p)
	var out bytes.Buffer
	script := `id -u; id -g; echo work > /workspace/f; ls /sys/class/net; touch /etc/x 2>/dev/null && echo rootfs-writable || echo rootfs-ro`
	name, args := helper(t, img, ws, "sh", "-c", script)
	if err := p.RunContainer(context.Background(), name, args, &out, 4096); err != nil {
		t.Fatal(err)
	}
	if !gone(t, p, name) {
		t.Fatal("finished helper not removed (--rm)")
	}
	// --log-driver none must not stop attached output.
	if got := strings.Fields(out.String()); strings.Join(got, " ") != "1000 1000 lo rootfs-ro" {
		t.Fatalf("helper output %q, want uid/gid 1000, only lo, read-only rootfs", out.String())
	}
	fi, err := os.Stat(filepath.Join(ws, "f"))
	if err != nil {
		t.Fatal(err)
	}
	uid := fi.Sys().(*syscall.Stat_t).Uid
	if int(uid) == os.Getuid() || uid == 0 {
		t.Fatalf("file owned by host uid %d; want a subordinate uid (ADR 0014)", uid)
	}
	t.Logf("agent files are owned by host uid %d", uid)
	// The host user cannot delete them directly.
	if err := os.Remove(filepath.Join(ws, "f")); err == nil {
		t.Fatal("host user deleted an agent-owned file directly")
	}
}

func TestHelperOutputLimit(t *testing.T) {
	p, img := podmanOrSkip(t)
	_, ws := workspace(t, p)
	start := time.Now()
	name, args := helper(t, img, ws, "sh", "-c", "head -c 10000000 /dev/zero; sleep 30")
	err := p.RunContainer(context.Background(), name, args, &bytes.Buffer{}, 1000)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("want ErrOutputTooLarge, got %v", err)
	}
	if time.Since(start) > 20*time.Second || !gone(t, p, name) {
		t.Fatal("helper container kept running after exceeding the limit")
	}
}

func TestHelperCancelRemovesContainer(t *testing.T) {
	p, img := podmanOrSkip(t)
	_, ws := workspace(t, p)
	name, args := helper(t, img, ws, "sleep", "60")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.RunContainer(ctx, name, args, &bytes.Buffer{}, 100); err == nil {
		t.Fatal("cancelled run reported success")
	}
	if !gone(t, p, name) {
		t.Fatal("cancelled helper container still exists")
	}
}

func TestHelperFailureCarriesStderr(t *testing.T) {
	p, img := podmanOrSkip(t)
	_, ws := workspace(t, p)
	name, args := helper(t, img, ws, "sh", "-c", "echo boom >&2; exit 3")
	err := p.RunContainer(context.Background(), name, args, &bytes.Buffer{}, 100)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %v", err)
	}
}

func TestListAndRemoveOrphans(t *testing.T) {
	p, img := podmanOrSkip(t)
	_, ws := workspace(t, p)
	// Started by a "crashed daemon": only labels and the name are left to find it by.
	orphan, args := helper(t, img, ws, "sleep", "60")
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background(), args, &bytes.Buffer{}, 100) }()

	var found bool
	for i := 0; i < 50 && !found; i++ {
		list, err := p.ListAgentContainers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if c.Name == orphan && c.AgentID == "abcdef123456" && c.Role == "helper" && c.Instance == "0123456789ab" {
				found = true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("running container not listed by label")
	}
	if err := p.Remove(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("container still running after Remove")
	}
	if err := p.Remove(context.Background(), orphan); err != nil {
		t.Fatalf("removing a missing container: %v", err)
	}
}

func TestRemoveAllAndChownRefuseOutsideRoot(t *testing.T) {
	p, _ := podmanOrSkip(t)
	root, _ := workspace(t, p)
	victim := t.TempDir()
	canary := filepath.Join(victim, "keep")
	if err := os.WriteFile(canary, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{victim, filepath.Join(root, "link", "keep"), root} {
		if err := p.RemoveAll(context.Background(), root, path); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("RemoveAll(%s) = %v", path, err)
		}
		if err := p.Chown(context.Background(), root, path); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("Chown(%s) = %v", path, err)
		}
	}
	// Removing the symlink itself removes only the link.
	if err := p.RemoveAll(context.Background(), root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("target of a symlink was touched: %v", err)
	}
}
