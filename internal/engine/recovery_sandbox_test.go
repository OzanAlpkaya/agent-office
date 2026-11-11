//go:build sandbox

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// TestSandboxedOOMKillIsExplained is incident 1 with a real kernel OOM kill
// (ADR 0051): the fake agent makes itself the biggest process in its worker,
// as a Claude Code process does, until the worker's memory limit is reached.
// Podman's State.OOMKilled is not relied on; sandbox-init's exit report says
// what happened. The agent is resumed once automatically.
func TestSandboxedOOMKillIsExplained(t *testing.T) {
	f := sandboxFixture(t, false)
	f.opts.Sandbox.Memory = "256m"
	// A fast running watch, so it may read the counter between the kill and
	// the exit (it did in an e2e run): the agent's death must still read as OOM.
	old := memWatchEvery
	memWatchEvery = 50 * time.Millisecond
	t.Cleanup(func() { memWatchEvery = old })
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	mark := log.mark()
	typeLine(t, e, a.ID, "hog")
	ex := log.waitFrom(t, 60*time.Second, mark, func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == a.ID && u.Agent.ExitedAt != nil
	}).Agent
	if !ex.OOMKilled || ex.ExitSignal != "KILL" || ex.ExitCode == nil || *ex.ExitCode != 137 ||
		!strings.Contains(ex.ExitDetail, "Out of memory") || !strings.Contains(ex.ExitDetail, "256m memory limit") {
		t.Fatalf("exit %+v", ex)
	}
	if containerExists(t, sandbox.ContainerName(a.ID)) {
		// The daemon inspects the ended worker (no --rm) and removes it itself.
		time.Sleep(time.Second)
		if containerExists(t, sandbox.ContainerName(a.ID)) {
			t.Fatal("the ended worker's container was not removed")
		}
	}
	log.waitFrom(t, 60*time.Second, mark, resumedSession(a.ID))
	items, _ := e.Inbox(context.Background())
	if len(items) == 0 || !strings.Contains(items[0].Body, "Out of memory") || !strings.Contains(items[0].Body, "resumed its conversation automatically") {
		t.Fatalf("the user is not told (a worker without a lead): %+v", items)
	}
}

// TestSandboxedChildOOMIsReported: an OOM kill of a process the agent ran
// leaves the agent running; the running memory watch tells it why.
func TestSandboxedChildOOMIsReported(t *testing.T) {
	f := sandboxFixture(t, false)
	f.opts.Sandbox.Memory = "256m"
	old := memWatchEvery
	memWatchEvery = 300 * time.Millisecond
	t.Cleanup(func() { memWatchEvery = old })
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	time.Sleep(2 * time.Second) // the watch found the worker's cgroup
	// The hog makes itself the kernel's first choice, so the agent survives.
	typeLine(t, e, a.ID, "sh echo 1000 > /proc/self/oom_score_adj; head -c 900m /dev/zero | tail")
	log.wait(t, 60*time.Second, func(u Update) bool {
		return u.Type == "sandbox_event" && u.SandboxEvent.AgentID == a.ID && u.SandboxEvent.Event.Kind == sandbox.OOMKill
	})
	eventually(t, "the agent is told", func() bool {
		return bodiesContaining(messagesTo(t, e, a.ID), "process(es) in your sandbox: it reached its 256m memory limit") >= 1
	})
	if got, _ := e.Get(a.ID); got.State == string(hooks.Exited) {
		t.Fatal("the agent itself died")
	}
}

// TestSandboxedGrantSurvivesResume is the request #4 bug (ADR 0053): an
// approved host stays allowed in the agent's resumed session.
func TestSandboxedGrantSurvivesResume(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a := startIdle(t, e, log)
	r, err := e.Team().RequestAccess(ctx, a.ID, mcp.RequestAccessInput{Kind: "domain", Value: "storage.googleapis.com", Reason: "module zips"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DecideAccess(ctx, r.RequestID, true); err != nil {
		t.Fatal(err)
	}
	exitAgent(t, e, log, a.ID)
	mark := log.mark()
	if _, err := e.Resume(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	log.waitFrom(t, 30*time.Second, mark, agentState(a.ID, hooks.Idle))
	e.mu.Lock()
	cur := e.agents[a.ID]
	ok := cur.sandbox != nil && cur.sandbox.grants.Allows("storage.googleapis.com")
	e.mu.Unlock()
	if !ok {
		t.Fatal("the approval did not reach the resumed session")
	}
	if _, err := e.Team().RequestAccess(ctx, a.ID, mcp.RequestAccessInput{Kind: "domain", Value: "storage.googleapis.com", Reason: "again"}); !isUser(err, ErrInvalid) {
		t.Fatalf("an approved host requested again: %v", err)
	}
}

// TestSandboxedRelaunchReplacesALeftoverExitReport: a daemon that was killed
// leaves the runtime dir with an exit report owned by the sandbox user; the
// restored launch must not fail on it (found by the e2e restore runs).
func TestSandboxedRelaunchReplacesALeftoverExitReport(t *testing.T) {
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a := startIdle(t, e, log)
	exitAgent(t, e, log, a.ID)
	dir := filepath.Join(f.opts.RuntimeDir, "agents", a.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(dir, exitReportName)
	if err := os.WriteFile(left, []byte(`{"code":0,"oom_kills":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.opts.Sandbox.Podman.Chown(ctx, f.opts.RuntimeDir, left); err != nil {
		t.Fatal(err)
	}
	mark := log.mark()
	if _, err := e.Resume(ctx, a.ID, ""); err != nil {
		t.Fatalf("resume over a leftover exit report: %v", err)
	}
	log.waitFrom(t, 30*time.Second, mark, agentState(a.ID, hooks.Idle))
}

// TestSandboxedPidsLimitIsReported: a worker that reaches its process limit
// (PulseGo's integration suite at the default 256) is told the limit and
// where to raise it, and the UI gets a sandbox event (Phase 17). The limit
// comes from the project or pack (Sandbox.Pids), as podman runs it.
func TestSandboxedPidsLimitIsReported(t *testing.T) {
	f := sandboxFixture(t, false)
	f.opts.Sandbox.Pids = 64
	old := memWatchEvery
	memWatchEvery = 300 * time.Millisecond
	t.Cleanup(func() { memWatchEvery = old })
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	name := sandbox.ContainerName(a.ID)
	out, err := exec.Command("podman", "inspect", "--format", "{{.HostConfig.PidsLimit}}", name).Output()
	if err != nil || strings.TrimSpace(string(out)) != "64" {
		t.Fatalf("the worker's pids limit: %q %v", out, err)
	}
	time.Sleep(2 * time.Second) // the watch found the worker's cgroup
	// More processes than the limit, in the worker's cgroup; some forks fail.
	_ = exec.Command("podman", "exec", name, "sh", "-c", "for i in $(seq 1 120); do sleep 3 & done; wait").Run()
	log.wait(t, 30*time.Second, func(u Update) bool {
		return u.Type == "sandbox_event" && u.SandboxEvent.AgentID == a.ID && u.SandboxEvent.Event.Kind == sandbox.PidsLimit
	})
	eventually(t, "the agent is told", func() bool {
		return bodiesContaining(messagesTo(t, e, a.ID), "reached its limit of 64 processes") >= 1
	})
	if got, _ := e.Get(a.ID); got.State == string(hooks.Exited) {
		t.Fatal("the agent died")
	}
}
