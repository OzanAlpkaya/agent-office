//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/OzanAlpkaya/agent-office/internal/github"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

type prReview struct {
	ID       int64  `json:"id"`
	Reviewer string `json:"reviewer"`
	Head     string `json:"head"`
	Status   string `json:"status"`
	Verdict  string `json:"verdict"`
	Summary  string `json:"summary"`
	URL      string `json:"url"`
	Followup string `json:"followup"`
	Error    string `json:"error"`
}

type pullRequest struct {
	ID      int64      `json:"id"`
	TaskID  string     `json:"task_id"`
	AgentID string     `json:"agent_id"`
	Branch  string     `json:"branch"`
	Number  int        `json:"number"`
	URL     string     `json:"url"`
	State   string     `json:"state"`
	Head    string     `json:"head"`
	Checks  string     `json:"checks"`
	Rounds  int        `json:"rounds"`
	Error   string     `json:"error"`
	Reviews []prReview `json:"reviews"`
}

// TestGitHubEndToEnd is the Phase 5 DoD with real Claude Code (haiku),
// sandboxed agents and the real GitHub repository named by AO_E2E_GITHUB_REPO
// (owner/name; the push remote of AO_E2E_REPO must point at it). It pushes
// branches agent-office/... and opens a pull request there:
//
//  1. a worker's finished task becomes a pull request;
//  2. the internal reviewer agent reviews it and the review is posted on GitHub;
//  3. the review reaches the original worker (automatically if it requested
//     changes; otherwise a review the test posts is sent with "Send to
//     worker"), which is relaunched and whose new commit updates the same PR;
//  4. nothing is merged.
//
// Afterwards the PR is closed and the branch deleted on GitHub, unless
// AO_E2E_KEEP=1. Needs podman, the worker image, the OAuth token file and the
// GitHub token file (ADR 0020).
func TestGitHubEndToEnd(t *testing.T) {
	src, want := os.Getenv("AO_E2E_REPO"), os.Getenv("AO_E2E_GITHUB_REPO")
	if src == "" || want == "" {
		t.Skip("AO_E2E_REPO and AO_E2E_GITHUB_REPO not set; this test pushes to GitHub and runs real Claude Code sessions")
	}
	for _, bin := range []string{"claude", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	tokenPath, err := sandbox.DefaultTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.ReadTokenFile(tokenPath); err != nil {
		t.Skipf("no usable OAuth token file: %v", err)
	}
	ghTokenPath, err := github.DefaultTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	ghToken, err := secret.ReadFile(ghTokenPath, github.ErrTokenFile)
	if err != nil {
		t.Skipf("no usable GitHub token file: %v", err)
	}
	ghRepo, err := github.ParseRepo(want)
	if err != nil {
		t.Fatal(err)
	}
	top := gitOut(t, src, "rev-parse", "--show-toplevel")
	remote := gitOut(t, top, "remote", "get-url", "--push", "origin")
	if r, err := github.ParseRemote(remote); err != nil || r != ghRepo {
		t.Fatalf("origin of %s is %q (%v), want %s", top, remote, err, want)
	}
	model := os.Getenv("AO_E2E_MODEL")
	if model == "" {
		model = "haiku"
	}
	// The test's own client, only to look at and clean up what the daemon did.
	client, err := gh.NewClient(gh.WithAuthToken(ghToken.Reveal()), gh.WithTimeout(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", data).Run() })
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", top, repo).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	gitOut(t, repo, "remote", "set-url", "origin", remote)
	for _, k := range []string{"user.name", "user.email"} {
		gitOut(t, repo, "config", k, gitOut(t, top, "config", k))
	}
	bin := filepath.Join(tmp, "agent-office")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--repo", repo, "--addr", "127.0.0.1:" + port, "--data-dir", data, "--model", model,
			"--max-agents", "3", "--log-level", "debug", "--sandbox", "podman", "--github", "--reviewer", "internal",
			"--review-rounds", "1"}}
	var pr pullRequest
	t.Cleanup(func() {
		var agents []teamAgent
		if d.cmd != nil {
			d.json("GET", "/api/agents", nil, 200, &agents)
			for _, a := range agents {
				d.do("POST", "/api/agents/"+a.ID+"/stop", nil)
			}
		}
		d.signal(syscall.SIGTERM)
		for _, a := range agents {
			_ = exec.Command("podman", "rm", "-f", "ao-"+a.ID).Run()
		}
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 150))
			}
		}
		if pr.Number == 0 || os.Getenv("AO_E2E_KEEP") == "1" {
			if pr.Number != 0 {
				t.Logf("kept %s", pr.URL)
			}
			return
		}
		if _, _, err := client.PullRequests.Edit(ctx, ghRepo.Owner, ghRepo.Name, pr.Number,
			&gh.PullRequest{State: gh.Ptr("closed")}); err != nil {
			t.Errorf("close PR #%d: %v", pr.Number, err)
		}
		if out, err := exec.Command("git", "-C", repo, "push", "-q", "origin", "--delete", pr.Branch).CombinedOutput(); err != nil {
			t.Errorf("delete branch %s: %v %s", pr.Branch, err, out)
		}
	})
	d.start()

	var b [3]byte
	_, _ = rand.Read(b[:])
	file := "e2e-greeting-" + hex.EncodeToString(b[:]) + ".txt"
	var task taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E GitHub: add " + file,
		"description": "Create a file named " + file + " containing the single line hello. Commit it, then call report_done."},
		201, &task)
	var w teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": task.ID}, 201, &w)

	waitPR := func(what string, timeout time.Duration, ok func(p pullRequest) bool) pullRequest {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			var list []pullRequest
			d.json("GET", "/api/pull-requests", nil, 200, &list)
			for _, p := range list {
				if p.TaskID == task.ID && ok(p) {
					return p
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s: %+v", what, list)
			}
			time.Sleep(3 * time.Second)
		}
	}

	// 1. The finished task becomes a pull request on GitHub.
	pr = waitPR("the pull request", 8*time.Minute, func(p pullRequest) bool { return p.State == "open" || p.Error != "" })
	if pr.State != "open" {
		t.Fatalf("pull request not opened: %+v", pr)
	}
	t.Logf("PR #%d %s on %s at %s", pr.Number, pr.URL, pr.Branch, pr.Head)
	ghPR, _, err := client.PullRequests.Get(ctx, ghRepo.Owner, ghRepo.Name, pr.Number)
	if err != nil || ghPR.GetHead().GetSHA() != pr.Head || ghPR.GetState() != "open" || ghPR.GetMerged() {
		t.Fatalf("PR on GitHub: %v head %s state %s", err, ghPR.GetHead().GetSHA(), ghPR.GetState())
	}
	if out, err := exec.Command("git", "-C", repo, "show", pr.Head+":"+file).Output(); err != nil ||
		strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("%s at the PR head: %q %v", file, out, err)
	}

	// 2. The internal reviewer reviews it; the review is on GitHub.
	pr = waitPR("the internal review", 10*time.Minute, func(p pullRequest) bool {
		return len(p.Reviews) > 0 && p.Reviews[len(p.Reviews)-1].Status != "queued" && p.Reviews[len(p.Reviews)-1].Status != "running"
	})
	rv := pr.Reviews[len(pr.Reviews)-1]
	t.Logf("review %d: %s, verdict %s, follow-up %q: %s", rv.ID, rv.Status, rv.Verdict, rv.Followup, rv.Summary)
	if rv.Status != "done" || rv.Reviewer != "internal" || rv.Head != pr.Head || rv.URL == "" {
		t.Fatalf("review %+v", rv)
	}
	reviews, _, err := client.PullRequests.ListReviews(ctx, ghRepo.Owner, ghRepo.Name, pr.Number, nil)
	if err != nil || len(reviews) == 0 || reviews[0].GetState() != "COMMENTED" || reviews[0].GetCommitID() != pr.Head {
		t.Fatalf("reviews on GitHub: %v %+v", err, reviews)
	}

	// 3. The feedback reaches the original worker.
	if rv.Verdict != "changes_requested" {
		t.Logf("the reviewer did not request changes; sending a review of our own with Send to worker")
		body := "Please add a second line `world` to " + file + "."
		if _, _, err := client.PullRequests.CreateReview(ctx, ghRepo.Owner, ghRepo.Name, pr.Number, &gh.PullRequestReviewRequest{
			CommitID: gh.Ptr(pr.Head), Body: gh.Ptr(body), Event: gh.Ptr("COMMENT")}); err != nil {
			t.Fatal(err)
		}
		pr = waitPR("the human review", 3*time.Minute, func(p pullRequest) bool {
			return len(p.Reviews) > 0 && p.Reviews[0].Reviewer == "human"
		})
		d.json("POST", "/api/pull-requests/"+strconv.FormatInt(pr.ID, 10)+"/follow-up", map[string]int64{"review_id": pr.Reviews[0].ID}, 200, nil)
	}
	first := pr.Head
	pr = waitPR("the follow-up push", 10*time.Minute, func(p pullRequest) bool { return p.Head != first && p.Error == "" })
	t.Logf("follow-up pushed: %s -> %s", first, pr.Head)
	var agents []teamAgent
	d.json("GET", "/api/agents", nil, 200, &agents)
	for _, a := range agents {
		t.Logf("agent %s %s %s (%s) session %s", a.ID, a.Role, a.State, a.ExitReason, a.SessionID)
		if a.Role == "worker" && a.ID != w.ID {
			t.Errorf("a second worker %s ran; the follow-up must relaunch the original one", a.ID)
		}
	}
	if err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", first, pr.Head).Run(); err != nil {
		t.Errorf("the follow-up is not a fast-forward of the first head: %v", err)
	}
	ghPR, _, err = client.PullRequests.Get(ctx, ghRepo.Owner, ghRepo.Name, pr.Number)
	if err != nil || ghPR.GetHead().GetSHA() != pr.Head {
		t.Fatalf("PR head on GitHub %s, want %s (%v)", ghPR.GetHead().GetSHA(), pr.Head, err)
	}

	// 4. Nothing merged.
	if ghPR.GetMerged() || ghPR.GetState() != "open" {
		t.Fatalf("PR merged or closed: %s merged=%v", ghPR.GetState(), ghPR.GetMerged())
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		switch {
		case strings.Contains(line, "team tool failed"), strings.Contains(line, "api request refused"):
			t.Errorf("%s", line)
		case strings.Contains(line, "pull request"), strings.Contains(line, "review"), strings.Contains(line, "relaunch"):
			t.Log(line)
		}
	}
}
