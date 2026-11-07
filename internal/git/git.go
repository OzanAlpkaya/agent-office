// Package git wraps the git CLI for the operations agent-office needs:
// repository discovery, worktrees and branches (ADR 0009), and the shared
// clones and bundle hand-off of sandboxed agents (ADR 0014).
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Git runs git commands. The zero value uses "git" from PATH and a 30 s timeout.
type Git struct {
	Bin     string
	Timeout time.Duration
	// UntrustedRoots are directories holding repositories that sandboxed
	// agents control (ADR 0014). Git config written there (core.fsmonitor,
	// hooks, ...) would run on the host, so every command whose working
	// directory or absolute path arguments lie under one of them is refused,
	// except while CreateSharedClone sets a new repository up.
	UntrustedRoots []string
}

// ErrUntrusted is returned for a git command that would run on, or read from,
// a path under Git.UntrustedRoots.
var ErrUntrusted = errors.New("git: refusing to run git on an agent-controlled path")

// Error is a failed git command.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// scrubbedKeys are variables that would redirect git to another repository or
// index if they leaked from the daemon's environment.
var scrubbedKeys = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_CEILING_DIRECTORIES": true, "GIT_PREFIX": true,
}

func env() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if scrubbedKeys[k] || k == "LC_ALL" || k == "GIT_TERMINAL_PROMPT" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	if err := g.checkTrusted(dir, args); err != nil {
		return "", err
	}
	return g.exec(ctx, dir, args...)
}

// exec runs git without the untrusted-path check. Only run and the setup
// steps of CreateSharedClone call it.
func (g Git) exec(ctx context.Context, dir string, args ...string) (string, error) {
	return g.execWith(ctx, dir, 0, false, args...)
}

// execWith runs git with an optional timeout override; detach runs it in a new
// session without a controlling terminal, so ssh cannot prompt on one.
func (g Git) execWith(ctx context.Context, dir string, timeout time.Duration, detach bool, args ...string) (string, error) {
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = g.Timeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env()
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// checkTrusted refuses dir and absolute path arguments (also in --opt=value
// form) that resolve, symlinks included, to a path under an untrusted root.
func (g Git) checkTrusted(dir string, args []string) error {
	if len(g.UntrustedRoots) == 0 {
		return nil
	}
	paths := []string{}
	if dir != "" {
		paths = append(paths, dir)
	}
	for _, a := range args {
		if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
			a = v
		}
		if filepath.IsAbs(a) {
			paths = append(paths, a)
		}
	}
	for _, p := range paths {
		rp := resolve(p)
		for _, root := range g.UntrustedRoots {
			if within(rp, resolve(root)) {
				return fmt.Errorf("%w: %s", ErrUntrusted, p)
			}
		}
	}
	return nil
}

// resolve makes p absolute and follows symlinks as far as the path exists.
func resolve(p string) string {
	p, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	for q, rest := p, ""; ; {
		if r, err := filepath.EvalSymlinks(q); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(q)
		if parent == q {
			return p
		}
		rest = filepath.Join(filepath.Base(q), rest)
		q = parent
	}
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Version returns the output of `git --version`.
func (g Git) Version(ctx context.Context) (string, error) {
	return g.run(ctx, "", "--version")
}

// TopLevel returns the top-level directory of the repository containing dir.
func (g Git) TopLevel(ctx context.Context, dir string) (string, error) {
	return g.run(ctx, dir, "rev-parse", "--show-toplevel")
}

// HeadCommit returns the full hash of HEAD in repo.
func (g Git) HeadCommit(ctx context.Context, repo string) (string, error) {
	return g.run(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
}

// ValidBranch reports whether name is a valid new branch name.
func (g Git) ValidBranch(ctx context.Context, name string) bool {
	_, err := g.run(ctx, "", "check-ref-format", "--branch", name)
	return err == nil && !strings.HasPrefix(name, "-")
}

// AddWorktree creates a worktree at path on a new branch starting at base.
func (g Git) AddWorktree(ctx context.Context, repo, path, branch, base string) error {
	if !g.ValidBranch(ctx, branch) {
		return fmt.Errorf("git: invalid branch name %q", branch)
	}
	_, err := g.run(ctx, repo, "worktree", "add", "-b", branch, "--", path, base)
	return err
}

// RemoveWorktree removes the worktree at path. Without force, git refuses if
// the worktree has uncommitted changes or untracked files.
func (g Git) RemoveWorktree(ctx context.Context, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := g.run(ctx, repo, append(args, "--", path)...)
	return err
}

// Prune removes administrative data of worktrees whose folders are gone.
func (g Git) Prune(ctx context.Context, repo string) error {
	_, err := g.run(ctx, repo, "worktree", "prune")
	return err
}

// IsDirty reports whether the worktree at path has uncommitted changes or
// untracked (non-ignored) files.
func (g Git) IsDirty(ctx context.Context, path string) (bool, error) {
	out, err := g.run(ctx, path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// CommitsAhead counts commits on branch that are not reachable from base.
func (g Git) CommitsAhead(ctx context.Context, repo, base, branch string) (int, error) {
	out, err := g.run(ctx, repo, "rev-list", "--count", base+".."+branch, "--")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// BranchExists reports whether a local branch exists.
func (g Git) BranchExists(ctx context.Context, repo, branch string) (bool, error) {
	_, err := g.run(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	var gerr *Error
	if errors.As(err, &gerr) {
		var exit *exec.ExitError
		if errors.As(gerr.Err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
	}
	return err == nil, err
}

// DeleteBranch deletes a local branch. Callers must have checked that it has
// no commits worth keeping (see CommitsAhead): this uses -D.
func (g Git) DeleteBranch(ctx context.Context, repo, branch string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("git: refusing to delete branch %q outside %s", branch, BranchPrefix)
	}
	_, err := g.run(ctx, repo, "branch", "-D", branch)
	return err
}

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path   string
	Head   string
	Branch string // full ref, e.g. refs/heads/main; empty when detached
}

// ListWorktrees lists the repository's worktrees (the main one first).
func (g Git) ListWorktrees(ctx context.Context, repo string) ([]Worktree, error) {
	out, err := g.run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	var cur *Worktree
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			list = append(list, Worktree{Path: val})
			cur = &list[len(list)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = val
			}
		}
	}
	return list, nil
}

// remoteName is what agent-office accepts as a git remote name.
var remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// RemoteURL returns the push URL of remote in repo.
func (g Git) RemoteURL(ctx context.Context, repo, remote string) (string, error) {
	if !remoteName.MatchString(remote) {
		return "", fmt.Errorf("git: invalid remote name %q", remote)
	}
	return g.run(ctx, repo, "remote", "get-url", "--push", remote)
}

// PushTimeout bounds a push.
const PushTimeout = 2 * time.Minute

// Push pushes one agent branch from repo to remote (ADR 0020): never forced,
// no tags, and without a controlling terminal, so an SSH passphrase prompt
// fails instead of hanging. A non-fast-forward update is refused by git.
func (g Git) Push(ctx context.Context, repo, remote, branch string) error {
	if !remoteName.MatchString(remote) {
		return fmt.Errorf("git: invalid remote name %q", remote)
	}
	if !strings.HasPrefix(branch, BranchPrefix) || !g.ValidBranch(ctx, branch) {
		return fmt.Errorf("git: refusing to push branch %q (only %s branches are pushed)", branch, BranchPrefix)
	}
	if err := g.checkTrusted(repo, nil); err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	_, err := g.execWith(ctx, repo, PushTimeout, true, "push", "--no-follow-tags", "--quiet", remote, ref+":"+ref)
	return err
}

// RevParse resolves rev to a commit in repo.
func (g Git) RevParse(ctx context.Context, repo, rev string) (string, error) {
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("git: invalid revision %q", rev)
	}
	return g.run(ctx, repo, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}
