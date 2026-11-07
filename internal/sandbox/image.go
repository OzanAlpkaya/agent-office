package sandbox

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
)

// containerfile is the base worker image definition, embedded so the daemon
// knows exactly which image it needs and can build it without a source
// checkout. It is the default image and the base of every pack image
// (ADR 0023).
//
//go:embed Containerfile
var containerfile []byte

// BaseImage is the local tag of the base worker image built from the
// embedded Containerfile: a change to the file changes the tag.
func BaseImage() string {
	sum := sha256.Sum256(containerfile)
	return "localhost/agent-office-base:" + hex.EncodeToString(sum[:6])
}

var packNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// PackImage is the local tag of a stack pack's image (ADR 0023). It hashes
// the base Containerfile, the pack name and every build file, so a change to
// any of them changes the tag.
func PackImage(pack string, files map[string][]byte) string {
	h := sha256.New()
	put := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	put(containerfile)
	put([]byte(pack))
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		put([]byte(n))
		put(files[n])
	}
	return "localhost/agent-office-pack-" + pack + ":" + hex.EncodeToString(h.Sum(nil)[:6])
}

// BuildBaseImage builds BaseImage with podman, streaming the build output to
// out. It needs network access for the base image and Debian packages.
func (p Podman) BuildBaseImage(ctx context.Context, out io.Writer) error {
	return p.build(ctx, BaseImage(), map[string][]byte{"Containerfile": containerfile}, "Containerfile", nil, out)
}

// BuildPackImage builds a pack's image FROM BaseImage, which must exist.
// files is the pack's build context (pack.Pack.BuildFiles), containerfile its
// path in there.
func (p Podman) BuildPackImage(ctx context.Context, pack string, files map[string][]byte, containerfile string, out io.Writer) error {
	if !packNameRe.MatchString(pack) || files[containerfile] == nil {
		return fmt.Errorf("sandbox: invalid pack %q or missing Containerfile %q", pack, containerfile)
	}
	if ok, err := p.ImageExists(ctx, BaseImage()); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("sandbox: base image %s missing; build it first", BaseImage())
	}
	return p.build(ctx, PackImage(pack, files), files, containerfile, []string{"BASE_IMAGE=" + BaseImage()}, out)
}

func (p Podman) build(ctx context.Context, tag string, files map[string][]byte, file string, buildArgs []string, out io.Writer) error {
	dir, err := os.MkdirTemp("", "agent-office-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, b := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("sandbox: build file %q is outside the build context", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
	}
	args := []string{"build", "--pull=missing", "--tag", tag, "--file", filepath.Join(dir, filepath.FromSlash(file))}
	for _, a := range buildArgs {
		args = append(args, "--build-arg", a)
	}
	cmd := exec.CommandContext(ctx, p.bin(), append(args, dir)...)
	cmd.Env = podmanEnv()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandbox: building %s: %w", tag, err)
	}
	return nil
}
