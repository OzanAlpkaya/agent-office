package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

func strp(s string) *string { return &s }

// agentGone matches the update that records an agent's process exit (a
// SessionEnd hook shows "exited" a moment before).
func agentGone(id string) func(Update) bool {
	return func(u Update) bool { return u.Type == "agent" && u.Agent.ID == id && u.Agent.ExitedAt != nil }
}

// startAndExit starts a worker on a new task, lets it finish one turn and
// exit, and returns it.
func startAndExit(t *testing.T, e *Engine, log *updateLog, title string) (AgentInfo, Task) {
	t.Helper()
	ctx := context.Background()
	task, err := e.CreateTask(ctx, TaskInput{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	typeLine(t, e, a.ID, "turn")
	typeLine(t, e, a.ID, "exit")
	log.wait(t, 5*time.Second, agentGone(a.ID))
	return a, task
}

// A resume continues the agent's latest conversation (ADR 0035): same agent,
// workspace and branch, a new session that records which one it continues,
// the task's status left alone, and no memory appended again.
func TestResumeKeepsAgentAndWorkspace(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	if _, err := e.AddMemory(ctx, MemoryInput{Kind: MemoryConvention, Title: "Tabs", Body: "Use tabs."}); err != nil {
		t.Fatal(err)
	}
	a, task := startAndExit(t, e, log, "Keep context")
	if got, _ := e.Get(a.ID); !got.Resumable {
		t.Fatalf("an exited worker is resumable: %+v", got)
	}
	if _, err := e.UpdateTask(ctx, task.ID, TaskPatch{Status: strp(StatusReview)}); err != nil {
		t.Fatal(err)
	}

	r, err := e.Resume(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != a.ID || r.SessionID == a.SessionID || r.WorktreePath != a.WorktreePath || r.Branch != a.Branch ||
		r.ResumedFrom != a.SessionID || r.Resumable {
		t.Fatalf("resumed %+v\nfrom %+v", r, a)
	}
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	out := screen(t, e, a.ID, "RESUMED:"+a.SessionID)
	if strings.Contains(out, "PROMPT:") {
		t.Fatalf("a resume without a prompt sent one:\n%s", out)
	}
	sess, err := f.store.Q.GetSession(ctx, r.SessionID)
	if err != nil || sess.ResumedFrom != a.SessionID || sess.AgentID != a.ID {
		t.Fatalf("session %+v %v", sess, err)
	}
	if got, _ := e.getTask(ctx, task.ID); got.Status != StatusReview {
		t.Fatalf("a resume moved the task to %s", got.Status)
	}

	// Again, with the user's prompt: it continues the second session, and the
	// prompt carries no memory section (the conversation has it).
	from := log.mark()
	typeLine(t, e, a.ID, "exit")
	log.waitFrom(t, 5*time.Second, from, agentGone(a.ID))
	r2, err := e.Resume(ctx, a.ID, "Please also add a test.")
	if err != nil {
		t.Fatal(err)
	}
	if r2.ResumedFrom != r.SessionID {
		t.Fatalf("second resume continues %s, want %s", r2.ResumedFrom, r.SessionID)
	}
	out = screen(t, e, a.ID, "PROMPT:Please also add a test.")
	if strings.Contains(out, "Project memory") {
		t.Fatalf("memory appended to a resume prompt:\n%s", out)
	}
}

func TestResumeRefusals(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()

	running, err := e.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(running.ID, hooks.Idle))
	if _, err := e.Resume(ctx, running.ID, ""); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("running agent: %v", err)
	}
	if _, err := e.Resume(ctx, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, err := e.Resume(ctx, running.ID, strings.Repeat("x", MaxResumePrompt+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long prompt: %v", err)
	}
	if err := e.Stop(running.ID); err != nil {
		t.Fatal(err)
	}

	// A lead whose goal is done is not resumed.
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Goal"})
	lead, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(lead.ID, hooks.Idle))
	if err := e.Stop(lead.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UpdateTask(ctx, goal.ID, TaskPatch{Status: strp(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, lead.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "done") {
		t.Fatalf("lead of a done goal: %v", err)
	}

	// The workspace is gone.
	if _, err := e.Remove(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, running.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed agent: %v", err)
	}

	// Reviewers and QA end with their run (ADR 0021, 0033).
	for kind, want := range map[string]bool{KindWorker: true, KindArchitect: true, KindLead: true, KindAssistant: true,
		KindQA: false, KindReviewer: false} {
		if resumable(kind) != want {
			t.Errorf("resumable(%s) = %v", kind, !want)
		}
	}
}

// A resumed CLI that cannot load the conversation exits before its first
// hook; the agent says so instead of "process exited".
func TestResumeFailed(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a, _ := startAndExit(t, e, log, "Lost conversation")
	if err := os.WriteFile(filepath.Join(a.WorktreePath, ".fake-no-conversation"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	from := log.mark()
	if _, err := e.Resume(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	u := log.waitFrom(t, 5*time.Second, from, agentGone(a.ID))
	if u.Agent.ExitReason != ExitResumeFailed {
		t.Fatalf("exit reason %q", u.Agent.ExitReason)
	}
	screen(t, e, a.ID, "No conversation found with session ID: "+a.SessionID)
}

// Messages queued for a conversation reach the session that resumes it, even
// though they are older than the new session (ADR 0035; a fresh relaunch
// still never gets them, ADR 0022).
func TestResumeDeliversQueuedMessages(t *testing.T) {
	f := newFixture(t)
	f.opts.deliverySettle, f.opts.typingQuiet, f.opts.enterDelay = 50*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a, err := e.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Idle))
	typeLine(t, e, a.ID, "busy")
	log.wait(t, 5*time.Second, agentState(a.ID, hooks.Working))
	time.Sleep(100 * time.Millisecond) // past typingQuiet
	if _, err := e.MessageAgent(ctx, a.ID, "the build is green"); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(a.ID); err != nil { // it never finished the turn: nothing delivered
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	screen(t, e, a.ID, "the build is green")
}
