//go:build sandbox

package engine

import (
	"context"
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
