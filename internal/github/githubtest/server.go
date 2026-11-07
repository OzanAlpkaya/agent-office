// Package githubtest is an in-memory fake of the parts of the GitHub REST API
// agent-office uses, for tests. It records every request and fails the test
// on anything it does not know, such as a merge.
package githubtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Token is the token the fake expects in Authorization headers.
const Token = "fake-github-token-for-tests"

// PR is a pull request in the fake.
type PR struct {
	Number int
	Branch string
	Base   string
	Title  string
	Body   string
	State  string // open | closed
	Merged bool
	Head   string
}

// Review is a submitted review in the fake.
type Review struct {
	ID       int64
	User     string
	State    string
	Body     string
	CommitID string
	Comments []Comment
}

// Comment is an inline review comment in the fake.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// CheckRun is a check run in the fake.
type CheckRun struct {
	Name, Status, Conclusion string
}

// Status is a commit status in the fake.
type Status struct {
	Context, State string
}

// Server is the fake. Lock Mu to read or change its fields during a test.
type Server struct {
	*httptest.Server
	t testing.TB

	Mu            sync.Mutex
	Owner, Name   string
	DefaultBranch string
	Login         string // the token owner; posted reviews are by this user
	PRs           []*PR
	Reviews       map[int][]*Review // by PR number
	CheckRuns     map[string][]CheckRun
	Statuses      map[string][]Status
	ChecksStatus  int                        // non-zero: check-runs answers this status (e.g. 403)
	Fail          map[string][]int           // op -> statuses to answer next, consumed in order
	HeadOf        func(branch string) string // head commit of a pushed branch; nil: "" (tests set it)
	Requests      []string                   // "METHOD /path"
	BadAuth       int                        // requests without the expected token
	HideOpen      int                        // the next n pull request lists come back empty
	nextReview    int64
}

// New starts a fake for owner/name with default branch main.
func New(t testing.TB, owner, name string) *Server {
	s := &Server{t: t, Owner: owner, Name: name, DefaultBranch: "main", Login: "ozan-test",
		Reviews: map[int][]*Review{}, CheckRuns: map[string][]CheckRun{}, Statuses: map[string][]Status{},
		Fail: map[string][]int{}, nextReview: 1000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// BaseURL is the API root to configure clients with.
func (s *Server) BaseURL() string { return s.Server.URL + "/" }

// Count returns how many recorded requests start with prefix ("POST /repos/o/r/pulls").
func (s *Server) Count(prefix string) int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// PR returns a copy of pull request number.
func (s *Server) PR(number int) (PR, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, p := range s.PRs {
		if p.Number == number {
			return *p, true
		}
	}
	return PR{}, false
}

// AddReview adds a review by user on pull request number and returns its id.
func (s *Server) AddReview(number int, user, state, body, commit string, comments ...Comment) int64 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.nextReview++
	s.Reviews[number] = append(s.Reviews[number], &Review{ID: s.nextReview, User: user, State: state, Body: body,
		CommitID: commit, Comments: comments})
	return s.nextReview
}

// ReviewsOf returns copies of a pull request's reviews.
func (s *Server) ReviewsOf(number int) []Review {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var out []Review
	for _, r := range s.Reviews[number] {
		out = append(out, *r)
	}
	return out
}

// SetMerged marks a pull request merged (a human merged it on GitHub).
func (s *Server) SetMerged(number int) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, p := range s.PRs {
		if p.Number == number {
			p.State, p.Merged = "closed", true
		}
	}
}

func (s *Server) failNext(op string) int {
	if f := s.Fail[op]; len(f) > 0 {
		s.Fail[op] = f[1:]
		return f[0]
	}
	return 0
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+Token {
		s.BadAuth++
		writeJSON(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	prefix := "/repos/" + s.Owner + "/" + s.Name
	path, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		s.unknown(w, r)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	op := ""
	switch {
	case r.Method == "GET" && path == "":
		op = "get_repository"
	case r.Method == "GET" && path == "/pulls":
		op = "list_pulls"
	case r.Method == "POST" && path == "/pulls":
		op = "create_pull"
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "pulls":
		op = "get_pull"
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "pulls" && parts[2] == "reviews":
		op = "list_reviews"
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "pulls" && parts[2] == "reviews":
		op = "create_review"
	case r.Method == "GET" && len(parts) == 5 && parts[0] == "pulls" && parts[2] == "reviews" && parts[4] == "comments":
		op = "list_review_comments"
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-runs":
		op = "check_runs"
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "commits" && parts[2] == "status":
		op = "status"
	default:
		s.unknown(w, r)
		return
	}
	if code := s.failNext(op); code != 0 {
		writeJSON(w, code, map[string]string{"message": fmt.Sprintf("injected %d", code)})
		return
	}
	switch op {
	case "get_repository":
		writeJSON(w, 200, map[string]any{"full_name": s.Owner + "/" + s.Name, "default_branch": s.DefaultBranch,
			"private": true, "html_url": "https://github.com/" + s.Owner + "/" + s.Name})
	case "list_pulls":
		state, head := r.URL.Query().Get("state"), r.URL.Query().Get("head")
		out := []any{}
		if s.HideOpen > 0 {
			s.HideOpen--
			writeJSON(w, 200, out)
			return
		}
		for _, p := range s.PRs {
			if (state == "" || state == "all" || p.State == state) && (head == "" || head == s.Owner+":"+p.Branch) {
				out = append(out, s.prJSON(p))
			}
		}
		writeJSON(w, 200, out)
	case "create_pull":
		var in struct{ Title, Head, Base, Body string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"message": "bad json"})
			return
		}
		for _, p := range s.PRs {
			if p.Branch == in.Head && p.State == "open" {
				writeJSON(w, 422, map[string]any{"message": "Validation Failed",
					"errors": []map[string]string{{"message": "A pull request already exists for " + s.Owner + ":" + in.Head + "."}}})
				return
			}
		}
		p := &PR{Number: len(s.PRs) + 1, Branch: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, State: "open"}
		s.PRs = append(s.PRs, p)
		writeJSON(w, 201, s.prJSON(p))
	case "get_pull":
		n, _ := strconv.Atoi(parts[1])
		for _, p := range s.PRs {
			if p.Number == n {
				writeJSON(w, 200, s.prJSON(p))
				return
			}
		}
		writeJSON(w, 404, map[string]string{"message": "Not Found"})
	case "list_reviews":
		n, _ := strconv.Atoi(parts[1])
		out := []any{}
		for _, rv := range s.Reviews[n] {
			out = append(out, map[string]any{"id": rv.ID, "user": map[string]string{"login": rv.User}, "state": rv.State,
				"body": rv.Body, "commit_id": rv.CommitID, "html_url": fmt.Sprintf("https://github.com/x/pull/%d#review-%d", n, rv.ID),
				"submitted_at": time.Now().UTC().Format(time.RFC3339)})
		}
		writeJSON(w, 200, out)
	case "list_review_comments":
		n, _ := strconv.Atoi(parts[1])
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		out := []any{}
		for _, rv := range s.Reviews[n] {
			if rv.ID == id {
				for _, c := range rv.Comments {
					out = append(out, map[string]any{"path": c.Path, "line": c.Line, "body": c.Body,
						"pull_request_review_id": id, "user": map[string]string{"login": rv.User}})
				}
			}
		}
		writeJSON(w, 200, out)
	case "create_review":
		n, _ := strconv.Atoi(parts[1])
		var in struct {
			CommitID string `json:"commit_id"`
			Body     string
			Event    string
			Comments []Comment
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"message": "bad json"})
			return
		}
		if in.Event != "COMMENT" {
			s.t.Errorf("githubtest: review event %q; agent-office must only post COMMENT reviews", in.Event)
		}
		s.nextReview++
		rv := &Review{ID: s.nextReview, User: s.Login, State: "COMMENTED", Body: in.Body, CommitID: in.CommitID,
			Comments: in.Comments}
		s.Reviews[n] = append(s.Reviews[n], rv)
		writeJSON(w, 200, map[string]any{"id": rv.ID, "state": rv.State, "body": rv.Body, "commit_id": rv.CommitID,
			"user":     map[string]string{"login": rv.User},
			"html_url": fmt.Sprintf("https://github.com/x/pull/%d#review-%d", n, rv.ID)})
	case "check_runs":
		if s.ChecksStatus != 0 {
			writeJSON(w, s.ChecksStatus, map[string]string{"message": "Resource not accessible by personal access token"})
			return
		}
		runs := []any{}
		for _, c := range s.CheckRuns[parts[1]] {
			runs = append(runs, map[string]string{"name": c.Name, "status": c.Status, "conclusion": c.Conclusion})
		}
		writeJSON(w, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
	case "status":
		sts := []any{}
		for _, st := range s.Statuses[parts[1]] {
			sts = append(sts, map[string]string{"context": st.Context, "state": st.State})
		}
		writeJSON(w, 200, map[string]any{"state": "pending", "total_count": len(sts), "statuses": sts})
	}
}

func (s *Server) unknown(w http.ResponseWriter, r *http.Request) {
	s.t.Errorf("githubtest: unexpected request %s %s", r.Method, r.URL.Path)
	writeJSON(w, 404, map[string]string{"message": "Not Found"})
}

func (s *Server) prJSON(p *PR) map[string]any {
	head := p.Head
	if s.HeadOf != nil {
		head = s.HeadOf(p.Branch)
	}
	m := map[string]any{"number": p.Number, "state": p.State, "merged": p.Merged, "title": p.Title, "body": p.Body,
		"html_url": fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.Owner, s.Name, p.Number),
		"head":     map[string]string{"ref": p.Branch, "sha": head}, "base": map[string]string{"ref": p.Base}}
	if p.Merged {
		m["merged_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
