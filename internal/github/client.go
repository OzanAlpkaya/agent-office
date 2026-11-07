// Package github is agent-office's GitHub integration (ADR 0020): pull requests,
// reviews and checks through go-github, with a token read from a user-only
// file for every operation. It deliberately has no merge operation: merging
// stays a human action.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// TokenFileEnv overrides the token file's path. It holds a path, never the token.
const TokenFileEnv = "AGENT_OFFICE_GITHUB_TOKEN_FILE"

// ErrTokenFile marks every problem with the token file. Messages name the
// file and the problem, never its contents.
var ErrTokenFile = errors.New("github: token file")

// ErrRateLimited is returned (wrapped in *RateLimitError) when GitHub's rate
// limit is exhausted.
var ErrRateLimited = errors.New("github: rate limited")

// RateLimitError says until when GitHub refuses requests.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limited until %s", e.Reset.UTC().Format(time.RFC3339))
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// DefaultTokenFile returns $AGENT_OFFICE_GITHUB_TOKEN_FILE, or else
// $XDG_CONFIG_HOME/agent-office/github-token (~/.config/agent-office/github-token).
func DefaultTokenFile() (string, error) {
	if p := os.Getenv(TokenFileEnv); p != "" {
		return filepath.Abs(p)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTokenFile, err)
	}
	return filepath.Join(dir, "agent-office", "github-token"), nil
}

// Options configures a Client.
type Options struct {
	Repo      Repo
	TokenFile string            // read for every operation (ADR 0020)
	BaseURL   string            // API root; empty: https://api.github.com/ (tests point it at a fake)
	Transport http.RoundTripper // nil: http.DefaultTransport
	Timeout   time.Duration     // per request; default 30 s
	Retries   int               // extra attempts for GETs on network errors and 5xx; default 2
	Backoff   time.Duration     // first retry delay, doubled each time; default 500 ms
	Logger    *slog.Logger
}

// Client talks to one repository.
type Client struct {
	opts Options
	log  *slog.Logger
}

// New validates opts. It does not read the token or call GitHub.
func New(opts Options) (*Client, error) {
	if err := opts.Repo.validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(opts.TokenFile) {
		return nil, fmt.Errorf("github: TokenFile must be an absolute path, got %q", opts.TokenFile)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	} else if opts.Retries == 0 {
		opts.Retries = 2
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 500 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Transport == nil {
		opts.Transport = http.DefaultTransport
	}
	return &Client{opts: opts, log: opts.Logger.With("repo", opts.Repo.String())}, nil
}

// Repo returns the client's repository.
func (c *Client) Repo() Repo { return c.opts.Repo }

// client builds a go-github client with a freshly read token, used for one
// operation and dropped, so the token is not kept in memory (ADR 0020).
func (c *Client) client() (*gh.Client, error) {
	tok, err := secret.ReadFile(c.opts.TokenFile, ErrTokenFile)
	if err != nil {
		return nil, err
	}
	opts := []gh.ClientOptionsFunc{
		gh.WithAuthToken(tok.Reveal()), // sent only to the API origin
		gh.WithHTTPClient(&http.Client{Transport: c.opts.Transport}),
		gh.WithTimeout(c.opts.Timeout),
		gh.WithUserAgent("agent-office"),
	}
	if c.opts.BaseURL != "" {
		u := c.opts.BaseURL
		opts = append(opts, gh.WithURLs(&u, &u))
	}
	return gh.NewClient(opts...)
}

// do runs one API call. Reads (idempotent) are retried on network errors and
// 5xx with backoff; writes never are (ADR 0020). Logs carry the operation,
// status and duration, never headers or bodies.
func (c *Client) do(ctx context.Context, op string, idempotent bool, fn func(*gh.Client) (*gh.Response, error)) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	delay := c.opts.Backoff
	for attempt := 0; ; attempt++ {
		start := time.Now()
		resp, err := fn(cl)
		status := 0
		if resp != nil && resp.Response != nil {
			status = resp.StatusCode
		}
		d := time.Since(start).Round(time.Millisecond)
		if err == nil {
			c.log.Debug("github", "op", op, "status", status, "duration", d)
			return nil
		}
		err = mapError(err)
		if !idempotent || attempt >= c.opts.Retries || !retryable(err, status) {
			c.log.Warn("github", "op", op, "status", status, "duration", d, "err", err)
			return err
		}
		c.log.Info("github retry", "op", op, "status", status, "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func mapError(err error) error {
	var rl *gh.RateLimitError
	if errors.As(err, &rl) {
		return &RateLimitError{Reset: rl.Rate.Reset.Time}
	}
	var ab *gh.AbuseRateLimitError
	if errors.As(err, &ab) {
		reset := time.Now().Add(time.Minute)
		if ab.RetryAfter != nil {
			reset = time.Now().Add(*ab.RetryAfter)
		}
		return &RateLimitError{Reset: reset}
	}
	return err
}

func retryable(err error, status int) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrRateLimited) {
		return false
	}
	if status >= 500 {
		return true
	}
	var ne net.Error
	return status == 0 && errors.As(err, &ne)
}

// StatusCode returns the HTTP status of a GitHub API error, or 0.
func StatusCode(err error) int {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode
	}
	return 0
}

// RepoInfo is what the daemon needs to know about the repository.
type RepoInfo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	URL           string `json:"url"`
}

// Repository reads the repository (GET /repos/{owner}/{repo}). The daemon calls
// it at startup to check the token and learn the default branch.
func (c *Client) Repository(ctx context.Context) (RepoInfo, error) {
	var r *gh.Repository
	err := c.do(ctx, "get_repository", true, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		r, resp, err = cl.Repositories.Get(ctx, c.opts.Repo.Owner, c.opts.Repo.Name)
		return resp, err
	})
	if err != nil {
		return RepoInfo{}, err
	}
	if r.GetDefaultBranch() == "" {
		return RepoInfo{}, errors.New("github: repository has no default branch (is it empty?)")
	}
	return RepoInfo{FullName: r.GetFullName(), DefaultBranch: r.GetDefaultBranch(), Private: r.GetPrivate(),
		URL: r.GetHTMLURL()}, nil
}

// PR is a pull request as the daemon uses it.
type PR struct {
	Number int
	URL    string
	State  string // open | closed
	Merged bool
	Draft  bool
	Head   string // head commit
	Branch string // head branch
	Base   string // base branch
}

func prFrom(p *gh.PullRequest) PR {
	return PR{Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), Merged: p.GetMerged() || p.MergedAt != nil,
		Draft: p.GetDraft(), Head: p.GetHead().GetSHA(), Branch: p.GetHead().GetRef(), Base: p.GetBase().GetRef()}
}

// NewPR describes a pull request to open.
type NewPR struct {
	Branch string // head branch in this repository
	Base   string
	Title  string
	Body   string
}

// EnsurePR returns the open pull request for the branch, creating it if there
// is none. Creating is not idempotent, so it looks first, and a 422 ("already
// exists", e.g. a concurrent create) leads to a second look (ADR 0020).
func (c *Client) EnsurePR(ctx context.Context, n NewPR) (PR, bool, error) {
	if n.Branch == "" || n.Base == "" || strings.TrimSpace(n.Title) == "" {
		return PR{}, false, errors.New("github: branch, base and title are required")
	}
	if p, ok, err := c.findPR(ctx, n.Branch); err != nil || ok {
		return p, false, err
	}
	var created *gh.PullRequest
	err := c.do(ctx, "create_pull_request", false, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		created, resp, err = cl.PullRequests.Create(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, gh.CreatePullRequest{
			Title: gh.Ptr(n.Title), Head: n.Branch, Base: n.Base, Body: gh.Ptr(n.Body),
			MaintainerCanModify: gh.Ptr(false)})
		return resp, err
	})
	if StatusCode(err) == http.StatusUnprocessableEntity {
		if p, ok, ferr := c.findPR(ctx, n.Branch); ferr == nil && ok {
			return p, false, nil
		}
	}
	if err != nil {
		return PR{}, false, err
	}
	p := prFrom(created)
	c.log.Info("pull request created", "pr", p.Number, "branch", n.Branch, "base", n.Base)
	return p, true, nil
}

// findPR looks for the open pull request whose head is branch.
func (c *Client) findPR(ctx context.Context, branch string) (PR, bool, error) {
	var list []*gh.PullRequest
	err := c.do(ctx, "find_pull_request", true, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		list, resp, err = cl.PullRequests.List(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, &gh.PullRequestListOptions{
			State: "open", Head: c.opts.Repo.Owner + ":" + branch, ListOptions: gh.ListOptions{PerPage: 10}})
		return resp, err
	})
	if err != nil {
		return PR{}, false, err
	}
	for _, p := range list {
		if p.GetHead().GetRef() == branch {
			return prFrom(p), true, nil
		}
	}
	return PR{}, false, nil
}

// GetPR reads one pull request.
func (c *Client) GetPR(ctx context.Context, number int) (PR, error) {
	var p *gh.PullRequest
	err := c.do(ctx, "get_pull_request", true, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		p, resp, err = cl.PullRequests.Get(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, number)
		return resp, err
	})
	if err != nil {
		return PR{}, err
	}
	return prFrom(p), nil
}

// Review is a submitted pull request review.
type Review struct {
	ID          int64
	User        string // login, e.g. coderabbitai[bot]
	State       string // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED | PENDING
	Body        string
	CommitID    string
	URL         string
	SubmittedAt time.Time
}

// maxPages bounds paginated reads (100 items per page).
const maxPages = 5

// Reviews lists a pull request's reviews, oldest first.
func (c *Client) Reviews(ctx context.Context, number int) ([]Review, error) {
	var out []Review
	for page := 1; page <= maxPages; page++ {
		var list []*gh.PullRequestReview
		var next int
		err := c.do(ctx, "list_reviews", true, func(cl *gh.Client) (*gh.Response, error) {
			var resp *gh.Response
			var err error
			list, resp, err = cl.PullRequests.ListReviews(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, number,
				&gh.ListOptions{Page: page, PerPage: 100})
			if resp != nil {
				next = resp.NextPage
			}
			return resp, err
		})
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			out = append(out, Review{ID: r.GetID(), User: r.GetUser().GetLogin(), State: r.GetState(), Body: r.GetBody(),
				CommitID: r.GetCommitID(), URL: r.GetHTMLURL(), SubmittedAt: r.GetSubmittedAt().Time})
		}
		if next == 0 {
			break
		}
	}
	return out, nil
}

// Comment is an inline review comment.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"` // line in the file at the reviewed commit (the diff's RIGHT side)
	Body string `json:"body"`
}

// ReviewComments lists the inline comments of one review.
func (c *Client) ReviewComments(ctx context.Context, number int, reviewID int64) ([]Comment, error) {
	var out []Comment
	for page := 1; page <= maxPages; page++ {
		var list []*gh.PullRequestComment
		var next int
		err := c.do(ctx, "list_review_comments", true, func(cl *gh.Client) (*gh.Response, error) {
			var resp *gh.Response
			var err error
			list, resp, err = cl.PullRequests.ListReviewComments(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, number,
				reviewID, &gh.ListOptions{Page: page, PerPage: 100})
			if resp != nil {
				next = resp.NextPage
			}
			return resp, err
		})
		if err != nil {
			return nil, err
		}
		for _, cm := range list {
			line := cm.GetLine()
			if line == 0 {
				line = cm.GetOriginalLine()
			}
			out = append(out, Comment{Path: cm.GetPath(), Line: line, Body: cm.GetBody()})
		}
		if next == 0 {
			break
		}
	}
	return out, nil
}

// PostedReview is the result of PostReview.
type PostedReview struct {
	ID     int64
	URL    string
	Inline bool // false: the comments were folded into the body
}

// PostReview submits a COMMENT review on commit (never APPROVE or
// REQUEST_CHANGES, ADR 0021). Inline comments on lines outside the diff make
// GitHub answer 422; then the comments are folded into the body and the
// review is posted once more, without inline comments. Other errors are not
// retried, so a review is never posted twice.
func (c *Client) PostReview(ctx context.Context, number int, commit, body string, comments []Comment) (PostedReview, error) {
	if strings.TrimSpace(body) == "" {
		return PostedReview{}, errors.New("github: a review needs a body")
	}
	post := func(body string, comments []Comment) (*gh.PullRequestReview, error) {
		req := &gh.PullRequestReviewRequest{CommitID: gh.Ptr(commit), Body: gh.Ptr(body), Event: gh.Ptr("COMMENT")}
		for _, cm := range comments {
			req.Comments = append(req.Comments, &gh.DraftReviewComment{Path: gh.Ptr(cm.Path), Line: gh.Ptr(cm.Line),
				Side: gh.Ptr("RIGHT"), Body: gh.Ptr(cm.Body)})
		}
		var r *gh.PullRequestReview
		err := c.do(ctx, "create_review", false, func(cl *gh.Client) (*gh.Response, error) {
			var resp *gh.Response
			var err error
			r, resp, err = cl.PullRequests.CreateReview(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, number, req)
			return resp, err
		})
		return r, err
	}
	r, err := post(body, comments)
	inline := true
	if err != nil && len(comments) > 0 && StatusCode(err) == http.StatusUnprocessableEntity {
		c.log.Info("inline review comments refused; posting them in the body", "pr", number, "comments", len(comments))
		r, err = post(FoldComments(body, comments), nil)
		inline = false
	}
	if err != nil {
		return PostedReview{}, err
	}
	return PostedReview{ID: r.GetID(), URL: r.GetHTMLURL(), Inline: inline && len(comments) > 0}, nil
}

// PostedComment is a comment agent-office posted on a pull request.
type PostedComment struct {
	ID  int64
	URL string
}

// PostComment posts a comment on pull request number (a reply in its
// conversation, through the issues API; the token's Pull requests write
// permission covers it). It is not retried, so it is never posted twice.
func (c *Client) PostComment(ctx context.Context, number int, body string) (PostedComment, error) {
	if strings.TrimSpace(body) == "" {
		return PostedComment{}, errors.New("github: a comment needs a body")
	}
	var cm *gh.IssueComment
	err := c.do(ctx, "create_comment", false, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		cm, resp, err = cl.Issues.CreateComment(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, number, gh.IssueCommentRequest{Body: body})
		return resp, err
	})
	if err != nil {
		return PostedComment{}, err
	}
	return PostedComment{ID: cm.GetID(), URL: cm.GetHTMLURL()}, nil
}

// FoldComments appends inline comments to a review body as a list.
func FoldComments(body string, comments []Comment) string {
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n**Comments** (GitHub refused them as inline comments):\n")
	for _, cm := range comments {
		fmt.Fprintf(&b, "\n- `%s:%d`: %s", strings.ReplaceAll(cm.Path, "`", ""), cm.Line,
			strings.ReplaceAll(cm.Body, "\n", "\n  "))
	}
	return b.String()
}

// Check states (pull_requests.checks).
const (
	ChecksNone        = "none"        // no check runs or statuses
	ChecksPending     = "pending"     // some still running
	ChecksSuccess     = "success"     // all finished and passed
	ChecksFailure     = "failure"     // at least one failed
	ChecksUnavailable = "unavailable" // the token may not read them (403/404)
)

// Checks is the combined result of a commit's check runs and statuses.
type Checks struct {
	State  string
	Failed []string // names of failed check runs and statuses
}

// Checks reads the check runs and the combined status of commit. Fine-grained
// tokens may lack the Checks permission; a 403 or 404 makes that source
// unavailable instead of failing.
func (c *Client) Checks(ctx context.Context, commit string) (Checks, error) {
	var runs *gh.ListCheckRunsResults
	runsErr := c.do(ctx, "list_check_runs", true, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		runs, resp, err = cl.Checks.ListCheckRunsForRef(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, commit,
			&gh.ListCheckRunsOptions{Filter: gh.Ptr("latest"), ListOptions: gh.ListOptions{PerPage: 100}})
		return resp, err
	})
	var st *gh.CombinedStatus
	stErr := c.do(ctx, "combined_status", true, func(cl *gh.Client) (*gh.Response, error) {
		var resp *gh.Response
		var err error
		st, resp, err = cl.Repositories.GetCombinedStatus(ctx, c.opts.Repo.Owner, c.opts.Repo.Name, commit,
			&gh.ListOptions{PerPage: 100})
		return resp, err
	})
	unavailable := func(err error) bool {
		s := StatusCode(err)
		return s == http.StatusForbidden || s == http.StatusNotFound
	}
	for _, err := range []error{runsErr, stErr} {
		if err != nil && !unavailable(err) {
			return Checks{}, err
		}
	}
	if runsErr != nil && stErr != nil {
		return Checks{State: ChecksUnavailable}, nil
	}
	total, pending := 0, false
	var failed []string
	if runsErr == nil {
		for _, r := range runs.CheckRuns {
			total++
			if r.GetStatus() != "completed" {
				pending = true
				continue
			}
			switch r.GetConclusion() {
			case "success", "neutral", "skipped":
			default:
				failed = append(failed, r.GetName())
			}
		}
	}
	if stErr == nil {
		for _, s := range st.Statuses {
			total++
			switch s.GetState() {
			case "success":
			case "pending":
				pending = true
			default:
				failed = append(failed, s.GetContext())
			}
		}
	}
	switch {
	case len(failed) > 0:
		return Checks{State: ChecksFailure, Failed: failed}, nil
	case pending:
		return Checks{State: ChecksPending}, nil
	case total == 0:
		return Checks{State: ChecksNone}, nil
	}
	return Checks{State: ChecksSuccess}, nil
}
