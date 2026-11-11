package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Squash merges of a goal into the project's target branch, and pushing that
// branch (ADR 0055). They run in the main repository, only when the user asks.

// ErrMergeConflict is returned when the squash would not merge cleanly.
var ErrMergeConflict = errors.New("git: the merge has conflicts")

// ErrBranchMoved is returned when a branch is no longer where the caller saw it.
var ErrBranchMoved = errors.New("git: the branch moved")

// ValidLocalBranch reports whether name is a plain local branch name git accepts.
func (g Git) ValidLocalBranch(ctx context.Context, name string) bool {
	return !strings.HasPrefix(name, "-") && !strings.Contains(name, "@{") && name != "HEAD" && g.ValidBranch(ctx, name)
}

// CurrentBranch is the branch checked out in the worktree at path (e.g.
// "main"), or "" when its HEAD is detached.
func (g Git) CurrentBranch(ctx context.Context, path string) (string, error) {
	out, _, err := g.runFull(ctx, path, nil, nil, []int{1}, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(out, "refs/heads/"), nil
}

// TrackedChanges reports whether the worktree at path has staged or unstaged
// changes to tracked files, or an unfinished merge, rebase or cherry-pick.
// Untracked files do not count: git refuses on its own if one would be
// overwritten.
func (g Git) TrackedChanges(ctx context.Context, path string) (bool, error) {
	out, err := g.run(ctx, path, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	if out != "" {
		return true, nil
	}
	for _, ref := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		if _, err := g.run(ctx, path, "rev-parse", "-q", "--verify", ref); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// SquashCommit makes, without touching any worktree, the commit that squash-
// merges source into the commit target: the merged tree with target as the
// only parent, the repository's user identity and message. A conflict returns
// ErrMergeConflict with the files in res.Conflicts.
func (g Git) SquashCommit(ctx context.Context, repo, target, source, message string) (sha string, res MergeResult, err error) {
	if !shaRe.MatchString(target) || !shaRe.MatchString(source) {
		return "", MergeResult{}, errors.New("git: SquashCommit needs full commit hashes")
	}
	if res, err = g.MergeTree(ctx, repo, target, source); err != nil {
		return "", res, err
	}
	if !res.Clean {
		return "", res, ErrMergeConflict
	}
	sha, err = g.CommitTree(ctx, repo, res.Tree, []string{target}, message)
	return sha, res, err
}

// TreeOf is the tree of a commit.
func (g Git) TreeOf(ctx context.Context, repo, commit string) (string, error) {
	if !shaRe.MatchString(commit) {
		return "", fmt.Errorf("git: invalid commit %q", commit)
	}
	return g.run(ctx, repo, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
}

// FastForwardWorktree moves the branch checked out at path to commit and
// updates its files (git merge --ff-only). git refuses if that is not a fast
// forward (the branch moved) or a local change or untracked file would be
// overwritten.
func (g Git) FastForwardWorktree(ctx context.Context, path, commit string) error {
	if !shaRe.MatchString(commit) {
		return fmt.Errorf("git: invalid commit %q", commit)
	}
	_, err := g.run(ctx, path, "merge", "--ff-only", "--quiet", "--no-edit", commit)
	return err
}

// MoveBranch moves a local branch that is not checked out from old to new: a
// compare-and-swap, so a branch that moved meanwhile is left alone
// (ErrBranchMoved).
func (g Git) MoveBranch(ctx context.Context, repo, branch, newSHA, old string) error {
	if !g.ValidLocalBranch(ctx, branch) {
		return fmt.Errorf("git: invalid branch %q", branch)
	}
	if !shaRe.MatchString(newSHA) || !shaRe.MatchString(old) {
		return errors.New("git: MoveBranch needs full commit hashes")
	}
	if _, err := g.run(ctx, repo, "update-ref", "refs/heads/"+branch, newSHA, old); err != nil {
		if cur, rerr := g.RevParse(ctx, repo, "refs/heads/"+branch); rerr == nil && cur != old {
			return ErrBranchMoved
		}
		return err
	}
	return nil
}

// PushBranch pushes a local branch to the same name on remote: never forced,
// no tags, without a controlling terminal (ADR 0020). The remote refuses a
// non-fast-forward update.
func (g Git) PushBranch(ctx context.Context, repo, remote, branch string) error {
	return g.PushCommit(ctx, repo, remote, "refs/heads/"+branch, branch)
}

// PushCommit pushes src (a commit or a local ref) to branch on remote, the
// same way: exactly what src names, nothing later.
func (g Git) PushCommit(ctx context.Context, repo, remote, src, branch string) error {
	if src == "" || strings.HasPrefix(src, "-") || strings.ContainsAny(src, ": ") {
		return fmt.Errorf("git: invalid push source %q", src)
	}
	if !remoteName.MatchString(remote) {
		return fmt.Errorf("git: invalid remote name %q", remote)
	}
	if !g.ValidLocalBranch(ctx, branch) {
		return fmt.Errorf("git: invalid branch %q", branch)
	}
	if err := g.checkTrusted(repo, nil); err != nil {
		return err
	}
	_, err := g.execWith(ctx, repo, PushTimeout, true, "push", "--no-follow-tags", "--quiet", "--end-of-options", remote,
		src+":refs/heads/"+branch)
	return err
}

// Branch is a local branch and its commit.
type Branch struct {
	Name string
	Head string
}

// AgentBranches lists the local branches under BranchPrefix (Phase 18).
func (g Git) AgentBranches(ctx context.Context, repo string) ([]Branch, error) {
	out, err := g.run(ctx, repo, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/"+BranchPrefix)
	if err != nil {
		return nil, err
	}
	var list []Branch
	for _, line := range strings.Split(out, "\n") {
		name, head, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && strings.HasPrefix(name, BranchPrefix) && shaRe.MatchString(head) {
			list = append(list, Branch{Name: name, Head: head})
		}
	}
	return list, nil
}

// DeleteBranchAt deletes an agent-office branch only if it still points at
// head: a compare-and-swap (ErrBranchMoved otherwise). It does not check
// worktrees: callers refuse branches that are checked out first.
func (g Git) DeleteBranchAt(ctx context.Context, repo, branch, head string) error {
	if !strings.HasPrefix(branch, BranchPrefix) || !g.ValidBranch(ctx, branch) {
		return fmt.Errorf("git: refusing to delete branch %q outside %s", branch, BranchPrefix)
	}
	if !shaRe.MatchString(head) {
		return errors.New("git: DeleteBranchAt needs a full commit hash")
	}
	if _, err := g.run(ctx, repo, "update-ref", "-d", "refs/heads/"+branch, head); err != nil {
		if cur, rerr := g.RevParse(ctx, repo, "refs/heads/"+branch); rerr == nil && cur != head {
			return ErrBranchMoved
		}
		return err
	}
	return nil
}
