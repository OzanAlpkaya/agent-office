package review

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/github"
	"github.com/OzanAlpkaya/agent-office/internal/github/githubtest"
)

// world is one reviewer under test plus a way to make it deliver a review.
type world struct {
	reviewer Reviewer
	// give makes the reviewer produce a review of head.
	give func(t *testing.T, pr PR, head, verdict string, comments []Comment)
}

// TestReviewerContract runs the same contract against both implementations
// (ADR 0021): pending until a review of the current head exists; then that
// review, in one shape; reviews of other heads are ignored.
func TestReviewerContract(t *testing.T) {
	worlds := map[string]func(t *testing.T) world{
		NameInternal:   internalWorld,
		NameCodeRabbit: codeRabbitWorld,
	}
	for name, mk := range worlds {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			w := mk(t)
			if w.reviewer.Name() != name {
				t.Fatalf("Name() = %q", w.reviewer.Name())
			}
			pr := PR{ID: 1, Number: 1, Branch: "agent-office/x", Base: "main", Head: "head1", AgentID: "a1"}
			if err := w.reviewer.Request(ctx, pr); err != nil {
				t.Fatal(err)
			}
			if r, ok := w.reviewer.(Internal); ok && len(r.Runner.(*fakeRunner).started) != 1 {
				t.Fatal("the internal reviewer did not start a reviewer agent")
			}
			if f, err := w.reviewer.Poll(ctx, pr); err != nil || f != nil {
				t.Fatalf("pending review: %+v %v", f, err)
			}
			w.give(t, pr, "head1", VerdictChangesRequested, []Comment{{Path: "store.go", Line: 12, Body: "check nil"}})
			f, err := w.reviewer.Poll(ctx, pr)
			if err != nil || f == nil {
				t.Fatalf("review: %+v %v", f, err)
			}
			if f.Reviewer != name || f.Head != "head1" || f.Verdict != VerdictChangesRequested || !f.Actionable() ||
				len(f.Comments) != 1 || f.Comments[0] != (Comment{Path: "store.go", Line: 12, Body: "check nil"}) {
				t.Fatalf("feedback %+v", f)
			}

			// A new head: the old review does not count for it.
			pr.Head = "head2"
			if f, err := w.reviewer.Poll(ctx, pr); err != nil || f != nil {
				t.Fatalf("stale review returned for a new head: %+v %v", f, err)
			}
			w.give(t, pr, "head2", VerdictApprove, nil)
			if f, err := w.reviewer.Poll(ctx, pr); err != nil || f == nil || f.Verdict != VerdictApprove || f.Actionable() {
				t.Fatalf("second round %+v %v", f, err)
			}
		})
	}
}

type fakeRunner struct {
	mu        sync.Mutex
	started   []PR
	submitted map[string]*Feedback // by head
}

func (r *fakeRunner) StartReviewer(_ context.Context, pr PR) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, pr)
	return nil
}

func (r *fakeRunner) Submitted(_ context.Context, pr PR) (*Feedback, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Like the engine: the latest submission for this pull request,
	// whichever head it was for; Poll must check the head.
	var last *Feedback
	for _, f := range r.submitted {
		last = f
	}
	if f, ok := r.submitted[pr.Head]; ok {
		last = f
	}
	if last == nil {
		return nil, nil
	}
	c := *last
	return &c, nil
}

func internalWorld(t *testing.T) world {
	r := &fakeRunner{submitted: map[string]*Feedback{}}
	return world{
		reviewer: Internal{Runner: r},
		give: func(t *testing.T, pr PR, head, verdict string, comments []Comment) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.submitted[head] = &Feedback{Head: head, Verdict: verdict, Summary: "s", Comments: comments}
		},
	}
}

func codeRabbitWorld(t *testing.T) world {
	srv := githubtest.New(t, "octo", "shelf")
	gh := ghClient(t, srv)
	if _, _, err := gh.EnsurePR(context.Background(), github.NewPR{Branch: "agent-office/x", Base: "main", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	return world{
		reviewer: CodeRabbit{GitHub: gh},
		give: func(t *testing.T, pr PR, head, verdict string, comments []Comment) {
			state := map[string]string{VerdictApprove: "APPROVED", VerdictComment: "COMMENTED",
				VerdictChangesRequested: "COMMENTED"}[verdict] // CodeRabbit comments; its inline comments make it actionable
			var cs []githubtest.Comment
			for _, c := range comments {
				cs = append(cs, githubtest.Comment{Path: c.Path, Line: c.Line, Body: c.Body})
			}
			srv.AddReview(pr.Number, CodeRabbitLogin, state, "Actionable comments posted", head, cs...)
		},
	}
}

func ghClient(t *testing.T, srv *githubtest.Server) *github.Client {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "github-token")
	if err := os.WriteFile(tok, []byte(githubtest.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := github.New(github.Options{Repo: github.Repo{Owner: srv.Owner, Name: srv.Name}, TokenFile: tok,
		BaseURL: srv.BaseURL(), Backoff: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// CodeRabbit's reader only trusts the bot's own reviews of the current head:
// a person's review, another bot, a pending or dismissed review do not count.
func TestCodeRabbitFilters(t *testing.T) {
	ctx := context.Background()
	srv := githubtest.New(t, "octo", "shelf")
	gh := ghClient(t, srv)
	if _, _, err := gh.EnsurePR(ctx, github.NewPR{Branch: "agent-office/x", Base: "main", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	cr := CodeRabbit{GitHub: gh}
	pr := PR{Number: 1, Head: "h1"}
	srv.AddReview(1, "someone", "CHANGES_REQUESTED", "please", "h1", githubtest.Comment{Path: "a", Line: 1, Body: "x"})
	srv.AddReview(1, "coderabbitai", "COMMENTED", "lookalike login", "h1")
	srv.AddReview(1, CodeRabbitLogin, "DISMISSED", "old", "h1")
	srv.AddReview(1, CodeRabbitLogin, "COMMENTED", "other head", "h0", githubtest.Comment{Path: "a", Line: 1, Body: "x"})
	if f, err := cr.Poll(ctx, pr); err != nil || f != nil {
		t.Fatalf("want no review, got %+v %v", f, err)
	}
	id := srv.AddReview(1, CodeRabbitLogin, "COMMENTED", "LGTM, no actionable comments", "h1")
	f, err := cr.Poll(ctx, pr)
	if err != nil || f == nil || f.Verdict != VerdictComment || f.GitHubReviewID != id || f.Summary == "" || f.URL == "" {
		t.Fatalf("plain comment review %+v %v", f, err)
	}
	srv.AddReview(1, CodeRabbitLogin, "CHANGES_REQUESTED", "request changes workflow", "h1")
	if f, err := cr.Poll(ctx, pr); err != nil || f.Verdict != VerdictChangesRequested {
		t.Fatalf("newest review wins: %+v %v", f, err)
	}
	if (CodeRabbit{GitHub: gh, Login: "other[bot]"}).Name() != NameCodeRabbit {
		t.Fatal("name")
	}
}

func TestVerdictOf(t *testing.T) {
	for state, want := range map[string]string{"CHANGES_REQUESTED": VerdictChangesRequested, "APPROVED": VerdictApprove,
		"COMMENTED": VerdictComment, "": VerdictComment} {
		if got := VerdictOf(state); got != want {
			t.Errorf("%q: %q", state, got)
		}
	}
}
