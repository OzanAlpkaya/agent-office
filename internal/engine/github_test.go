package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/github"
	"github.com/OzanAlpkaya/agent-office/internal/github/githubtest"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/review"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// ghFixture is an engine with the GitHub loop on: a local bare repository as
// the push remote and the GitHub API fake.
type ghFixture struct {
	f    *fixture
	e    *Engine
	log  *updateLog
	srv  *githubtest.Server
	bare string
	team mcp.Team
}

func newGHFixture(t *testing.T, tweak func(o *GitHubOptions)) *ghFixture {
	t.Helper()
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 4
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	gitIn(t, f.repo, "config", "user.name", "Test User")
	gitIn(t, f.repo, "config", "user.email", "test@example.com")
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, f.repo, "init", "-q", "--bare", bare)
	gitIn(t, f.repo, "remote", "add", "origin", bare)
	gitIn(t, f.repo, "push", "-q", "origin", "main")

	srv := githubtest.New(t, "octo", "shelf")
	srv.HeadOf = func(branch string) string {
		out, err := os.ReadFile(filepath.Join(bare, "refs", "heads", branch))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "github-token")
	if err := os.WriteFile(tok, []byte(githubtest.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := github.New(github.Options{Repo: github.Repo{Owner: "octo", Name: "shelf"}, TokenFile: tok,
		BaseURL: srv.BaseURL(), Backoff: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	g := &GitHubOptions{Client: client, Remote: "origin", Base: "main", Reviewer: review.NameInternal, Rounds: 2,
		PollFast: 50 * time.Millisecond, PollSlow: 200 * time.Millisecond, SlotWait: 5 * time.Second}
	if tweak != nil {
		tweak(g)
	}
	f.opts.GitHub = g
	e := f.engine(t)
	return &ghFixture{f: f, e: e, log: record(t, e), srv: srv, bare: bare, team: e.Team()}
}

// worker starts a host worker on a new task, lets it commit and report done.
func (gf *ghFixture) reportedWorker(t *testing.T, title, summary string) (Task, AgentInfo) {
	t.Helper()
	ctx := context.Background()
	task, err := gf.e.CreateTask(ctx, TaskInput{Title: title, Description: "Fixes #7 for @octocat"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := gf.e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	gf.log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	typeLine(t, gf.e, w.ID, "commit")
	screen(t, gf.e, w.ID, "\ncommitted\n")
	if _, err := gf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: summary}); err != nil {
		t.Fatal(err)
	}
	return task, w
}

func (gf *ghFixture) waitPR(t *testing.T, d time.Duration, ok func(p *PullRequest) bool) PullRequest {
	t.Helper()
	u := gf.log.wait(t, d, func(u Update) bool { return u.Type == "pull_request" && ok(u.PullRequest) })
	return *u.PullRequest
}

func (gf *ghFixture) reviewer(t *testing.T, n int) AgentInfo {
	t.Helper()
	seen := 0
	u := gf.log.wait(t, 10*time.Second, func(u Update) bool {
		if u.Type == "agent" && u.Agent.Role == RoleReviewer && u.Agent.State == string(hooks.Starting) {
			seen++
			return seen == n
		}
		return false
	})
	gf.log.wait(t, 5*time.Second, agentState(u.Agent.ID, hooks.Idle))
	return *u.Agent
}

// submit is a reviewer's submit_review followed by the end of its turn.
func (gf *ghFixture) submit(t *testing.T, r AgentInfo, in mcp.SubmitReviewInput) mcp.SubmitReviewOutput {
	t.Helper()
	out, err := gf.team.SubmitReview(context.Background(), r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	typeLine(t, gf.e, r.ID, "stop")
	return out
}

func remoteHead(t *testing.T, bare, branch string) string {
	t.Helper()
	return gitIn(t, bare, "rev-parse", "refs/heads/"+branch)
}

// TestGitHubLoop is the Phase 5 DoD with fakes: a reported task becomes a
// pull request; the internal reviewer reviews it; its feedback relaunches the
// original worker, whose new commit updates the same pull request; the next
// review approves; a human merge moves the task to done; nothing merges.
func TestGitHubLoop(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, w := gf.reportedWorker(t, "Add tags", "Added tags. Thanks @octocat, this fixes #3.")

	p := gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	if p.Number != 1 || p.TaskID != task.ID || p.AgentID != w.ID || p.Branch != w.Branch || p.Base != "main" {
		t.Fatalf("pull request %+v", p)
	}
	head := gitIn(t, gf.f.repo, "rev-parse", w.Branch)
	if p.Head != head || remoteHead(t, gf.bare, w.Branch) != head {
		t.Fatalf("pushed head %s, remote %s, want %s", p.Head, remoteHead(t, gf.bare, w.Branch), head)
	}
	gh, _ := gf.srv.PR(1)
	if gh.Title != "Add tags" || gh.Base != "main" || strings.Contains(gh.Body, "@octocat") ||
		strings.Contains(gh.Body, "fixes #3") || strings.Contains(gh.Body, "Fixes #7") ||
		!strings.Contains(gh.Body, "never merges") {
		t.Fatalf("PR body not neutralized or incomplete:\n%s", gh.Body)
	}
	if got, _ := gf.e.Get(w.ID); got.State == string(hooks.Exited) {
		t.Fatal("a top-level worker keeps running after report_done")
	}
	if err := gf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}

	// The internal reviewer starts on the PR head, on its own branch, and
	// does not change the task's status.
	r := gf.reviewer(t, 1)
	if !strings.HasPrefix(r.Branch, "agent-office/review-pr1-") || r.TaskID != task.ID {
		t.Fatalf("reviewer %+v", r)
	}
	if got := gitIn(t, r.WorktreePath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("reviewer at %s, want %s", got, head)
	}
	scr := screen(t, gf.e, r.ID, "PROMPT:")
	if !strings.Contains(scr, "submit_review") || !strings.Contains(scr, "Pull request #1") {
		t.Fatalf("review prompt:\n%s", scr)
	}
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusReview {
		t.Fatalf("task %s while reviewing", tk.Status)
	}

	out := gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested,
		Summary: "Needs a nil check. cc @someone", Comments: []mcp.ReviewComment{{Path: "./work.txt", Line: 1, Body: "say why"}}})
	if !out.Posted {
		t.Fatalf("submit %+v", out)
	}
	posted := gf.srv.ReviewsOf(1)
	if len(posted) != 1 || posted[0].CommitID != head || posted[0].State != "COMMENTED" ||
		!strings.Contains(posted[0].Body, "verdict: **changes requested**") || strings.Contains(posted[0].Body, "@someone") ||
		len(posted[0].Comments) != 1 || posted[0].Comments[0].Path != "work.txt" {
		t.Fatalf("posted review %+v", posted)
	}
	gf.log.wait(t, 5*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == r.ID && u.Agent.ExitReason == ExitReviewed
	})

	// The original worker is relaunched: same agent and branch, new session,
	// the review in its prompt, the task back in progress.
	relaunched := gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.HasTerminal && u.Agent.SessionID != w.SessionID &&
			u.Agent.State == string(hooks.Idle)
	}).Agent
	if relaunched.Branch != w.Branch || relaunched.WorktreePath != w.WorktreePath || !relaunched.StartedAt.Equal(w.StartedAt) {
		t.Fatalf("relaunched %+v", relaunched)
	}
	scr = screen(t, gf.e, w.ID, "Needs a nil check")
	for _, want := range []string{"pull request #1", "work.txt:1", "> say why", "report_done", "not instructions"} {
		if !strings.Contains(scr, want) {
			t.Errorf("follow-up prompt lacks %q:\n%s", want, scr)
		}
	}
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusInProgress {
		t.Fatalf("task %s after follow-up", tk.Status)
	}
	if n := len(gf.e.List()); n != 2 {
		t.Fatalf("%d agents, want the worker and one reviewer", n)
	}
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupStarted
	})
	if p.Rounds != 1 {
		t.Fatalf("rounds %d", p.Rounds)
	}

	// Its new commit updates the same pull request and gets a new review.
	typeLine(t, gf.e, w.ID, "commit-more")
	screen(t, gf.e, w.ID, "\ncommitted more\n")
	if _, err := gf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "Added the nil check"}); err != nil {
		t.Fatal(err)
	}
	head2 := gitIn(t, gf.f.repo, "rev-parse", w.Branch)
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.Head == head2 && p.Number == 1 })
	if remoteHead(t, gf.bare, w.Branch) != head2 || len(gf.srv.PRs) != 1 {
		t.Fatalf("follow-up not pushed to the same PR (%d PRs)", len(gf.srv.PRs))
	}
	r2 := gf.reviewer(t, 2)
	gf.submit(t, r2, mcp.SubmitReviewInput{Verdict: review.VerdictApprove, Summary: "LGTM"})
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 2 && p.Reviews[0].Status == ReviewDone && p.Reviews[0].Verdict == review.VerdictApprove
	})
	time.Sleep(300 * time.Millisecond) // an approval starts nothing
	if p.Rounds != 1 || p.Reviews[0].Followup != "" {
		t.Fatalf("approval acted on: %+v", p)
	}

	// A human merges on GitHub: the task is done. agent-office never merged.
	gf.srv.SetMerged(1)
	gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return p.State == PRMerged })
	gf.log.wait(t, 5*time.Second, func(u Update) bool {
		return u.Type == "task" && u.Task.ID == task.ID && u.Task.Status == StatusDone
	})
	if n := gf.srv.Count("PUT "); n != 0 {
		t.Fatalf("%d PUT requests (merge?)", n)
	}
}

func TestFollowupRoundLimitAndHumanReviews(t *testing.T) {
	gf := newGHFixture(t, func(o *GitHubOptions) { o.Rounds = 0 })
	ctx := context.Background()
	_, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	if err := gf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	r := gf.reviewer(t, 1)
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "fix it"})
	p := gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupSkipped
	})
	if !strings.Contains(p.Reviews[0].Error, "round limit") {
		t.Fatalf("skipped review %+v", p.Reviews[0])
	}

	// A person's review on GitHub is recorded and never acted on by itself.
	head := p.Head
	id := gf.srv.AddReview(1, "ozan-human", "CHANGES_REQUESTED", "Please rename it", head,
		githubtest.Comment{Path: "work.txt", Line: 1, Body: "name"})
	gf.srv.AddReview(1, "dependabot[bot]", "COMMENTED", "bot noise", head)
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 2 && p.Reviews[0].Reviewer == review.NameHuman
	})
	hr := p.Reviews[0]
	if hr.Verdict != review.VerdictChangesRequested || hr.Summary != "Please rename it" || len(hr.Comments) != 1 ||
		hr.Followup != "" {
		t.Fatalf("human review %+v", hr)
	}
	time.Sleep(300 * time.Millisecond)
	if got, _ := gf.e.Get(w.ID); got.HasTerminal && got.State != string(hooks.Exited) {
		t.Fatal("a human review relaunched the worker by itself")
	}
	if rs, _ := gf.e.PullRequests(ctx); len(rs[0].Reviews) != 2 || gf.srv.Count("GET /repos/octo/shelf/pulls/1/reviews/"+
		jsonID(id)) == 0 {
		t.Fatalf("reviews %+v", rs[0].Reviews)
	}

	// "Send to worker" sends it, beyond the limit.
	if _, err := gf.e.SendToWorker(ctx, p.ID, hr.ID); err != nil {
		t.Fatal(err)
	}
	gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.SessionID != w.SessionID && u.Agent.State == string(hooks.Idle)
	})
	screen(t, gf.e, w.ID, "a person on GitHub")
	if _, err := gf.e.SendToWorker(ctx, p.ID, hr.ID); err == nil {
		t.Fatal("a review sent twice")
	}
}

func jsonID(id int64) string { b, _ := json.Marshal(id); return string(b) }

func TestRunningWorkerGetsReviewAsMessage(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	r := gf.reviewer(t, 1)
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "rename x"})
	gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupMessaged })
	msgs := messagesTo(t, gf.e, w.ID)
	if len(msgs) != 1 || msgs[0].From != "" || !strings.Contains(msgs[0].Body, "rename x") {
		t.Fatalf("messages %+v", msgs)
	}
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusInProgress {
		t.Fatalf("task %s", tk.Status)
	}
	if got, _ := gf.e.Get(w.ID); got.SessionID != w.SessionID {
		t.Fatal("a running worker was relaunched")
	}
}

func TestReviewWaitsForASlot(t *testing.T) {
	gf := newGHFixture(t, nil)
	gf.e.opts.MaxAgents = 1
	_, w := gf.reportedWorker(t, "Add tags", "done")
	p := gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	time.Sleep(300 * time.Millisecond)
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 })
	if p.Reviews[0].Status != ReviewQueued {
		t.Fatalf("review %+v with all slots busy", p.Reviews[0])
	}
	if err := gf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	r := gf.reviewer(t, 1)
	gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Status == ReviewRunning && p.Reviews[0].AgentID == r.ID
	})

	// A reviewer that ends without submitting fails its review.
	if err := gf.e.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Status == ReviewFailed && strings.Contains(p.Reviews[0].Error, "without submitting")
	})
	// "Request review" asks again.
	if _, err := gf.e.RequestReview(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	gf.reviewer(t, 2)
	if _, err := gf.e.RequestReview(context.Background(), p.ID); err == nil {
		t.Fatal("a second review requested while one runs")
	}
}

func TestSubmitReviewAuthorization(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	_, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	r := gf.reviewer(t, 1)
	ok := mcp.SubmitReviewInput{Verdict: review.VerdictComment, Summary: "fine"}
	if _, err := gf.team.SubmitReview(ctx, w.ID, ok); !isUser(err, ErrForbidden) {
		t.Fatalf("worker submitted a review: %v", err)
	}
	for _, in := range []mcp.SubmitReviewInput{
		{Verdict: "lgtm", Summary: "x"},
		{Verdict: review.VerdictApprove, Summary: " "},
		{Verdict: review.VerdictApprove, Summary: "x", Comments: []mcp.ReviewComment{{Path: "../etc/passwd", Line: 1, Body: "b"}}},
		{Verdict: review.VerdictApprove, Summary: "x", Comments: []mcp.ReviewComment{{Path: "/etc/passwd", Line: 1, Body: "b"}}},
		{Verdict: review.VerdictApprove, Summary: "x", Comments: []mcp.ReviewComment{{Path: "a.go", Line: 0, Body: "b"}}},
		{Verdict: review.VerdictApprove, Summary: "x", Comments: make([]mcp.ReviewComment, MaxReviewComments+1)},
	} {
		if _, err := gf.team.SubmitReview(ctx, r.ID, in); !isUser(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", in, err)
		}
	}
	for _, call := range []func() error{
		func() error { _, err := gf.team.ReportDone(ctx, r.ID, mcp.ReportDoneInput{Summary: "x"}); return err },
		func() error {
			_, err := gf.team.SendMessage(ctx, r.ID, mcp.SendMessageInput{To: w.ID, Body: "x"})
			return err
		},
		func() error { _, err := gf.team.CreateTask(ctx, r.ID, mcp.CreateTaskInput{Title: "x"}); return err },
	} {
		if err := call(); !isUser(err, ErrForbidden) {
			t.Errorf("reviewer allowed: %v", err)
		}
	}
	if _, err := gf.team.SubmitReview(ctx, r.ID, ok); err != nil {
		t.Fatal(err)
	}
	if _, err := gf.team.SubmitReview(ctx, r.ID, ok); !isUser(err, ErrInvalid) {
		t.Fatalf("second submit: %v", err)
	}
	if n := len(gf.srv.ReviewsOf(1)); n != 1 {
		t.Fatalf("%d reviews posted", n)
	}
}

// Review posting fails on GitHub: the review still counts, with the error.
func TestReviewNotPostedStillCounts(t *testing.T) {
	gf := newGHFixture(t, nil)
	_, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	_ = gf.e.Stop(w.ID)
	r := gf.reviewer(t, 1)
	gf.srv.Mu.Lock()
	gf.srv.Fail["create_review"] = []int{502}
	gf.srv.Mu.Unlock()
	if out := gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "x"}); out.Posted {
		t.Fatalf("%+v", out)
	}
	p := gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupStarted
	})
	if !strings.Contains(p.Reviews[0].Error, "not posted") {
		t.Fatalf("review %+v", p.Reviews[0])
	}
}

func TestPushFailureIsShown(t *testing.T) {
	gf := newGHFixture(t, nil)
	if err := os.RemoveAll(gf.bare); err != nil {
		t.Fatal(err)
	}
	gf.reportedWorker(t, "Add tags", "done")
	p := gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.Error != "" })
	if p.State != PRPending || !strings.HasPrefix(p.Error, "push:") || len(gf.srv.PRs) != 0 {
		t.Fatalf("pull request %+v", p)
	}
}

// ADR 0014: flagged commits are not pushed until the user accepts them.
func TestFlaggedCommitsBlockPush(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, err := gf.e.CreateTask(ctx, TaskInput{Title: "Add tags"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := gf.e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	gf.log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	typeLine(t, gf.e, w.ID, "commit") // author t <t@example.com>, no trailer
	screen(t, gf.e, w.ID, "\ncommitted\n")
	// Pretend it is a sandboxed agent whose branch was imported.
	gf.e.mu.Lock()
	a := gf.e.agents[w.ID]
	a.info.Sandbox = SandboxPodman
	a.info.LastImport = &ImportInfo{Outcome: "created", Branch: w.Branch}
	gf.e.mu.Unlock()
	t.Cleanup(func() {
		gf.e.mu.Lock()
		a.info.Sandbox = SandboxHost
		gf.e.mu.Unlock()
	})

	_, err = gf.e.OpenPullRequest(ctx, task.ID)
	if !errors.Is(err, ErrFlagged) {
		t.Fatalf("want ErrFlagged, got %v", err)
	}
	p := gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Flagged) == 1 })
	if p.State != PRPending || !strings.Contains(p.Flagged[0].Problem, "author is t <t@example.com>") {
		t.Fatalf("pull request %+v", p)
	}
	if _, err := os.Stat(filepath.Join(gf.bare, "refs", "heads", w.Branch)); err == nil {
		t.Fatal("flagged branch pushed")
	}
	if _, err := gf.e.AcceptFlagged(ctx, p.ID, []string{"0000000000000000000000000000000000000000"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted an unknown commit: %v", err)
	}
	p2, err := gf.e.AcceptFlagged(ctx, p.ID, []string{p.Flagged[0].Commit})
	if err != nil || p2.State != PROpen || len(p2.Flagged) != 0 {
		t.Fatalf("after accepting: %+v %v", p2, err)
	}
	if remoteHead(t, gf.bare, w.Branch) != p.Flagged[0].Commit {
		t.Fatal("accepted branch not pushed")
	}

	// A diverged (rewritten) import is never pushed automatically.
	gf.e.mu.Lock()
	a.info.LastImport = &ImportInfo{Outcome: "diverged", Branch: w.Branch}
	gf.e.mu.Unlock()
	if _, err := gf.e.submitPR(ctx, w.ID, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("diverged import pushed automatically: %v", err)
	}
}

// Restart: an internal review whose reviewer died with the daemon is queued
// again, once.
func TestRestartRequeuesReview(t *testing.T) {
	gf := newGHFixture(t, nil)
	_, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	_ = gf.e.Stop(w.ID)
	r := gf.reviewer(t, 1)
	gf.e.mu.Lock()
	gf.e.agents[r.ID].reviewID = 0 // the daemon "dies": nothing records the reviewer's end
	gf.e.mu.Unlock()
	gf.e.Shutdown()

	g := *gf.f.opts.GitHub
	g.PollFast = time.Hour
	gf.f.opts.GitHub = &g
	e2 := gf.f.engine(t)
	rows, err := e2.q.ListReviewsByStatus(context.Background(), db.ListReviewsByStatusParams{ProjectID: e2.project.ID,
		Status: ReviewQueued})
	if err != nil || len(rows) != 1 || rows[0].AgentID.Valid || rows[0].Attempts != 1 {
		t.Fatalf("queued reviews after restart %+v %v", rows, err)
	}
}

func TestReviewPath(t *testing.T) {
	ok := map[string]string{"a.go": "a.go", "./internal/x.go": "internal/x.go", "a/../b.go": "b.go"}
	for in, want := range ok {
		if got, err := reviewPath(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "/abs", "..", "../x", "a/../../x", "a\x00b", `a\b`, strings.Repeat("a", 301)} {
		if _, err := reviewPath(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
