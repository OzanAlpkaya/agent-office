package sandbox

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// containerfile is the worker image definition, embedded so the daemon knows
// exactly which image it needs and can build it without a source checkout.
//
//go:embed Containerfile
var containerfile []byte

// WorkerImage is the local tag of the worker image built from the embedded
// Containerfile: a change to the file changes the tag.
func WorkerImage() string {
	sum := sha256.Sum256(containerfile)
	return "localhost/agent-office-worker:" + hex.EncodeToString(sum[:6])
}

// BuildWorkerImage builds WorkerImage with podman, streaming the build output
// to out. It needs network access for the base image, Debian packages and the
// Go download.
func (p Podman) BuildWorkerImage(ctx context.Context, out io.Writer) error {
	dir, err := os.MkdirTemp("", "agent-office-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), containerfile, 0o644); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.bin(), "build", "--pull=missing", "--tag", WorkerImage(),
		"--file", filepath.Join(dir, "Containerfile"), dir)
	cmd.Env = podmanEnv()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandbox: building %s: %w", WorkerImage(), err)
	}
	return nil
}
