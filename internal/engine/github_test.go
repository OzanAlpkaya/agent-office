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
	g, srv, bare := withGitHub(t, f)
	if tweak != nil {
		tweak(g)
	}
	e := f.engine(t)
	return &ghFixture{f: f, e: e, log: record(t, e), srv: srv, bare: bare, team: e.Team()}
}

// withGitHub turns the GitHub loop on for f: a local bare repository as the
// push remote and the GitHub API fake.
func withGitHub(t *testing.T, f *fixture) (*GitHubOptions, *githubtest.Server, string) {
	t.Helper()
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
		PollFast: 50 * time.Millisecond, PollSlow: 200 * time.Millisecond, SlotWait: 5 * time.Second,
		FollowUpQuiet: 100 * time.Millisecond}
	f.opts.GitHub = g
	return g, srv, bare
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
		if u.Type == "agent" && u.Agent.Kind == KindReviewer && u.Agent.State == string(hooks.Starting) {
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

// A worker that still runs (top-level workers keep their session after
// report_done) is ended once idle and relaunched with the review in its
// prompt; a team message would be read as mere information (ADR 0022).
func TestRunningWorkerIsRelaunched(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	typeLine(t, gf.e, w.ID, "busy") // working: the follow-up waits
	gf.log.wait(t, 5*time.Second, agentState(w.ID, hooks.Working))
	r := gf.reviewer(t, 1)
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "rename x"})
	time.Sleep(500 * time.Millisecond)
	if got, _ := gf.e.Get(w.ID); got.SessionID != w.SessionID || got.State != string(hooks.Working) {
		t.Fatalf("a working worker was ended: %+v", got)
	}
	gf.e.mu.Lock()
	gf.e.agents[w.ID].lastInput = time.Time{} // the user stopped typing long ago
	gf.e.mu.Unlock()
	if err := gf.e.Input(w.ID, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.ExitReason == ExitFollowUp
	})
	gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.SessionID != w.SessionID && u.Agent.State == string(hooks.Idle)
	})
	screen(t, gf.e, w.ID, "rename x")
	gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupStarted })
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusInProgress {
		t.Fatalf("task %s", tk.Status)
	}
	// ADR 0018: a message left undelivered by the previous session never
	// reaches the new one.
	if _, err := gf.e.q.InsertMessage(ctx, db.InsertMessageParams{ProjectID: gf.e.project.ID, ToAgentID: w.ID,
		Body: "old session only", CreatedAt: w.StartedAt}); err != nil {
		t.Fatal(err)
	}
	typeLine(t, gf.e, w.ID, "turn")
	time.Sleep(500 * time.Millisecond)
	if msgs := messagesTo(t, gf.e, w.ID); len(msgs) != 1 || msgs[0].DeliveredAt != nil {
		t.Fatalf("the old session's message was delivered to the new one: %+v", msgs)
	}
	if _, err := gf.e.sendMessage(ctx, "", w.ID, "hello again", false); err != nil {
		t.Fatal(err)
	}
	screen(t, gf.e, w.ID, "hello again")
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

// ADR 0020: a branch that changes .github/ (workflows run with the
// repository's secrets on push) is not pushed until the user accepts it.
func TestWorkflowChangesBlockPush(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, _ := gf.e.CreateTask(ctx, TaskInput{Title: "Add CI"})
	w, err := gf.e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	gf.log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	if err := os.MkdirAll(filepath.Join(w.WorktreePath, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.WorktreePath, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.WorktreePath, "add", ".")
	gitIn(t, w.WorktreePath, "commit", "-q", "-m", "ci: add workflow")
	_, err = gf.e.OpenPullRequest(ctx, task.ID)
	if !errors.Is(err, ErrFlagged) {
		t.Fatalf("want ErrFlagged, got %v", err)
	}
	p := gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Flagged) == 1 })
	if !strings.Contains(p.Flagged[0].Problem, ".github/workflows/ci.yml") || len(gf.srv.PRs) != 0 {
		t.Fatalf("pull request %+v", p)
	}
	if p, err := gf.e.AcceptFlagged(ctx, p.ID, []string{p.Flagged[0].Commit}); err != nil || p.State != PROpen {
		t.Fatalf("after accepting: %+v %v", p, err)
	}
}

func TestReviewsOfOldHeadsAreSuperseded(t *testing.T) {
	gf := newGHFixture(t, nil)
	gf.e.opts.MaxAgents = 1 // the worker keeps the only slot: the review stays queued
	ctx := context.Background()
	_, w := gf.reportedWorker(t, "Add tags", "done")
	p := gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 })
	typeLine(t, gf.e, w.ID, "commit-more")
	screen(t, gf.e, w.ID, "\ncommitted more\n")
	if _, err := gf.e.OpenPullRequest(ctx, p.TaskID); err != nil {
		t.Fatal(err)
	}
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 2 })
	if old := p.Reviews[1]; old.Status != ReviewFailed || !strings.Contains(old.Error, "superseded") {
		t.Fatalf("old review %+v", old)
	}
	if p.Reviews[0].Head != p.Head || p.Reviews[0].Status != ReviewQueued {
		t.Fatalf("new review %+v", p.Reviews[0])
	}
}

func TestReviewCapAndReviewerTimeout(t *testing.T) {
	gf := newGHFixture(t, func(o *GitHubOptions) { o.ReviewTimeout = 300 * time.Millisecond })
	ctx := context.Background()
	_, w := gf.reportedWorker(t, "Add tags", "done")
	p := gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	_ = gf.e.Stop(w.ID)
	// A reviewer that never submits is ended and its review fails.
	r := gf.reviewer(t, 1)
	gf.log.wait(t, 5*time.Second, agentState(r.ID, hooks.Exited))
	p = gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool {
		return len(p.Reviews) == 1 && p.Reviews[0].Status == ReviewFailed
	})
	if !strings.Contains(p.Reviews[0].Error, "no review submitted") && !strings.Contains(p.Reviews[0].Error, "without submitting") {
		t.Fatalf("review %+v", p.Reviews[0])
	}

	// At most MaxReviewsPerPR automatic reviews per pull request.
	for i := 0; i < MaxReviewsPerPR; i++ {
		if _, err := gf.e.q.InsertReview(ctx, db.InsertReviewParams{PrID: p.ID, Reviewer: review.NameInternal,
			Head: jsonID(int64(i)), Status: ReviewFailed, Comments: "[]", CreatedAt: now(), UpdatedAt: now()}); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := gf.e.getPR(ctx, p.ID)
	row.Head = "another-head"
	lock := gf.e.branchLock(row.Branch)
	lock.Lock()
	gf.e.requestReviewLocked(ctx, row, false)
	lock.Unlock()
	row, _ = gf.e.getPR(ctx, p.ID)
	if _, err := gf.e.q.ReviewForHead(ctx, db.ReviewForHeadParams{PrID: p.ID, Reviewer: review.NameInternal,
		Head: "another-head"}); err == nil || !strings.Contains(row.Error, "automatic reviews reached") {
		t.Fatalf("review beyond the cap requested: %q %v", row.Error, err)
	}
}

// Restart: a follow-up whose worker died with the daemon before pushing is
// offered again (Send to worker).
func TestRestartReoffersFollowup(t *testing.T) {
	gf := newGHFixture(t, nil)
	_, w := gf.reportedWorker(t, "Add tags", "done")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	_ = gf.e.Stop(w.ID)
	r := gf.reviewer(t, 1)
	mark := gf.log.mark()
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "fix"})
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 && p.Reviews[0].Followup == FollowupStarted })
	// "started" is claimed before the relaunch runs: wait for the relaunched
	// worker, so the daemon dies after the follow-up began, as the test means.
	gf.log.waitFrom(t, startWait, mark, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.State == string(hooks.Idle) && u.Agent.ExitedAt == nil
	})
	gf.e.Shutdown()
	g := *gf.f.opts.GitHub
	g.PollFast = time.Hour
	gf.f.opts.GitHub = &g
	e2 := gf.f.engine(t)
	prs, err := e2.PullRequests(context.Background())
	if err != nil || len(prs) != 1 || prs[0].Reviews[0].Followup != FollowupRefused ||
		!strings.Contains(prs[0].Reviews[0].Error, "restarted") {
		t.Fatalf("after restart %+v %v", prs, err)
	}
}

// followupWorker reports a worker's task, has the reviewer request changes and
// waits until the original worker is relaunched with the review (ADR 0022).
func (gf *ghFixture) followupWorker(t *testing.T) (Task, AgentInfo) {
	t.Helper()
	task, w := gf.reportedWorker(t, "Add tags", "Added tags.")
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	if err := gf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	r := gf.reviewer(t, 1)
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "Add a nil check."})
	gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.HasTerminal && u.Agent.SessionID != w.SessionID &&
			u.Agent.State == string(hooks.Idle)
	})
	scr := screen(t, gf.e, w.ID, "Add a nil check.")
	if !strings.Contains(scr, "report_no_change") || !strings.Contains(scr, "refused until the branch has a new commit") {
		t.Fatalf("follow-up prompt does not explain the two ways to answer:\n%s", scr)
	}
	return task, w
}

// In a follow-up round, report_done needs a new commit; report_no_change
// answers without one, posts the reason on the pull request and starts no
// new review.
func TestFollowupNoChangeNeeded(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	task, w := gf.followupWorker(t)

	_, err := gf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "Nothing to do"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no new commit since the review of pull request #1") ||
		!strings.Contains(err.Error(), "report_no_change") {
		t.Fatalf("report_done without a new commit: %v", err)
	}
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusInProgress {
		t.Fatalf("a refused report moved the task to %s", tk.Status)
	}

	out, err := gf.team.ReportNoChange(ctx, w.ID, mcp.ReportNoChangeInput{
		Reason: "The nil check is in store.go:12 already. cc @octocat, fixes #3"})
	if err != nil || !out.Posted || out.Status != StatusReview || !strings.Contains(out.URL, "#issuecomment-") {
		t.Fatalf("report_no_change %+v %v", out, err)
	}
	comments := gf.srv.CommentsOf(1)
	if len(comments) != 1 || !strings.Contains(comments[0], "made no change") || !strings.Contains(comments[0], "> The nil check") ||
		strings.Contains(comments[0], "@octocat") || strings.Contains(comments[0], "fixes #3") {
		t.Fatalf("PR comment not posted, quoted or neutralized: %q", comments)
	}
	if tk, _ := gf.e.getTask(ctx, task.ID); tk.Status != StatusReview {
		t.Fatalf("task %s after report_no_change", tk.Status)
	}
	p := gf.waitPR(t, 5*time.Second, func(p *PullRequest) bool { return len(p.Reviews) == 1 && p.Reviews[0].ReplyURL != "" })
	if p.Reviews[0].Reply != "The nil check is in store.go:12 already. cc @octocat, fixes #3" || p.Rounds != 1 {
		t.Fatalf("review reply %+v", p.Reviews[0])
	}
	time.Sleep(300 * time.Millisecond) // the head did not move: no new review
	if n := len(gf.srv.ReviewsOf(1)); n != 1 {
		t.Fatalf("%d reviews posted, want only the first", n)
	}
	if _, err := gf.team.ReportNoChange(ctx, w.ID, mcp.ReportNoChangeInput{Reason: "again"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second answer to the same round: %v", err)
	}
	if n := len(gf.srv.CommentsOf(1)); n != 1 {
		t.Fatalf("%d comments", n)
	}
}

// report_no_change is only for a follow-up round still at the reviewed head;
// a follow-up with a new commit reports with report_done as before.
func TestReportNoChangeRefusals(t *testing.T) {
	gf := newGHFixture(t, nil)
	ctx := context.Background()
	_, w := gf.reportedWorker(t, "Add tags", "Added tags.")
	if _, err := gf.team.ReportNoChange(ctx, w.ID, mcp.ReportNoChangeInput{Reason: "no"}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "there is none open for you") {
		t.Fatalf("outside a follow-up round: %v", err)
	}
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.State == PROpen })
	if err := gf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	r := gf.reviewer(t, 1)
	gf.submit(t, r, mcp.SubmitReviewInput{Verdict: review.VerdictChangesRequested, Summary: "Add a nil check."})
	gf.log.wait(t, 10*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == w.ID && u.Agent.SessionID != w.SessionID && u.Agent.State == string(hooks.Idle)
	})
	if _, err := gf.team.ReportNoChange(ctx, r.ID, mcp.ReportNoChangeInput{Reason: "no"}); !errors.Is(err, ErrForbidden) &&
		!errors.Is(err, ErrInvalid) {
		t.Fatalf("a reviewer: %v", err)
	}
	typeLine(t, gf.e, w.ID, "commit-more")
	screen(t, gf.e, w.ID, "\ncommitted more\n")
	if _, err := gf.team.ReportNoChange(ctx, w.ID, mcp.ReportNoChangeInput{Reason: "no"}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "new commits since the review") {
		t.Fatalf("with a new commit: %v", err)
	}
	if _, err := gf.team.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "Added the nil check"}); err != nil {
		t.Fatalf("report_done with a new commit: %v", err)
	}
	head := gitIn(t, gf.f.repo, "rev-parse", w.Branch)
	gf.waitPR(t, 10*time.Second, func(p *PullRequest) bool { return p.Head == head })
	if n := len(gf.srv.CommentsOf(1)); n != 0 {
		t.Fatalf("%d comments posted", n)
	}
}
