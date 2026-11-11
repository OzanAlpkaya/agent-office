package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Sandboxed agents' repositories: import into the main repository, removal
// and saving uncommitted work (ADR 0014). The host never runs git in an agent
// repository; everything that looks inside one runs in a helper container.

// What started an import (imports.trigger).
const (
	TriggerExit     = "exit"
	TriggerManual   = "manual"
	TriggerRestart  = "restart"
	TriggerRemove   = "remove"
	TriggerSaveWork = "save_work"
)

// OutcomeNoCommits: the agent has no commits since its base.
const OutcomeNoCommits = "no_commits"

// maxBundleBytes bounds a bundle streamed out of a sandbox.
const maxBundleBytes = 512 << 20

// ErrNotSandboxed: host-mode agents commit in the user's repository directly.
var ErrNotSandboxed = fmt.Errorf("%w: only sandboxed agents are imported; host agents commit in your repository directly", ErrInvalid)

// ImportInfo is one import of an agent's branch (AgentInfo.LastImport).
type ImportInfo struct {
	Trigger  string          `json:"trigger"`
	Outcome  string          `json:"outcome"` // no_commits | created | fast_forward | up_to_date | diverged
	Branch   string          `json:"branch,omitempty"`
	Head     string          `json:"head,omitempty"`
	Previous string          `json:"previous,omitempty"`
	Commits  int             `json:"commits"`
	Flagged  []FlaggedCommit `json:"flagged"`
	At       time.Time       `json:"at"`
}

// FlaggedCommit is an imported commit that failed the identity check.
type FlaggedCommit struct {
	Commit  string `json:"commit"`
	Problem string `json:"problem"`
}

// Import brings a sandboxed agent's branch into the main repository, while
// the agent runs ("Import now") or after it exited. It is the single entry
// point for imports; Phase 4's report_done reuses it.
func (e *Engine) Import(ctx context.Context, id string) (ImportInfo, error) {
	return e.importAgent(ctx, id, TriggerManual)
}

func (e *Engine) sandboxedAgent(id string) (*agentProc, AgentInfo, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	switch {
	case !ok:
		return nil, AgentInfo{}, "", ErrNotFound
	case a.info.Sandbox != SandboxPodman || e.opts.Sandbox == nil:
		return nil, AgentInfo{}, "", ErrNotSandboxed
	case a.info.WorktreeStatus == WorktreeRemoved:
		return nil, AgentInfo{}, "", fmt.Errorf("%w: the agent's repository was removed", ErrInvalid)
	}
	return a, a.info, a.baseCommit, nil
}

func (e *Engine) importAgent(ctx context.Context, id, trigger string) (ImportInfo, error) {
	a, info, base, err := e.sandboxedAgent(id)
	if err != nil {
		return ImportInfo{}, err
	}
	a.importMu.Lock()
	defer a.importMu.Unlock()
	return e.importLocked(ctx, a, info, base, trigger)
}

// importLocked runs one import; the caller holds a.importMu.
func (e *Engine) importLocked(ctx context.Context, a *agentProc, info AgentInfo, base, trigger string) (ImportInfo, error) {
	log := e.log.With("agent_id", info.ID, "trigger", trigger)
	imp, err := e.runImport(ctx, info, base, trigger)
	if err != nil {
		log.Warn("import failed", "err", err)
		e.sandboxEvent(info.ID, sandbox.Event{Kind: sandbox.ImportFailed, Detail: firstLine(err.Error()), At: now()})
		return ImportInfo{}, err
	}
	flagged, _ := json.Marshal(imp.Flagged)
	if _, err := e.q.InsertImport(ctx, db.InsertImportParams{AgentID: info.ID, Trigger: trigger, Outcome: imp.Outcome,
		Branch: imp.Branch, Head: imp.Head, Previous: imp.Previous, Commits: int64(imp.Commits),
		Flagged: string(flagged), At: imp.At}); err != nil {
		return ImportInfo{}, fmt.Errorf("record import: %w", err)
	}
	if imp.Outcome == string(git.ImportDiverged) {
		e.sandboxEvent(info.ID, sandbox.Event{Kind: sandbox.ImportDiverged, At: imp.At,
			Detail: fmt.Sprintf("rewritten history imported as %s; %s stays at %s", imp.Branch, info.Branch, short(imp.Previous))})
	}
	for _, f := range imp.Flagged {
		e.sandboxEvent(info.ID, sandbox.Event{Kind: sandbox.CommitFlagged, Detail: short(f.Commit) + ": " + f.Problem, At: imp.At})
	}
	e.mu.Lock()
	a.info.LastImport = &imp
	e.publishAgentLocked(a)
	e.mu.Unlock()
	log.Info("import", "outcome", imp.Outcome, "branch", imp.Branch, "head", short(imp.Head), "commits", imp.Commits,
		"flagged", len(imp.Flagged))
	return imp, nil
}

func (e *Engine) runImport(ctx context.Context, info AgentInfo, base, trigger string) (ImportInfo, error) {
	imp := ImportInfo{Trigger: trigger, At: now(), Flagged: []FlaggedCommit{}}
	var out bytes.Buffer
	if err := e.helper(ctx, info, sandbox.GitIdentity{}, gitCmd(git.CountArgs(base, info.Branch)), &out, 64); err != nil {
		return imp, fmt.Errorf("count commits: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		return imp, fmt.Errorf("count commits: unexpected output %q", firstLine(out.String()))
	}
	if n == 0 {
		imp.Outcome = OutcomeNoCommits
		return imp, nil
	}

	dir := filepath.Join(e.opts.RuntimeDir, "imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return imp, err
	}
	f, err := os.CreateTemp(dir, info.ID+"-*.bundle")
	if err != nil {
		return imp, err
	}
	defer os.Remove(f.Name())
	herr := e.helper(ctx, info, sandbox.GitIdentity{}, gitCmd(git.BundleArgs(base, info.Branch)), f, maxBundleBytes)
	if cerr := f.Close(); herr == nil {
		herr = cerr
	}
	if herr != nil {
		return imp, fmt.Errorf("bundle: %w", herr)
	}
	res, err := e.git.ImportBundle(ctx, e.project.RepoPath, f.Name(), info.ID, info.Branch)
	if err != nil {
		return imp, err
	}
	imp.Outcome, imp.Branch, imp.Head, imp.Previous = string(res.Outcome), res.Branch, res.Head, res.Previous
	commits, err := e.git.BranchCommits(ctx, e.project.RepoPath, base, res.Branch)
	if err != nil {
		return imp, err
	}
	imp.Commits = len(commits)
	name, email, err := e.git.UserIdentity(ctx, e.project.RepoPath)
	if err != nil {
		return imp, err
	}
	imp.Flagged = checkCommits(commits, name, email, sandbox.CoAuthor(agentName(info.ID)))
	return imp, nil
}

// checkCommits flags commits that do not carry the user's identity as author
// and committer, or lack the agent's Co-Authored-By trailer (ADR 0014).
func checkCommits(commits []git.Commit, name, email, coAuthor string) []FlaggedCommit {
	flagged := []FlaggedCommit{}
	for _, c := range commits {
		var problems []string
		if c.AuthorName != name || c.AuthorEmail != email {
			problems = append(problems, fmt.Sprintf("author is %s <%s>", c.AuthorName, c.AuthorEmail))
		}
		if c.CommitterName != name || c.CommitterEmail != email {
			problems = append(problems, fmt.Sprintf("committer is %s <%s>", c.CommitterName, c.CommitterEmail))
		}
		found := false
		for _, co := range c.CoAuthors {
			found = found || co == coAuthor
		}
		if !found {
			problems = append(problems, "no Co-Authored-By trailer for "+coAuthor)
		}
		if len(problems) > 0 {
			flagged = append(flagged, FlaggedCommit{Commit: c.Hash, Problem: strings.Join(problems, "; ")})
		}
	}
	return flagged
}

// helper runs git in a short-lived helper container with the agent's
// repository mounted (ADR 0014).
func (e *Engine) helper(ctx context.Context, info AgentInfo, id sandbox.GitIdentity, cmd []string, out io.Writer, max int64) error {
	s := e.opts.Sandbox
	objects, err := e.git.ObjectsDir(ctx, e.project.RepoPath)
	if err != nil {
		return err
	}
	if id.Name == "" {
		if id.Name, id.Email, err = e.git.UserIdentity(ctx, e.project.RepoPath); err != nil {
			return err
		}
	}
	name, args, err := sandbox.HelperArgs(sandbox.HelperSpec{Instance: s.Instance, AgentID: info.ID, Image: s.Image,
		Workspace: info.WorktreePath, ObjectsDir: objects, Git: id, Command: cmd})
	if err != nil {
		return err
	}
	return s.Podman.RunContainer(ctx, name, args, out, max)
}

func gitCmd(args []string) []string { return append([]string{"git"}, args...) }

// workspaceDirty reports the number of changed or untracked paths in a
// sandboxed agent's repository.
func (e *Engine) workspaceDirty(ctx context.Context, info AgentInfo) (int, error) {
	var out bytes.Buffer
	if err := e.helper(ctx, info, sandbox.GitIdentity{}, gitCmd(git.StatusArgs()), &out, git.MaxStatusBytes); err != nil {
		return 0, fmt.Errorf("status: %w (the repository's objects must be readable by others for the sandbox user)", err)
	}
	return git.ParseStatus(out.Bytes())
}

// SaveWork commits everything uncommitted in an exited sandboxed agent's
// repository as one WIP commit (the user's identity plus the agent's trailer),
// then imports the branch. The user cannot commit there by hand: the
// repository belongs to the sandbox user (ADR 0014).
func (e *Engine) SaveWork(ctx context.Context, id string) (ImportInfo, error) {
	a, info, base, err := e.sandboxedAgent(id)
	if err != nil {
		return ImportInfo{}, err
	}
	e.mu.Lock()
	running := a.status.State != hooks.Exited
	e.mu.Unlock()
	if running {
		return ImportInfo{}, ErrStillRunning
	}
	if a.term != nil {
		<-a.exited
	}
	if a.afterExit != nil {
		<-a.afterExit // the exit saves the work itself (ADR 0051)
	}
	a.importMu.Lock()
	defer a.importMu.Unlock()
	n, err := e.workspaceDirty(ctx, info)
	if err != nil {
		return ImportInfo{}, err
	}
	if n == 0 {
		return ImportInfo{}, fmt.Errorf("%w: no uncommitted work to save", ErrInvalid)
	}
	if err := e.commitWorkLocked(ctx, info); err != nil {
		return ImportInfo{}, err
	}
	return e.importLocked(ctx, a, info, base, TriggerSaveWork)
}

// cleanClone removes a sandboxed agent's repository without losing work: its
// commits are imported first (a failed import refuses the removal), and a
// dirty repository is kept unless force. Then the base ref and, as in host
// mode, a branch without commits are deleted.
func (e *Engine) cleanClone(ctx context.Context, a *agentProc, info AgentInfo, base string, force bool) (RemoveResult, error) {
	s := e.opts.Sandbox
	if s == nil {
		return RemoveResult{}, fmt.Errorf("%w: this agent ran sandboxed; start the daemon with --sandbox podman to remove it", ErrInvalid)
	}
	a.importMu.Lock()
	defer a.importMu.Unlock()
	if _, err := os.Stat(info.WorktreePath); err == nil && info.WorktreeStatus != WorktreeRemoved {
		if _, err := e.importLocked(ctx, a, info, base, TriggerRemove); err != nil {
			return RemoveResult{Worktree: "kept", Branch: "kept"}, fmt.Errorf("import before removal (nothing was removed): %w", err)
		}
		if !force {
			n, err := e.workspaceDirty(ctx, info)
			if err != nil {
				return RemoveResult{}, err
			}
			if n > 0 {
				return RemoveResult{Worktree: "kept", Branch: "kept"}, ErrWorktreeDirty
			}
		}
		if err := s.Podman.RemoveAll(ctx, s.WorkspaceRoot, info.WorktreePath); err != nil {
			return RemoveResult{}, err
		}
	}
	if err := s.Podman.RemoveAll(ctx, s.AgentsRoot, filepath.Join(s.AgentsRoot, info.ID)); err != nil {
		e.log.Warn("remove agent config", "agent_id", info.ID, "err", err)
	}
	if err := e.git.UnprotectBase(ctx, e.project.RepoPath, info.ID); err != nil {
		return RemoveResult{}, err
	}
	return e.cleanBranch(ctx, info, base)
}

// reconcileSandbox removes this daemon's containers left by a previous run
// (the daemon died; conmon kept them running) and then imports, in the
// background, the agents whose workers were interrupted.
func (e *Engine) reconcileSandbox(ctx context.Context, interrupted []string) {
	if !e.hubbed {
		// Under a hub, other projects' workers may run already: the hub removes
		// the previous run's containers once, before any project opens (ADR 0046).
		removeOrphanContainers(ctx, e.opts.Sandbox.Podman, e.opts.Sandbox.Instance, e.log)
	}
	for _, id := range interrupted {
		e.wg.Add(1)
		e.restartImports.Add(1) // a restore waits for them (ADR 0035)
		go func() {
			defer e.wg.Done()
			defer e.restartImports.Done()
			ictx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_, _ = e.importAgent(ictx, id, TriggerRestart)
		}()
	}
}

// removeOrphanContainers removes this daemon instance's containers left by a
// previous run (the daemon died; conmon kept them running).
func removeOrphanContainers(ctx context.Context, pm sandbox.Podman, instance string, log *slog.Logger) {
	list, err := pm.ListAgentContainers(ctx)
	if err != nil {
		log.Warn("list containers", "err", err)
	}
	removed := 0
	for _, c := range list {
		if c.Instance != instance {
			continue
		}
		if err := pm.Remove(ctx, c.Name); err != nil {
			log.Warn("remove orphan container", "container", c.Name, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Info("orphan containers removed", "count", removed)
	}
}

func importFromRow(r db.Import) *ImportInfo {
	imp := &ImportInfo{Trigger: r.Trigger, Outcome: r.Outcome, Branch: r.Branch, Head: r.Head, Previous: r.Previous,
		Commits: int(r.Commits), At: r.At, Flagged: []FlaggedCommit{}}
	_ = json.Unmarshal([]byte(r.Flagged), &imp.Flagged)
	return imp
}

func (e *Engine) loadLastImport(ctx context.Context, id string) *ImportInfo {
	r, err := e.q.LatestImport(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil
	}
	return importFromRow(r)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
