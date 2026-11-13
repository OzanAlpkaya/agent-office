package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// Reading a commit's files without a working tree (Phase 20, ADR 0066): the
// Tech Lead looks at its team's branches through the daemon, in the main
// repository only (ADR 0014).

// TreeEntry is one entry of a tree listing.
type TreeEntry struct {
	Path string // repository-relative
	Type string // blob | tree | commit (a submodule)
	Mode string
	Size int64 // blobs only; -1 otherwise
}

// maxListing bounds the output of one tree listing.
const maxListing = 8 << 20

// ErrNotFound is returned for a path a commit does not have.
var ErrNotFound = errors.New("git: no such path in the commit")

// CleanRepoPath checks and cleans a repository-relative path from an agent:
// relative, no "..", no control characters, at most 1024 bytes. "" or "."
// is the top of the tree.
func CleanRepoPath(p string) (string, error) {
	if len(p) > 1024 || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("invalid path")
	}
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "" || p == "." || p == "/" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("the path must be relative to the repository's top, e.g. assets/render.png")
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("the path must stay inside the repository")
	}
	return c, nil
}

// ListTree lists dir (repository-relative, "" for the top) at commit, its
// subdirectories too when recursive, at most limit entries; more reports
// whether there were more.
func (g Git) ListTree(ctx context.Context, repo, commit, dir string, recursive bool, limit int) (entries []TreeEntry, more bool, err error) {
	if !shaRe.MatchString(commit) {
		return nil, false, fmt.Errorf("git: invalid commit %q", commit)
	}
	args := []string{"ls-tree", "-z", "-l", "--full-tree"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, commit)
	if dir != "" {
		if t, err := g.objectType(ctx, repo, commit, dir); err != nil {
			return nil, false, err
		} else if t != "tree" {
			return nil, false, fmt.Errorf("%s is a file, not a directory", dir)
		}
		args = append(args, "--", dir+"/")
	}
	// Bounded: a worker could commit a huge tree (security review).
	raw, err := g.raw(ctx, repo, maxListing, args...)
	if err != nil {
		return nil, false, err
	}
	for _, rec := range strings.Split(string(raw), "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			continue
		}
		if len(entries) == limit {
			return entries, true, nil
		}
		size := int64(-1)
		if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
			size = n
		}
		entries = append(entries, TreeEntry{Mode: f[0], Type: f[1], Size: size, Path: p})
	}
	return entries, false, nil
}

// objectType is the type of commit:p (blob, tree, commit), or ErrNotFound.
func (g Git) objectType(ctx context.Context, repo, commit, p string) (string, error) {
	out, code, err := g.runFull(ctx, repo, nil, nil, []int{128}, "cat-file", "-t", commit+":"+p)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", ErrNotFound
	}
	return strings.TrimSpace(out), nil
}

// ReadBlob reads file p at commit when it is at most max bytes; size is its
// size either way (data is nil when it is larger).
func (g Git) ReadBlob(ctx context.Context, repo, commit, p string, max int64) (data []byte, size int64, err error) {
	if !shaRe.MatchString(commit) || p == "" {
		return nil, 0, fmt.Errorf("git: invalid commit or path")
	}
	t, err := g.objectType(ctx, repo, commit, p)
	if err != nil {
		return nil, 0, err
	}
	if t != "blob" {
		return nil, 0, fmt.Errorf("%s is a directory, not a file", p)
	}
	out, _, err := g.runFull(ctx, repo, nil, nil, nil, "cat-file", "-s", commit+":"+p)
	if err != nil {
		return nil, 0, err
	}
	if size, err = strconv.ParseInt(strings.TrimSpace(out), 10, 64); err != nil {
		return nil, 0, fmt.Errorf("git: blob size: %w", err)
	}
	if size > max {
		return nil, size, nil
	}
	data, err = g.raw(ctx, repo, max, "cat-file", "blob", commit+":"+p)
	return data, size, err
}

// raw runs git and returns stdout unmodified (binary files), at most max bytes.
func (g Git) raw(ctx context.Context, dir string, max int64, args ...string) ([]byte, error) {
	if err := g.checkTrusted(dir, args); err != nil {
		return nil, err
	}
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, &Error{Args: args, Err: err}
	}
	data, rerr := io.ReadAll(io.LimitReader(stdout, max+1))
	if rerr != nil || int64(len(data)) > max {
		_ = cmd.Process.Kill() // it may block writing the rest
	}
	werr := cmd.Wait()
	switch {
	case rerr != nil:
		return nil, rerr
	case int64(len(data)) > max:
		return nil, fmt.Errorf("more than %d bytes of output: too large to read at once", max)
	case werr != nil:
		return nil, &Error{Args: args, Stderr: stderr.String(), Err: werr}
	}
	return data, nil
}
