package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Plumbing for Phase 12 (ADR 0031, 0032): commits made without a working
// tree, and merges computed without touching anyone's checkout. They run in
// the main repository only (ADR 0014).

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// runFull runs git with optional stdin and extra environment and returns
// stdout and the exit code; err is set only if git could not run or failed
// with a code other than those in okCodes.
func (g Git) runFull(ctx context.Context, dir string, stdin io.Reader, extraEnv []string, okCodes []int,
	args ...string) (string, int, error) {
	if err := g.checkTrusted(dir, args); err != nil {
		return "", 0, err
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
	cmd.Env = append(env(), extraEnv...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
		if slices.Contains(okCodes, code) {
			err = nil
		}
	}
	if err != nil {
		return "", code, &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return strings.TrimRight(stdout.String(), "\n"), code, nil
}

// validRepoPath checks a repository-relative file path for CommitFile.
func validRepoPath(p string) bool {
	return p != "" && len(p) <= 300 && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." &&
		!strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\n\r")
}

// CommitFile makes a commit on top of base that sets the file at p to content
// (mode 100644) and returns it. No working tree or checkout is touched: the
// commit is built in a temporary index file under tmpDir. The author and
// committer are the repository's configured user.
func (g Git) CommitFile(ctx context.Context, repo, base, p string, content []byte, message, tmpDir string) (string, error) {
	if !validRepoPath(p) {
		return "", fmt.Errorf("git: invalid path %q", p)
	}
	if strings.HasPrefix(base, "-") || strings.TrimSpace(message) == "" {
		return "", errors.New("git: CommitFile needs a base and a message")
	}
	idx, err := os.CreateTemp(tmpDir, "index-*")
	if err != nil {
		return "", err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath) // git creates it; an empty file is not a valid index
	defer os.Remove(idxPath)
	envIdx := []string{"GIT_INDEX_FILE=" + idxPath}
	blob, _, err := g.runFull(ctx, repo, bytes.NewReader(content), nil, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	steps := [][]string{
		{"read-tree", "--end-of-options", base + "^{tree}"},
		{"update-index", "--add", "--cacheinfo", "100644," + blob + "," + p},
	}
	for _, args := range steps {
		if _, _, err := g.runFull(ctx, repo, nil, envIdx, nil, args...); err != nil {
			return "", err
		}
	}
	tree, _, err := g.runFull(ctx, repo, nil, envIdx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return g.CommitTree(ctx, repo, tree, []string{base}, message)
}

// CommitTree makes a commit of tree with the given parents.
func (g Git) CommitTree(ctx context.Context, repo, tree string, parents []string, message string) (string, error) {
	if !shaRe.MatchString(tree) {
		return "", fmt.Errorf("git: invalid tree %q", tree)
	}
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		if strings.HasPrefix(p, "-") || p == "" {
			return "", fmt.Errorf("git: invalid parent %q", p)
		}
		args = append(args, "-p", p)
	}
	out, _, err := g.runFull(ctx, repo, strings.NewReader(message), nil, nil, append(args, "-F", "-")...)
	return out, err
}

// SetBranch moves (or creates, with old "") the agent-office branch to newSHA
// in repo, only if it is still at old: a compare-and-swap.
func (g Git) SetBranch(ctx context.Context, repo, branch, newSHA, old string) error {
	if !strings.HasPrefix(branch, BranchPrefix) || !g.ValidBranch(ctx, branch) {
		return fmt.Errorf("git: refusing to set branch %q outside %s", branch, BranchPrefix)
	}
	if !shaRe.MatchString(newSHA) || (old != "" && !shaRe.MatchString(old)) {
		return errors.New("git: SetBranch needs full commit hashes")
	}
	_, err := g.run(ctx, repo, "update-ref", "refs/heads/"+branch, newSHA, old)
	return err
}

// MergeResult is what merging two commits would give.
type MergeResult struct {
	Tree      string   // the merged tree (with conflict markers where Clean is false)
	Clean     bool     // no conflicts
	Conflicts []string // conflicted paths, sorted
}

// MergeTree computes the merge of a and b without a working tree (git
// merge-tree --write-tree, git 2.38+). Nothing changes but new objects.
func (g Git) MergeTree(ctx context.Context, repo, a, b string) (MergeResult, error) {
	for _, r := range []string{a, b} {
		if strings.HasPrefix(r, "-") || r == "" {
			return MergeResult{}, fmt.Errorf("git: invalid revision %q", r)
		}
	}
	out, code, err := g.runFull(ctx, repo, nil, nil, []int{1}, "merge-tree", "--write-tree", "--name-only",
		"--no-messages", "-z", a, b)
	if err != nil {
		return MergeResult{}, err
	}
	parts := strings.Split(out, "\x00")
	res := MergeResult{Tree: parts[0], Clean: code == 0}
	if !shaRe.MatchString(res.Tree) {
		return MergeResult{}, fmt.Errorf("git: unexpected merge-tree output")
	}
	for _, p := range parts[1:] {
		if p != "" && !slices.Contains(res.Conflicts, p) {
			res.Conflicts = append(res.Conflicts, p)
		}
	}
	slices.Sort(res.Conflicts)
	if !res.Clean && len(res.Conflicts) == 0 {
		return MergeResult{}, errors.New("git: merge-tree reported a conflict without files")
	}
	return res, nil
}

// FetchBranch fetches branch from remote into refs/remotes/<remote>/<branch>
// of repo (the main repository), without a terminal (ADR 0020, 0032), and
// returns its commit.
func (g Git) FetchBranch(ctx context.Context, repo, remote, branch string) (string, error) {
	if !remoteName.MatchString(remote) || !g.ValidBranch(ctx, branch) {
		return "", fmt.Errorf("git: invalid remote %q or branch %q", remote, branch)
	}
	if err := g.checkTrusted(repo, nil); err != nil {
		return "", err
	}
	dst := "refs/remotes/" + remote + "/" + branch
	if _, err := g.execWith(ctx, repo, PushTimeout, true, "fetch", "--no-tags", "--quiet", remote,
		"+refs/heads/"+branch+":"+dst); err != nil {
		return "", err
	}
	return g.RevParse(ctx, repo, dst)
}

// IsAncestor reports whether a is an ancestor of b (or equal) in repo.
func (g Git) IsAncestor(ctx context.Context, repo, a, b string) (bool, error) {
	return g.isAncestor(ctx, repo, a, b)
}
