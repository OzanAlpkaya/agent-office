// Package review is the pluggable code review of pull requests (ADR 0021):
// one Reviewer interface with two implementations, an internal reviewer agent
// and CodeRabbit. Whatever reviews, the engine gets the same Feedback.
package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/OzanAlpkaya/agent-office/internal/github"
)

// Reviewer names (reviews.reviewer).
const (
	NameInternal   = "internal"
	NameCodeRabbit = "coderabbit"
	NameHuman      = "human" // a person's review on GitHub; recorded, never automatic
)

// Verdicts (reviews.verdict).
const (
	VerdictApprove          = "approve"
	VerdictChangesRequested = "changes_requested"
	VerdictComment          = "comment"
)

// Verdicts lists the valid verdicts.
var Verdicts = []string{VerdictApprove, VerdictChangesRequested, VerdictComment}

// PR is the pull request to review: Head is the commit the review must be for.
type PR struct {
	ID      int64  // agent-office's pull request id
	Number  int    // GitHub number
	Branch  string // head branch
	Base    string
	Head    string
	Title   string
	URL     string
	TaskID  string
	AgentID string // the worker whose branch it is
}

// Comment is one inline comment.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// Feedback is a finished review of one head.
type Feedback struct {
	Reviewer       string    `json:"reviewer"`
	Head           string    `json:"head"`
	Verdict        string    `json:"verdict"`
	Summary        string    `json:"summary"`
	Comments       []Comment `json:"comments"`
	URL            string    `json:"url,omitempty"`
	GitHubReviewID int64     `json:"github_review_id,omitempty"`
}

// Actionable reports whether the feedback asks the worker for changes (ADR 0022).
func (f Feedback) Actionable() bool { return f.Verdict == VerdictChangesRequested }

// Reviewer reviews pull requests.
type Reviewer interface {
	Name() string
	// Request asks for a review of pr.Head. It may return ErrNoSlot: the
	// review stays queued and is requested again later.
	Request(ctx context.Context, pr PR) error
	// Poll returns the review of pr.Head, or nil while it is pending.
	// Reviews of other commits are ignored.
	Poll(ctx context.Context, pr PR) (*Feedback, error)
}

// ErrNoSlot: the reviewer cannot start now (all agent slots are busy).
var ErrNoSlot = errors.New("review: no free agent slot")

// Runner is what the internal reviewer needs from the engine: start a
// reviewer agent on a pull request, and read what it submitted.
type Runner interface {
	StartReviewer(ctx context.Context, pr PR) error
	Submitted(ctx context.Context, pr PR) (*Feedback, error)
}

// Internal is the internal reviewer: a sandboxed agent with role reviewer
// that submits a structured review through the submit_review tool; the
// daemon posts it to GitHub.
type Internal struct{ Runner Runner }

func (Internal) Name() string { return NameInternal }

func (r Internal) Request(ctx context.Context, pr PR) error { return r.Runner.StartReviewer(ctx, pr) }

func (r Internal) Poll(ctx context.Context, pr PR) (*Feedback, error) {
	f, err := r.Runner.Submitted(ctx, pr)
	if err != nil || f == nil || f.Head != pr.Head {
		return nil, err
	}
	f.Reviewer = NameInternal
	return f, nil
}

// GitHubReader is the part of the GitHub client CodeRabbit's reader needs.
type GitHubReader interface {
	Reviews(ctx context.Context, number int) ([]github.Review, error)
	ReviewComments(ctx context.Context, number int, reviewID int64) ([]github.Comment, error)
}

// CodeRabbitLogin is the GitHub login CodeRabbit's reviews are posted under.
const CodeRabbitLogin = "coderabbitai[bot]"

// CodeRabbit reads the CodeRabbit GitHub App's reviews. The app reviews new
// pull requests and pushes by itself, so Request does nothing.
type CodeRabbit struct {
	GitHub GitHubReader
	Login  string // default CodeRabbitLogin
}

func (CodeRabbit) Name() string { return NameCodeRabbit }

func (CodeRabbit) Request(context.Context, PR) error { return nil }

// Poll returns the newest review by the bot on pr.Head, with its inline comments.
func (r CodeRabbit) Poll(ctx context.Context, pr PR) (*Feedback, error) {
	login := r.Login
	if login == "" {
		login = CodeRabbitLogin
	}
	reviews, err := r.GitHub.Reviews(ctx, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("coderabbit: %w", err)
	}
	var last *github.Review
	for i := range reviews {
		rv := &reviews[i]
		if rv.User == login && rv.CommitID == pr.Head && rv.State != "PENDING" && rv.State != "DISMISSED" {
			last = rv
		}
	}
	if last == nil {
		return nil, nil
	}
	cs, err := r.GitHub.ReviewComments(ctx, pr.Number, last.ID)
	if err != nil {
		return nil, fmt.Errorf("coderabbit: %w", err)
	}
	f := &Feedback{Reviewer: NameCodeRabbit, Head: pr.Head, Summary: last.Body, URL: last.URL, GitHubReviewID: last.ID,
		Comments: fromGitHub(cs)}
	switch {
	case last.State == "CHANGES_REQUESTED" || len(f.Comments) > 0:
		f.Verdict = VerdictChangesRequested
	case last.State == "APPROVED":
		f.Verdict = VerdictApprove
	default:
		f.Verdict = VerdictComment
	}
	return f, nil
}

// VerdictOf maps a GitHub review state to a verdict (people's reviews).
func VerdictOf(state string) string {
	switch state {
	case "CHANGES_REQUESTED":
		return VerdictChangesRequested
	case "APPROVED":
		return VerdictApprove
	}
	return VerdictComment
}

func fromGitHub(cs []github.Comment) []Comment {
	out := make([]Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, Comment{Path: c.Path, Line: c.Line, Body: c.Body})
	}
	return out
}

// ToGitHub converts comments for posting.
func ToGitHub(cs []Comment) []github.Comment {
	out := make([]github.Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, github.Comment{Path: c.Path, Line: c.Line, Body: c.Body})
	}
	return out
}
