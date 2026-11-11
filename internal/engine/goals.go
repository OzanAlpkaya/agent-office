package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Goals after review (ADR 0054, 0055): the record a done goal keeps in the
// archive, removing its exited agents in one action, and the user's squash
// merge of its tested work into the project's target branch, and its push.

// ErrMergeRefused is a merge or push refused for a reason the user can fix
// (a dirty tree, a conflict, a branch that moved); the message says which.
var ErrMergeRefused = errors.New("merge refused")

func mergeRefused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMergeRefused, fmt.Sprintf(format, args...))
}

// GoalReport is the Tech Lead's report of a goal. Agent-written: render as text.
type GoalReport struct {
	AgentID       string    `json:"agent_id,omitempty"`
	Summary       string    `json:"summary"`
	CommitMessage string    `json:"commit_message,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// GoalMerge is one squash merge of a goal by the user, and its push.
type GoalMerge struct {
	ID           int64      `json:"id"`
	GoalID       string     `json:"goal_id"`
	QARunID      int64      `json:"qa_run_id,omitempty"`
	Target       string     `json:"target"`
	SourceHead   string     `json:"source_head"`
	TargetBefore string     `json:"target_before"`
	Commit       string     `json:"commit"`
	Message      string     `json:"message"`
	CreatedAt    time.Time  `json:"created_at"`
	PushedAt     *time.Time `json:"pushed_at,omitempty"`
	PushRemote   string     `json:"push_remote,omitempty"`
}

func goalMergeFromRow(r db.GoalMerge) GoalMerge {
	m := GoalMerge{ID: r.ID, GoalID: r.GoalID, QARunID: r.QaRunID.Int64, Target: r.Target, SourceHead: r.SourceHead,
		TargetBefore: r.TargetBefore, Commit: r.CommitSha, Message: r.Message, CreatedAt: r.CreatedAt.UTC(),
		PushRemote: r.PushRemote}
	if r.PushedAt.Valid {
		t := r.PushedAt.Time.UTC()
		m.PushedAt = &t
	}
	return m
}

// GoalAgent is an agent that worked on a goal, removed ones included.
type GoalAgent struct {
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	Role       string     `json:"role"`
	Kind       string     `json:"kind"`
	CLI        string     `json:"cli"`
	Model      string     `json:"model,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	Branch     string     `json:"branch"`
	State      string     `json:"state"`
	ExitReason string     `json:"exit_reason,omitempty"`
	ExitDetail string     `json:"exit_detail,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ExitedAt   *time.Time `json:"exited_at,omitempty"`
	Removed    bool       `json:"removed"`
}

// GoalRecord is everything the archive shows about a goal.
type GoalRecord struct {
	Goal     Task        `json:"goal"`
	SubTasks []Task      `json:"sub_tasks"`
	Agents   []GoalAgent `json:"agents"`
	QARuns   []QARun     `json:"qa_runs"` // newest first
	Report   *GoalReport `json:"report,omitempty"`
	Merges   []GoalMerge `json:"merges"` // newest first
}

// getGoal is a top-level task of this project.
func (e *Engine) getGoal(ctx context.Context, id string) (db.Task, error) {
	t, err := e.getTask(ctx, id)
	if err != nil {
		return db.Task{}, err
	}
	if t.ParentTaskID.Valid {
		return db.Task{}, invalid("task %s is a sub-task, not a goal", id)
	}
	return t, nil
}

func (e *Engine) latestReport(ctx context.Context, goalID string) *GoalReport {
	r, err := e.q.LatestGoalReport(ctx, goalID)
	if err != nil {
		return nil
	}
	return &GoalReport{AgentID: r.AgentID.String, Summary: r.Summary, CommitMessage: r.CommitMessage, CreatedAt: r.CreatedAt.UTC()}
}

// GoalRecord returns a goal with its sub-tasks, every agent that worked on
// it, its QA runs, the Tech Lead's report and its merges (ADR 0054).
func (e *Engine) GoalRecord(ctx context.Context, id string) (GoalRecord, error) {
	goal, err := e.getGoal(ctx, id)
	if err != nil {
		return GoalRecord{}, err
	}
	rec := GoalRecord{Goal: taskFromRow(goal), SubTasks: []Task{}, Agents: []GoalAgent{}, QARuns: []QARun{}, Merges: []GoalMerge{}}
	kids, err := e.q.ListChildTasks(ctx, nullString(id))
	if err != nil {
		return GoalRecord{}, err
	}
	for _, k := range kids {
		rec.SubTasks = append(rec.SubTasks, taskFromRow(k))
	}
	agents, err := e.q.ListGoalAgents(ctx, db.ListGoalAgentsParams{ProjectID: e.project.ID, GoalID: nullString(id)})
	if err != nil {
		return GoalRecord{}, err
	}
	for _, a := range agents {
		ga := GoalAgent{ID: a.ID, Name: a.Name, Role: a.Role, Kind: a.Kind, CLI: cliOr(a.Cli), Model: a.Model,
			TaskID: a.TaskID.String, Branch: a.Branch, State: a.State, ExitReason: a.ExitReason, ExitDetail: a.ExitDetail,
			StartedAt: a.StartedAt.UTC(), Removed: a.RemovedAt.Valid}
		if a.ExitedAt.Valid {
			t := a.ExitedAt.Time.UTC()
			ga.ExitedAt = &t
		}
		rec.Agents = append(rec.Agents, ga)
	}
	runs, err := e.q.ListGoalQARuns(ctx, id)
	if err != nil {
		return GoalRecord{}, err
	}
	for _, r := range runs {
		rec.QARuns = append(rec.QARuns, qaRunFromRow(r))
	}
	rec.Report = e.latestReport(ctx, id)
	merges, err := e.q.ListProjectGoalMerges(ctx, db.ListProjectGoalMergesParams{ProjectID: e.project.ID, Limit: 1000})
	if err != nil {
		return GoalRecord{}, err
	}
	for _, m := range merges {
		if m.GoalID == id {
			rec.Merges = append(rec.Merges, goalMergeFromRow(m))
		}
	}
	return rec, nil
}

// GoalMerges lists the project's merges, newest first, for the board.
func (e *Engine) GoalMerges(ctx context.Context) ([]GoalMerge, error) {
	rows, err := e.q.ListProjectGoalMerges(ctx, db.ListProjectGoalMergesParams{ProjectID: e.project.ID, Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]GoalMerge, 0, len(rows))
	for _, r := range rows {
		out = append(out, goalMergeFromRow(r))
	}
	return out, nil
}

// RemovedAgent is one agent of a goal cleanup.
type RemovedAgent struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name,omitempty"`
	Branch  string `json:"branch,omitempty"` // "deleted" | "kept" for a removed agent
	Reason  string `json:"reason,omitempty"` // why a kept agent stays
}

// GoalCleanup is what RemoveGoalAgents did.
type GoalCleanup struct {
	Removed []RemovedAgent `json:"removed"`
	Kept    []RemovedAgent `json:"kept"`
}

// RemoveGoalAgents removes every exited agent of a done goal and its
// sub-tasks with the normal, work-preserving removal (ADR 0010, 0054): a
// dirty workspace is kept, a branch with commits is kept. Running agents are
// not touched.
func (e *Engine) RemoveGoalAgents(ctx context.Context, goalID string) (GoalCleanup, error) {
	goal, err := e.getGoal(ctx, goalID)
	if err != nil {
		return GoalCleanup{}, err
	}
	if goal.Status != StatusDone {
		return GoalCleanup{}, invalid("mark the goal done first; its agents may still be needed")
	}
	rows, err := e.q.ListGoalAgents(ctx, db.ListGoalAgentsParams{ProjectID: e.project.ID, GoalID: nullString(goalID)})
	if err != nil {
		return GoalCleanup{}, err
	}
	out := GoalCleanup{Removed: []RemovedAgent{}, Kept: []RemovedAgent{}}
	for _, r := range rows {
		if r.RemovedAt.Valid {
			continue
		}
		item := RemovedAgent{AgentID: r.ID, Name: r.Name}
		e.mu.Lock()
		a, ok := e.agents[r.ID]
		running := ok && a.status.State != hooks.Exited
		e.mu.Unlock()
		switch {
		case !ok:
			continue
		case running:
			item.Reason = "still running"
			out.Kept = append(out.Kept, item)
			continue
		}
		res, err := e.Remove(ctx, r.ID)
		switch {
		case errors.Is(err, ErrWorktreeDirty):
			item.Reason = "its workspace has uncommitted changes; kept (Save work or Discard on its card)"
			out.Kept = append(out.Kept, item)
		case err != nil:
			item.Reason = firstLine(err.Error())
			out.Kept = append(out.Kept, item)
		default:
			item.Branch = res.Branch
			out.Removed = append(out.Removed, item)
		}
	}
	e.log.Info("goal agents removed", "task_id", goalID, "removed", len(out.Removed), "kept", len(out.Kept))
	return out, nil
}

// MergePreview is what the Merge confirmation shows, computed without
// changing anything. Ready is false with Reason when the merge would be
// refused.
type MergePreview struct {
	GoalID     string     `json:"goal_id"`
	QARunID    int64      `json:"qa_run_id,omitempty"`
	Branch     string     `json:"branch"`      // the QA integration branch
	SourceHead string     `json:"source_head"` // what QA passed on
	Target     string     `json:"target"`
	TargetHead string     `json:"target_head"`
	Message    string     `json:"message"`     // the Tech Lead's commit message, or a default
	Files      int        `json:"files"`       // files the squash changes
	Paths      []string   `json:"paths"`       // the first of them (maxPreviewPaths), for the user to read
	Sensitive  []string   `json:"sensitive"`   // changed paths that run code on CI or in hooks
	Untested   int        `json:"untested"`    // target commits QA did not test with (the target moved since)
	CheckedOut bool       `json:"checked_out"` // the target is checked out in the repository: its files change
	Ready      bool       `json:"ready"`
	Reason     string     `json:"reason,omitempty"`
	Merged     *GoalMerge `json:"merged,omitempty"` // the latest merge of this goal
	Remote     string     `json:"remote"`           // what Push pushes to
	RemoteURL  string     `json:"remote_url,omitempty"`
}

// MergeRequest is the user's confirmation: the heads the preview showed and
// the message to commit.
type MergeRequest struct {
	TargetHead string `json:"target_head"`
	SourceHead string `json:"source_head"`
	Message    string `json:"message"`
}

const goalTrailer = "Agent-Office-Goal"

func (e *Engine) mergeTarget() string {
	if e.opts.MergeTarget != "" {
		return e.opts.MergeTarget
	}
	return DefaultMergeTarget
}

func (e *Engine) pushRemote() string {
	if e.opts.PushRemote != "" {
		return e.opts.PushRemote
	}
	return "origin"
}

// defaultMergeMessage is used when the Tech Lead gave no commit message.
func defaultMergeMessage(goal db.Task, report *GoalReport) string {
	msg := truncateRunes(goal.Title, 72)
	if report != nil && report.Summary != "" {
		msg += "\n\n" + report.Summary
	}
	return msg
}

// withGoalTrailer appends the goal's trailer to a commit message.
func withGoalTrailer(msg, goalID string) string {
	msg = strings.TrimRight(msg, "\n")
	line := goalTrailer + ": " + goalID
	if strings.Contains(msg, line) {
		return msg + "\n"
	}
	return msg + "\n\n" + line + "\n"
}

// mergeState is what preview and merge both check.
type mergeState struct {
	goal                   db.Task
	run                    db.QaRun
	target, targetHead     string
	sourceHead, branch     string
	checkedOut             bool
	files, untested        int
	paths                  []string
	alreadyMerged          *GoalMerge
	report                 *GoalReport
	worktreeOfTarget, repo string
}

// checkMerge gathers the state of a goal's merge; a non-nil error is the
// reason it would be refused. st is filled as far as it got.
func (e *Engine) checkMerge(ctx context.Context, goalID string) (st mergeState, err error) {
	st.repo, st.target = e.project.RepoPath, e.mergeTarget()
	if st.goal, err = e.getGoal(ctx, goalID); err != nil {
		return st, err
	}
	st.report = e.latestReport(ctx, goalID)
	if m, merr := e.q.LatestGoalMerge(ctx, goalID); merr == nil {
		v := goalMergeFromRow(m)
		st.alreadyMerged = &v
	}
	if st.goal.Status != StatusReview && st.goal.Status != StatusDone {
		return st, mergeRefused("the goal is %s: the Tech Lead reports it once QA passed", st.goal.Status)
	}
	run, rerr := e.q.LatestQARun(ctx, goalID)
	if errors.Is(rerr, sql.ErrNoRows) {
		return st, mergeRefused("QA has not run on this goal: there is no tested branch to merge")
	}
	if rerr != nil {
		return st, rerr
	}
	st.run, st.branch, st.sourceHead = run, run.Branch, run.Head
	if run.Status != QAPassed || run.Head == "" {
		return st, mergeRefused("the last QA run %s: only work QA passed is merged", run.Status)
	}
	if e.opts.QA {
		kids, kerr := e.q.ListChildTasks(ctx, nullString(goalID))
		if kerr != nil {
			return st, kerr
		}
		if gerr := e.qaGate(ctx, st.goal, kids); gerr != nil {
			return st, mergeRefused("%s", strings.TrimPrefix(gerr.Error(), ErrInvalid.Error()+": "))
		}
	}
	if st.alreadyMerged != nil && st.alreadyMerged.SourceHead == st.sourceHead {
		return st, mergeRefused("already merged into %s as %s", st.alreadyMerged.Target, short(st.alreadyMerged.Commit))
	}
	if st.targetHead, err = e.git.RevParse(ctx, st.repo, "refs/heads/"+st.target); err != nil {
		return st, mergeRefused("the target branch %s does not exist in the repository", st.target)
	}
	if _, err = e.git.RevParse(ctx, st.repo, st.sourceHead); err != nil {
		return st, mergeRefused("QA's commit %s is no longer in the repository", short(st.sourceHead))
	}
	if e.isAncestor(ctx, st.sourceHead, st.targetHead) {
		return st, mergeRefused("%s already contains QA's commit %s", st.target, short(st.sourceHead))
	}
	if n, cerr := e.git.CommitsAhead(ctx, st.repo, st.sourceHead, st.targetHead); cerr == nil {
		st.untested = n
	}
	mr, merr := e.git.MergeTree(ctx, st.repo, st.targetHead, st.sourceHead)
	if merr != nil {
		return st, merr
	}
	if !mr.Clean {
		return st, mergeRefused("%s has changed since QA tested, and the merge conflicts in %s: let the Tech Lead "+
			"bring the goal up to date (a follow-up), then QA again", st.target, strings.Join(mr.Conflicts, ", "))
	}
	if tree, terr := e.git.TreeOf(ctx, st.repo, st.targetHead); terr == nil && tree == mr.Tree {
		return st, mergeRefused("the goal's changes are already in %s", st.target)
	}
	if files, ferr := e.git.ChangedFiles(ctx, st.repo, st.targetHead, mr.Tree); ferr == nil {
		st.files, st.paths = len(files), files
	}
	wts, werr := e.git.ListWorktrees(ctx, st.repo)
	if werr != nil {
		return st, werr
	}
	for i, w := range wts {
		if w.Branch != "refs/heads/"+st.target {
			continue
		}
		if i == 0 {
			st.checkedOut, st.worktreeOfTarget = true, w.Path
		} else {
			return st, mergeRefused("%s is checked out in another worktree (%s): switch it to another branch first",
				st.target, w.Path)
		}
	}
	if st.checkedOut {
		dirty, derr := e.git.TrackedChanges(ctx, st.worktreeOfTarget)
		if derr != nil {
			return st, derr
		}
		if dirty {
			return st, mergeRefused("%s is checked out with uncommitted changes (or an unfinished merge or rebase): "+
				"commit or stash them first", st.target)
		}
	}
	if _, _, ierr := e.git.UserIdentity(ctx, st.repo); ierr != nil {
		return st, mergeRefused("git has no user.name or user.email for this repository: set them, the merge is your commit")
	}
	return st, nil
}

// MergePreview shows what Merge would do (ADR 0055).
func (e *Engine) MergePreview(ctx context.Context, goalID string) (MergePreview, error) {
	st, err := e.checkMerge(ctx, goalID)
	if errors.Is(err, ErrNotFound) || (err != nil && st.goal.ID == "") {
		return MergePreview{}, err
	}
	p := MergePreview{GoalID: goalID, QARunID: st.run.ID, Branch: st.branch, SourceHead: st.sourceHead, Target: st.target,
		TargetHead: st.targetHead, Files: st.files, Paths: firstN(st.paths, maxPreviewPaths),
		Sensitive: sensitivePaths(st.paths), Untested: st.untested, CheckedOut: st.checkedOut, Ready: err == nil,
		Merged: st.alreadyMerged, Remote: e.pushRemote()}
	p.Message = defaultMergeMessage(st.goal, st.report)
	if st.report != nil && st.report.CommitMessage != "" {
		p.Message = st.report.CommitMessage
	}
	if err != nil {
		if !errors.Is(err, ErrMergeRefused) {
			return MergePreview{}, err
		}
		p.Reason = strings.TrimPrefix(err.Error(), ErrMergeRefused.Error()+": ")
	}
	if u, uerr := e.git.RemoteURL(ctx, e.project.RepoPath, p.Remote); uerr == nil {
		p.RemoteURL = redactURL(u)
	}
	return p, nil
}

// MergeGoal squash-merges the goal's QA-passed branch into the target branch
// with the confirmed message (ADR 0055). It is refused unless everything is
// as the confirmation showed: the same heads, a clean merge, no uncommitted
// changes where the target is checked out.
func (e *Engine) MergeGoal(ctx context.Context, goalID string, req MergeRequest) (GoalMerge, error) {
	msg, err := normalizeText(req.Message, MaxCommitMessageLen, "message")
	if err != nil {
		return GoalMerge{}, err
	}
	e.mergeMu.Lock()
	defer e.mergeMu.Unlock()
	st, err := e.checkMerge(ctx, goalID)
	if err != nil {
		return GoalMerge{}, err
	}
	if req.SourceHead != st.sourceHead {
		return GoalMerge{}, mergeRefused("QA's result changed since the confirmation: review it again")
	}
	if req.TargetHead != st.targetHead {
		return GoalMerge{}, mergeRefused("%s moved since the confirmation (now at %s): review it again", st.target,
			short(st.targetHead))
	}
	sha, _, err := e.git.SquashCommit(ctx, st.repo, st.targetHead, st.sourceHead, withGoalTrailer(msg, goalID))
	if errors.Is(err, git.ErrMergeConflict) {
		return GoalMerge{}, mergeRefused("the merge conflicts: review it again")
	}
	if err != nil {
		return GoalMerge{}, err
	}
	if st.checkedOut {
		err = e.git.FastForwardWorktree(ctx, st.worktreeOfTarget, sha)
		if err != nil {
			return GoalMerge{}, mergeRefused("git refused to update %s in your working tree: %s", st.target, gitReason(err))
		}
	} else if err = e.git.MoveBranch(ctx, st.repo, st.target, sha, st.targetHead); errors.Is(err, git.ErrBranchMoved) {
		// Residual (review): a checkout of the target in the instant between
		// the worktree check and this compare-and-swap is not detected.
		return GoalMerge{}, mergeRefused("%s moved during the merge: review it again", st.target)
	} else if err != nil {
		return GoalMerge{}, err
	}
	// The branch has moved: the record must follow, or Push and the archive
	// would not know (review finding). A few tries, then a loud log line.
	params := db.InsertGoalMergeParams{GoalID: goalID, QaRunID: sql.NullInt64{Int64: st.run.ID, Valid: true},
		Target: st.target, SourceHead: st.sourceHead, TargetBefore: st.targetHead, CommitSha: sha, Message: msg, CreatedAt: now()}
	var row db.GoalMerge
	for try := 0; try < 3; try++ {
		if row, err = e.q.InsertGoalMerge(context.WithoutCancel(ctx), params); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		e.log.Error("goal merged but not recorded", "task_id", goalID, "target", st.target, "commit", sha,
			"previous", st.targetHead, "source", st.sourceHead, "err", err)
		return GoalMerge{}, fmt.Errorf("merged into %s as %s, but recording it failed: %w", st.target, short(sha), err)
	}
	m := goalMergeFromRow(row)
	e.log.Info("goal merged", "task_id", goalID, "target", st.target, "commit", sha, "previous", st.targetHead,
		"source", st.sourceHead, "qa_run", st.run.ID, "checked_out", st.checkedOut)
	e.publish(Update{Type: "goal_merge", GoalMerge: &m})
	return m, nil
}

// PushGoal pushes the goal's latest merge commit to its target branch on the
// project's remote (ADR 0055): never forced; the remote refuses a branch that
// moved there.
func (e *Engine) PushGoal(ctx context.Context, goalID string) (GoalMerge, error) {
	if _, err := e.getGoal(ctx, goalID); err != nil {
		return GoalMerge{}, err
	}
	// The push runs outside mergeMu (a slow remote must not block merges);
	// recording it is a compare-and-swap.
	row, err := e.q.LatestGoalMerge(ctx, goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalMerge{}, mergeRefused("the goal has not been merged; merge it first")
	}
	if err != nil {
		return GoalMerge{}, err
	}
	m := goalMergeFromRow(row)
	if m.PushedAt != nil {
		return GoalMerge{}, mergeRefused("already pushed to %s", m.PushRemote)
	}
	remote := e.pushRemote()
	if _, err := e.git.RemoteURL(ctx, e.project.RepoPath, remote); err != nil {
		return GoalMerge{}, mergeRefused("the repository has no remote %q (the project's github_remote)", remote)
	}
	if head, err := e.git.RevParse(ctx, e.project.RepoPath, "refs/heads/"+m.Target); err != nil || !e.isAncestor(ctx, m.Commit, head) {
		return GoalMerge{}, mergeRefused("%s no longer contains the merge %s", m.Target, short(m.Commit))
	}
	// Exactly the merge the user confirmed (security review): later commits on
	// the target are not pushed with it.
	if err := e.git.PushCommit(ctx, e.project.RepoPath, remote, m.Commit, m.Target); err != nil {
		e.log.Warn("goal push failed", "task_id", goalID, "target", m.Target, "remote", remote, "err", err)
		return GoalMerge{}, mergeRefused("git push %s %s failed: %s", remote, m.Target, gitReason(err))
	}
	row, err = e.q.MarkGoalMergePushed(context.WithoutCancel(ctx), db.MarkGoalMergePushedParams{PushedAt: nullTime(now()),
		PushRemote: remote, ID: m.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return GoalMerge{}, mergeRefused("already pushed to %s", remote) // a concurrent push recorded it first
	}
	if err != nil {
		return GoalMerge{}, fmt.Errorf("pushed, but recording it failed: %w", err)
	}
	m = goalMergeFromRow(row)
	e.log.Info("goal pushed", "task_id", goalID, "target", m.Target, "remote", remote, "commit", m.Commit)
	e.publish(Update{Type: "goal_merge", GoalMerge: &m})
	return m, nil
}

// maxPreviewPaths bounds the changed paths the merge preview lists.
const maxPreviewPaths = 200

func firstN(s []string, n int) []string {
	if s == nil {
		return []string{}
	}
	return s[:min(len(s), n)]
}

// sensitivePrefixes are paths whose change runs code outside the review: CI
// pipelines and git or package-manager hooks (security review, Phase 17).
var sensitivePrefixes = []string{".github/", ".gitlab-ci.yml", ".gitlab/", ".circleci/", ".buildkite/", "Jenkinsfile",
	".githooks/", ".husky/", ".pre-commit-config.yaml", ".gitattributes", ".gitmodules", ".devcontainer/", ".vscode/tasks.json",
	".claude/", ".codex/", ".mcp.json", "CLAUDE.md", "AGENTS.md"}

// sensitivePaths lists the changed paths a reviewer should read before merging.
func sensitivePaths(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		for _, pre := range sensitivePrefixes {
			if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// redactURL drops credentials from a remote URL shown to paired devices.
func redactURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.User == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return u // scp-like (git@host:path), ssh:// or a path: a user name, no secret
	}
	parsed.User = nil
	return parsed.String()
}

// gitReason is git's own message of a failed command: its stderr's last
// meaningful lines, bounded.
func gitReason(err error) string {
	var ge *git.Error
	msg := err.Error()
	if errors.As(err, &ge) && strings.TrimSpace(ge.Stderr) != "" {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(ge.Stderr), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "hint:") {
				lines = append(lines, l)
			}
		}
		msg = strings.Join(lines, " ")
	}
	return truncateRunes(msg, 400)
}
