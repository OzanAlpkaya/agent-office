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
	"strings"
)

// Shared clones and the bundle hand-off for sandboxed agents (ADR 0014).
//
// The host sets an agent repository up with CreateSharedClone and never runs
// git in it again. Commands that inspect it run inside the sandbox; their
// argument lists come from the *Args functions below, and their output is
// parsed as untrusted data. Work returns as a bundle that ImportBundle
// verifies and fetches into the main repository.

// ErrRepoHasAlternates means the main repository borrows objects itself; its
// alternates chain would have to be mounted too, which is not supported.
var ErrRepoHasAlternates = errors.New("git: repositories that use alternates are not supported for sandboxed agents")

// BaseRefPrefix holds one ref per sandboxed agent in the main repository,
// keeping the agent's base commit (and the objects its clone borrows)
// reachable through git gc.
const BaseRefPrefix = "refs/agent-office/base/"

var agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ObjectsDir returns the absolute object directory of repo (shared by all its
// worktrees), which an agent clone borrows from and the sandbox mounts
// read-only at the same path.
func (g Git) ObjectsDir(ctx context.Context, repo string) (string, error) {
	common, err := g.run(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	objects := filepath.Join(common, "objects")
	if b, err := os.ReadFile(filepath.Join(objects, "info", "alternates")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return "", ErrRepoHasAlternates
	}
	return objects, nil
}

// CreateSharedClone sets up a new agent repository at path (which must not
// exist): no hook templates, no remote, no refs except branch at base, and the
// objects of repo borrowed through objects/info/alternates. It is the only
// operation allowed to run git under an untrusted root, and it runs before the
// agent has had access to path. On failure, path is removed again.
func (g Git) CreateSharedClone(ctx context.Context, repo, path, branch, base string) (err error) {
	if !g.ValidBranch(ctx, branch) || !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("git: invalid agent branch name %q", branch)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("git: clone path must be absolute: %q", path)
	}
	objects, err := g.ObjectsDir(ctx, repo)
	if err != nil {
		return err
	}
	if base, err = g.run(ctx, repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}"); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("git: clone path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(path)
		}
	}()
	// An empty --template keeps the user's hook templates out of the clone.
	if _, err := g.exec(ctx, "", "init", "--quiet", "--template=", "--", path); err != nil {
		return err
	}
	info := filepath.Join(path, ".git", "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(objects+"\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"update-ref", "refs/heads/" + branch, base},
		{"symbolic-ref", "HEAD", "refs/heads/" + branch},
		{"reset", "--hard", "--quiet"},
	} {
		if _, err := g.exec(ctx, path, args...); err != nil {
			return err
		}
	}
	return nil
}

// ProtectBase records base under BaseRefPrefix+agentID in repo.
func (g Git) ProtectBase(ctx context.Context, repo, agentID, base string) error {
	if !agentIDRe.MatchString(agentID) {
		return fmt.Errorf("git: invalid agent id %q", agentID)
	}
	_, err := g.run(ctx, repo, "update-ref", BaseRefPrefix+agentID, base)
	return err
}

// UnprotectBase deletes the base ref of agentID; a missing ref is not an error.
func (g Git) UnprotectBase(ctx context.Context, repo, agentID string) error {
	if !agentIDRe.MatchString(agentID) {
		return fmt.Errorf("git: invalid agent id %q", agentID)
	}
	_, err := g.run(ctx, repo, "update-ref", "-d", BaseRefPrefix+agentID)
	return err
}

// ---- commands that run inside the sandbox (argument lists only) ----

// BundleArgs writes the agent's commits since base to stdout as a bundle.
// git refuses an empty bundle, so check CountArgs first.
func BundleArgs(base, branch string) []string {
	return []string{"bundle", "create", "-", base + "..refs/heads/" + branch}
}

// CountArgs prints the number of commits on branch since base.
func CountArgs(base, branch string) []string {
	return []string{"rev-list", "--count", base + "..refs/heads/" + branch, "--"}
}

// StatusArgs prints the working tree status for ParseStatus. Command-line
// config outranks the repository's, so an agent-written core.fsmonitor does
// not run even inside the helper container.
func StatusArgs() []string {
	// --no-optional-locks: the helper also runs while the agent works; a status
	// that refreshes the index would take index.lock and break the agent's git.
	return []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z",
		"--untracked-files=normal", "--ignore-submodules=none"}
}

// MaxStatusBytes bounds the status output read from a sandbox.
const MaxStatusBytes = 1 << 20

// ParseStatus parses `git status --porcelain=v1 -z` output from a sandbox and
// returns the number of changed or untracked paths. The output is untrusted:
// anything that is not well-formed porcelain is an error.
func ParseStatus(out []byte) (int, error) {
	if len(out) > MaxStatusBytes {
		return 0, fmt.Errorf("git: status output larger than %d bytes", MaxStatusBytes)
	}
	if len(out) == 0 {
		return 0, nil
	}
	if out[len(out)-1] != 0 {
		return 0, errors.New("git: malformed status output (no terminator)")
	}
	recs := bytes.Split(out[:len(out)-1], []byte{0})
	n := 0
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 4 || r[2] != ' ' || !statusCode(r[0]) || !statusCode(r[1]) {
			return 0, errors.New("git: malformed status record")
		}
		n++
		if r[0] == 'R' || r[0] == 'C' { // rename/copy: the original path follows
			if i+1 >= len(recs) || len(recs[i+1]) == 0 {
				return 0, errors.New("git: malformed status rename record")
			}
			i++
		}
	}
	return n, nil
}

func statusCode(c byte) bool { return strings.IndexByte(" MTADRCU?!", c) >= 0 }

// ---- import on the host (main repository, trusted) ----

// ImportOutcome says what an import did to the main repository.
type ImportOutcome string

const (
	ImportCreated     ImportOutcome = "created"      // the agent branch did not exist yet
	ImportFastForward ImportOutcome = "fast_forward" // new commits on top of what was imported
	ImportUpToDate    ImportOutcome = "up_to_date"   // nothing new
	ImportDiverged    ImportOutcome = "diverged"     // rewritten history: imported under a new -rewrite-N branch
)

// ImportResult is the outcome of ImportBundle.
type ImportResult struct {
	Head     string        // the agent's branch head
	Branch   string        // the branch in the main repository that now points at Head
	Outcome  ImportOutcome //
	Previous string        // the branch's previous head (empty when created)
}

// ErrBranchCheckedOut means the branch an import would move is checked out
// in a worktree of the main repository; moving it would leave that working
// tree looking modified.
var ErrBranchCheckedOut = errors.New("git: the agent branch is checked out in your repository; switch to another branch to import")

const maxRewrites = 100

// ImportBundle verifies a bundle written by an agent's sandbox (exactly one
// ref, refs/heads/<branch>), fetches it with fsck into a private ref, and then
// updates the main repository without ever moving a branch backwards
// (ADR 0014):
//   - nothing imported yet: create <branch>
//   - a known branch (<branch> or an earlier <branch>-rewrite-N) already at
//     the head: up to date
//   - a known branch that is an ancestor of the head: fast-forward it
//   - otherwise (the agent rebased, reset or amended): create the next
//     <branch>-rewrite-N and leave the earlier branches untouched
//
// Only agent branches are touched, each with a compare-and-swap update.
func (g Git) ImportBundle(ctx context.Context, repo, bundle, agentID, branch string) (ImportResult, error) {
	if !g.ValidBranch(ctx, branch) || !strings.HasPrefix(branch, BranchPrefix) {
		return ImportResult{}, fmt.Errorf("git: invalid agent branch name %q", branch)
	}
	if !agentIDRe.MatchString(agentID) {
		return ImportResult{}, fmt.Errorf("git: invalid agent id %q", agentID)
	}
	if !filepath.IsAbs(bundle) {
		return ImportResult{}, fmt.Errorf("git: bundle path must be absolute: %q", bundle)
	}
	ref := "refs/heads/" + branch
	if _, err := g.run(ctx, repo, "bundle", "verify", "--quiet", bundle); err != nil {
		return ImportResult{}, err
	}
	heads, err := g.run(ctx, repo, "bundle", "list-heads", bundle)
	if err != nil {
		return ImportResult{}, err
	}
	lines := strings.Split(heads, "\n")
	head, name, _ := strings.Cut(lines[0], " ")
	if len(lines) != 1 || name != ref || len(head) != 40 {
		return ImportResult{}, fmt.Errorf("git: bundle must contain exactly %s", ref)
	}
	incoming := "refs/agent-office/incoming/" + agentID
	if _, err := g.run(ctx, repo, "-c", "transfer.fsckObjects=true", "fetch", "--quiet", "--no-tags",
		"--no-write-fetch-head", "--no-recurse-submodules", bundle, "+"+ref+":"+incoming); err != nil {
		return ImportResult{}, err
	}
	defer func() { _, _ = g.run(context.WithoutCancel(ctx), repo, "update-ref", "-d", incoming) }()

	// Known branches, newest first: the latest rewrite is the likeliest base.
	known := []string{branch}
	free := ""
	for n := 2; n < 2+maxRewrites; n++ {
		b := fmt.Sprintf("%s-rewrite-%d", branch, n)
		if ok, err := g.BranchExists(ctx, repo, b); err != nil {
			return ImportResult{}, err
		} else if !ok {
			free = b
			break
		}
		known = append([]string{b}, known...)
	}
	for _, b := range known {
		cur, ok, err := g.branchHead(ctx, repo, b)
		if err != nil {
			return ImportResult{}, err
		}
		if !ok {
			continue
		}
		if cur == head {
			return ImportResult{Head: head, Branch: b, Outcome: ImportUpToDate, Previous: cur}, nil
		}
		if anc, err := g.isAncestor(ctx, repo, cur, head); err != nil {
			return ImportResult{}, err
		} else if anc {
			if err := g.moveAgentBranch(ctx, repo, b, head, cur); err != nil {
				return ImportResult{}, err
			}
			return ImportResult{Head: head, Branch: b, Outcome: ImportFastForward, Previous: cur}, nil
		}
	}
	if _, ok, err := g.branchHead(ctx, repo, branch); err != nil {
		return ImportResult{}, err
	} else if !ok {
		if err := g.moveAgentBranch(ctx, repo, branch, head, ""); err != nil {
			return ImportResult{}, err
		}
		return ImportResult{Head: head, Branch: branch, Outcome: ImportCreated}, nil
	}
	if free == "" || !g.ValidBranch(ctx, free) {
		return ImportResult{}, fmt.Errorf("git: no free rewrite branch name for %s", branch)
	}
	prev, _, _ := g.branchHead(ctx, repo, known[0])
	if err := g.moveAgentBranch(ctx, repo, free, head, ""); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Head: head, Branch: free, Outcome: ImportDiverged, Previous: prev}, nil
}

// moveAgentBranch sets refs/heads/<branch> to head if it is still at old
// (empty old: only if it does not exist), refusing a branch that is checked
// out in a worktree of repo.
func (g Git) moveAgentBranch(ctx context.Context, repo, branch, head, old string) error {
	wts, err := g.ListWorktrees(ctx, repo)
	if err != nil {
		return err
	}
	for _, wt := range wts {
		if wt.Branch == "refs/heads/"+branch {
			return fmt.Errorf("%w (%s in %s)", ErrBranchCheckedOut, branch, wt.Path)
		}
	}
	if old == "" {
		old = strings.Repeat("0", 40) // must not exist yet
	}
	_, err = g.run(ctx, repo, "update-ref", "-m", "agent-office import", "refs/heads/"+branch, head, old)
	return err
}

// branchHead returns the commit of a local branch, if it exists.
func (g Git) branchHead(ctx context.Context, repo, branch string) (string, bool, error) {
	ok, err := g.BranchExists(ctx, repo, branch)
	if err != nil || !ok {
		return "", false, err
	}
	h, err := g.run(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	return h, err == nil, err
}

// isAncestor reports whether commit a is an ancestor of (or equal to) b.
func (g Git) isAncestor(ctx context.Context, repo, a, b string) (bool, error) {
	_, err := g.run(ctx, repo, "merge-base", "--is-ancestor", a, b)
	var gerr *Error
	var exit *exec.ExitError
	if errors.As(err, &gerr) && errors.As(gerr.Err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// UserIdentity returns user.name and user.email as configured for repo.
func (g Git) UserIdentity(ctx context.Context, repo string) (name, email string, err error) {
	if name, err = g.run(ctx, repo, "config", "--get", "user.name"); err != nil {
		return "", "", fmt.Errorf("git: user.name is not set: %w", err)
	}
	if email, err = g.run(ctx, repo, "config", "--get", "user.email"); err != nil {
		return "", "", fmt.Errorf("git: user.email is not set: %w", err)
	}
	return name, email, nil
}

// Commit is one commit on an agent branch, for the identity check on import.
type Commit struct {
	Hash, AuthorName, AuthorEmail, CommitterName, CommitterEmail string
	CoAuthors                                                    []string // Co-Authored-By trailer values
}

// BranchCommits lists the commits on branch since base in repo, newest first.
func (g Git) BranchCommits(ctx context.Context, repo, base, branch string) ([]Commit, error) {
	out, err := g.run(ctx, repo, "log",
		"--format=%H%x00%an%x00%ae%x00%cn%x00%ce%x00%(trailers:key=Co-authored-by,valueonly,separator=%x01)%x1e",
		base+"..refs/heads/"+branch, "--")
	if err != nil {
		return nil, err
	}
	var list []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimPrefix(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x00")
		if len(f) != 6 {
			return nil, fmt.Errorf("git: unexpected log record with %d fields", len(f))
		}
		c := Commit{Hash: f[0], AuthorName: f[1], AuthorEmail: f[2], CommitterName: f[3], CommitterEmail: f[4]}
		for _, v := range strings.Split(strings.TrimSpace(f[5]), "\x01") {
			if v = strings.TrimSpace(v); v != "" {
				c.CoAuthors = append(c.CoAuthors, v)
			}
		}
		list = append(list, c)
	}
	return list, nil
}
