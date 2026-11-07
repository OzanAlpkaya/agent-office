// Package git wraps the git CLI for the operations agent-office needs:
// repository discovery, worktrees and branches (ADR 0009).
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Git runs git commands. The zero value uses "git" from PATH and a 30 s timeout.
type Git struct {
	Bin     string
	Timeout time.Duration
}

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
	bin, timeout := g.Bin, g.Timeout
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
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
