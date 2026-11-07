package github

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/github/githubtest"
)

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-office")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "github-token")
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newClient(t *testing.T, srv *githubtest.Server) *Client {
	t.Helper()
	c, err := New(Options{Repo: Repo{Owner: srv.Owner, Name: srv.Name}, TokenFile: tokenFile(t, githubtest.Token),
		BaseURL: srv.BaseURL(), Backoff: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRepositoryAndTokenFile(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	info, err := c.Repository(context.Background())
	if err != nil || info.DefaultBranch != "main" || info.FullName != "octo/shelf" {
		t.Fatalf("repository %+v %v", info, err)
	}
	if srv.BadAuth != 0 {
		t.Fatal("token not sent")
	}

	// The token is read for every operation: a replaced file takes effect at once.
	if err := os.WriteFile(c.opts.TokenFile, []byte("another-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Repository(context.Background()); StatusCode(err) != http.StatusUnauthorized {
		t.Fatalf("want 401 with the replaced token, got %v", err)
	}
	if err := os.Chmod(c.opts.TokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = c.Repository(context.Background())
	if !errors.Is(err, ErrTokenFile) || strings.Contains(err.Error(), "another-token") {
		t.Fatalf("want ErrTokenFile without the token in it, got %v", err)
	}
}

func TestEnsurePRIsIdempotent(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	srv.HeadOf = func(string) string { return "abc123" }
	c := newClient(t, srv)
	ctx := context.Background()
	n := NewPR{Branch: "agent-office/x-1", Base: "main", Title: "Add x", Body: "body"}
	p, created, err := c.EnsurePR(ctx, n)
	if err != nil || !created || p.Number != 1 || p.Head != "abc123" || p.Branch != n.Branch || p.Base != "main" {
		t.Fatalf("create %+v %v %v", p, created, err)
	}
	p2, created, err := c.EnsurePR(ctx, n)
	if err != nil || created || p2.Number != 1 {
		t.Fatalf("second call must find the PR: %+v %v %v", p2, created, err)
	}
	if got := srv.Count("POST /repos/octo/shelf/pulls"); got != 1 {
		t.Fatalf("%d creates", got)
	}

	// A create that races with another one (422 "already exists") finds it.
	n2 := NewPR{Branch: "agent-office/y-2", Base: "main", Title: "Add y"}
	if _, _, err := c.EnsurePR(ctx, n2); err != nil {
		t.Fatal(err)
	}
	srv.Mu.Lock()
	srv.HideOpen = 1 // the first lookup misses the PR another caller just created
	srv.Mu.Unlock()
	if p, created, err := c.EnsurePR(ctx, n2); err != nil || created || p.Number != 2 {
		t.Fatalf("422 path %+v %v %v", p, created, err)
	}
}

func TestCreateIsNeverRetried(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	srv.Fail["create_pull"] = []int{502}
	_, _, err := c.EnsurePR(context.Background(), NewPR{Branch: "agent-office/x", Base: "main", Title: "x"})
	if StatusCode(err) != 502 {
		t.Fatalf("want the 502, got %v", err)
	}
	if got := srv.Count("POST /repos/octo/shelf/pulls"); got != 1 {
		t.Fatalf("POST retried: %d", got)
	}
}

func TestReadsAreRetried(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	srv.Fail["get_repository"] = []int{503, 500}
	if _, err := c.Repository(context.Background()); err != nil {
		t.Fatalf("two 5xx then success: %v", err)
	}
	srv.Fail["get_repository"] = []int{503, 503, 503}
	if _, err := c.Repository(context.Background()); StatusCode(err) != 503 {
		t.Fatalf("retries are bounded: %v", err)
	}
	srv.Fail["get_repository"] = []int{404}
	before := srv.Count("GET /repos/octo/shelf")
	if _, err := c.Repository(context.Background()); StatusCode(err) != 404 {
		t.Fatal(err)
	}
	if srv.Count("GET /repos/octo/shelf")-before != 1 {
		t.Fatal("a 4xx must not be retried")
	}
}

func TestReviewsAndPostReview(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	ctx := context.Background()
	if _, _, err := c.EnsurePR(ctx, NewPR{Branch: "agent-office/x", Base: "main", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	id := srv.AddReview(1, "coderabbitai[bot]", "COMMENTED", "Actionable comments posted: 1", "h1",
		githubtest.Comment{Path: "a.go", Line: 3, Body: "nil check"})
	rs, err := c.Reviews(ctx, 1)
	if err != nil || len(rs) != 1 || rs[0].ID != id || rs[0].User != "coderabbitai[bot]" || rs[0].CommitID != "h1" {
		t.Fatalf("reviews %+v %v", rs, err)
	}
	cs, err := c.ReviewComments(ctx, 1, id)
	if err != nil || len(cs) != 1 || cs[0] != (Comment{Path: "a.go", Line: 3, Body: "nil check"}) {
		t.Fatalf("comments %+v %v", cs, err)
	}

	posted, err := c.PostReview(ctx, 1, "h1", "Verdict: changes requested", []Comment{{Path: "a.go", Line: 3, Body: "x"}})
	if err != nil || !posted.Inline || posted.ID == 0 {
		t.Fatalf("post %+v %v", posted, err)
	}
	// Inline comments outside the diff: 422, then one body-only post.
	srv.Fail["create_review"] = []int{http.StatusUnprocessableEntity}
	posted, err = c.PostReview(ctx, 1, "h1", "Verdict: comment", []Comment{{Path: "b.go", Line: 99, Body: "far away"}})
	if err != nil || posted.Inline {
		t.Fatalf("fallback %+v %v", posted, err)
	}
	last := srv.ReviewsOf(1)[len(srv.ReviewsOf(1))-1]
	if len(last.Comments) != 0 || !strings.Contains(last.Body, "`b.go:99`: far away") || last.CommitID != "h1" {
		t.Fatalf("folded review %+v", last)
	}
	if got := srv.Count("POST /repos/octo/shelf/pulls/1/reviews"); got != 3 {
		t.Fatalf("%d review posts, want 3", got)
	}
	// Any other error is not retried: a review is never posted twice.
	srv.Fail["create_review"] = []int{502}
	if _, err := c.PostReview(ctx, 1, "h1", "x", nil); StatusCode(err) != 502 {
		t.Fatal(err)
	}
	if got := srv.Count("POST /repos/octo/shelf/pulls/1/reviews"); got != 4 {
		t.Fatalf("%d review posts, want 4", got)
	}
}

func TestChecks(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	ctx := context.Background()
	cases := []struct {
		name   string
		runs   []githubtest.CheckRun
		sts    []githubtest.Status
		denied int
		want   string
		failed []string
	}{
		{name: "none", want: ChecksNone},
		{name: "success", runs: []githubtest.CheckRun{run("test", "completed", "success"), run("lint", "completed", "skipped")},
			sts: []githubtest.Status{stat("ci/x", "success")}, want: ChecksSuccess},
		{name: "pending", runs: []githubtest.CheckRun{run("test", "in_progress", "")}, want: ChecksPending},
		{name: "failure", runs: []githubtest.CheckRun{run("test", "completed", "failure"), run("build", "in_progress", "")},
			sts: []githubtest.Status{stat("ci/y", "error")}, want: ChecksFailure, failed: []string{"test", "ci/y"}},
		{name: "runs forbidden, statuses fine", denied: 403, sts: []githubtest.Status{stat("ci/x", "success")}, want: ChecksSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.Mu.Lock()
			srv.CheckRuns["h"], srv.Statuses["h"], srv.ChecksStatus = tc.runs, tc.sts, tc.denied
			srv.Mu.Unlock()
			got, err := c.Checks(ctx, "h")
			if err != nil || got.State != tc.want || strings.Join(got.Failed, ",") != strings.Join(tc.failed, ",") {
				t.Fatalf("checks %+v %v, want %s %v", got, err, tc.want, tc.failed)
			}
		})
	}
	srv.Mu.Lock()
	srv.ChecksStatus = 403
	srv.Fail["status"] = []int{403}
	srv.Mu.Unlock()
	if got, err := c.Checks(ctx, "h"); err != nil || got.State != ChecksUnavailable {
		t.Fatalf("both forbidden: %+v %v", got, err)
	}
}

func TestRateLimit(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	reset := time.Now().Add(10 * time.Minute).Unix()
	srv.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})
	_, err := c.Repository(context.Background())
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || rl.Reset.Unix() != reset {
		t.Fatalf("want a rate-limit error with the reset time, got %v", err)
	}
}

func TestTokenOnlyToAPIOrigin(t *testing.T) {
	// go-github's WithAuthToken attaches the token to the configured origins
	// only; a request elsewhere (e.g. a redirect) goes without it.
	other := githubtest.New(t, "octo", "shelf")
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	cl, err := c.client()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cl.NewRequest(context.Background(), "GET", other.BaseURL()+"repos/octo/shelf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Do(req, nil); err == nil {
		t.Fatal("want 401 from the other origin")
	}
	if other.BadAuth != 1 {
		t.Fatalf("the token reached another origin")
	}
}

func TestParseRemote(t *testing.T) {
	ok := map[string]Repo{
		"git@github-personal:OzanAlpkaya/agent-office-playground.git": {"OzanAlpkaya", "agent-office-playground"},
		"git@github.com:octo/shelf":                                   {"octo", "shelf"},
		"ssh://git@github.com/octo/shelf.git":                         {"octo", "shelf"},
		"ssh://git@github.com:22/octo/shelf.git/":                     {"octo", "shelf"},
		"https://github.com/octo/my.repo_x.git":                       {"octo", "my.repo_x"},
	}
	for in, want := range ok {
		if got, err := ParseRemote(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "/srv/git/shelf.git", "file:///srv/shelf", "https://gitlab.com/octo/shelf",
		"https://user:pw@github.com/octo/shelf", "git@github.com:octo", "git@github.com:octo/shelf/extra",
		"git@github.com:-octo/shelf", "git@github.com:octo/..", "git@github.com:oc to/shelf"} {
		if _, err := ParseRemote(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestNeutralize(t *testing.T) {
	cases := map[string]string{
		"thanks @octocat and @octo-team":                    "thanks @‍octocat and @‍octo-team",
		"Fixes #12, closes #3":                              "F‍ixes #12, c‍loses #3",
		"resolved octo/shelf#4":                             "r‍esolved octo/shelf#4",
		"a@b.c is an email-ish":                             "a@‍b.c is an email-ish",
		"see ![x](https://evil.example/?d=secret)":          "see !\u200d[x](https://evil.example/?d=secret)",
		"prefix and suffix stay: fixture, closet, prefixes": "prefix and suffix stay: fixture, closet, prefixes",
	}
	for in, want := range cases {
		if got := Neutralize(in); got != want {
			t.Errorf("Neutralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func run(name, status, conclusion string) githubtest.CheckRun {
	return githubtest.CheckRun{Name: name, Status: status, Conclusion: conclusion}
}

func stat(context, state string) githubtest.Status {
	return githubtest.Status{Context: context, State: state}
}

// Merging stays a human action (roadmap DoD, ADR 0020): the client has no
// operation that merges, and the fake fails any test that sends one.
func TestClientCannotMerge(t *testing.T) {
	typ := reflect.TypeOf(&Client{})
	for i := 0; i < typ.NumMethod(); i++ {
		if name := strings.ToLower(typ.Method(i).Name); strings.Contains(name, "merge") {
			t.Errorf("Client has a merge operation: %s", typ.Method(i).Name)
		}
	}
}

func TestPostCommentIsNeverRetried(t *testing.T) {
	srv := githubtest.New(t, "octo", "shelf")
	c := newClient(t, srv)
	ctx := context.Background()
	pr, _, err := c.EnsurePR(ctx, NewPR{Branch: "agent-office/x", Base: "main", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.PostComment(ctx, pr.Number, "no change needed")
	if err != nil || got.ID == 0 || !strings.Contains(got.URL, "#issuecomment-") {
		t.Fatalf("%+v %v", got, err)
	}
	if cs := srv.CommentsOf(pr.Number); len(cs) != 1 || cs[0] != "no change needed" {
		t.Fatalf("comments %v", cs)
	}
	srv.Fail["create_comment"] = []int{502}
	if _, err := c.PostComment(ctx, pr.Number, "again"); StatusCode(err) != 502 {
		t.Fatalf("want the 502, got %v", err)
	}
	if n := srv.Count("POST /repos/octo/shelf/issues/1/comments"); n != 2 {
		t.Fatalf("POST retried: %d requests", n)
	}
	if _, err := c.PostComment(ctx, pr.Number, "  "); err == nil {
		t.Fatal("empty comment accepted")
	}
}
