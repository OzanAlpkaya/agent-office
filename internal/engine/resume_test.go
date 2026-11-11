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

	from := log.mark()
	r, err := e.Resume(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != a.ID || r.Name != a.Name || r.SessionID == a.SessionID || r.WorktreePath != a.WorktreePath || r.Branch != a.Branch ||
		r.ResumedFrom != a.SessionID || r.Resumable {
		t.Fatalf("resumed %+v\nfrom %+v", r, a)
	}
	log.waitFrom(t, 5*time.Second, from, agentState(a.ID, hooks.Idle))
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

	// Again, with the user's prompt. The second session got no message, so
	// Claude Code wrote no conversation for it: the resume continues the first
	// one (resumeSource). The prompt carries no memory section.
	from = log.mark()
	typeLine(t, e, a.ID, "exit")
	log.waitFrom(t, 5*time.Second, from, agentGone(a.ID))
	r2, err := e.Resume(ctx, a.ID, "Please also add a test.")
	if err != nil {
		t.Fatal(err)
	}
	if r2.ResumedFrom != a.SessionID {
		t.Fatalf("second resume continues %s, want %s (the empty fork %s skipped)", r2.ResumedFrom, a.SessionID, r.SessionID)
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
	f.opts.HistoryDir = t.TempDir()
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
	// The failed session never ran: it is forgotten, so the next resume
	// continues the original conversation (found in the end-of-phase review).
	ss, _ := e.Sessions(ctx, a.ID)
	if len(ss) != 1 || ss[0].ID != a.SessionID {
		t.Fatalf("sessions after a failed resume: %+v", ss)
	}
	if _, err := os.Stat(e.historyPath(a.ID, u.Agent.SessionID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the failed session's terminal was kept: %v", err)
	}
	if _, err := os.Stat(e.historyPath(a.ID, a.SessionID)); err != nil {
		t.Fatalf("the original session's terminal: %v", err)
	}
	if err := os.Remove(filepath.Join(a.WorktreePath, ".fake-no-conversation")); err != nil {
		t.Fatal(err)
	}
	r, err := e.Resume(ctx, a.ID, "")
	if err != nil || r.ResumedFrom != a.SessionID {
		t.Fatalf("resume after a failed one: %+v %v", r, err)
	}
	screen(t, e, a.ID, "RESUMED:"+a.SessionID)
}

// A shutdown in the middle of a restore keeps the states it did not reach,
// so the next start restores them (found in the end-of-phase review).
func TestRestoreStopsAtShutdown(t *testing.T) {
	f := newFixture(t)
	f.opts.RestoreAgents = true
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	w, _ := e1.Start(ctx, StartRequest{})
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	e1.Shutdown()
	// A daemon that is already shutting down when its restore reaches the agent.
	e2 := &Engine{opts: f.opts, log: e1.log, q: f.store.Q, st: f.store, project: e1.project, agents: map[string]*agentProc{},
		subs: map[chan Update]struct{}{}, startingKind: map[string]bool{}, noRestore: map[string]string{}, closing: true}
	e2.restore(ctx)
	if st := resumeStateOf(t, f, w.ID); st != "idle" {
		t.Fatalf("resume state after an interrupted restore: %q", st)
	}
	if items, _ := e2.Inbox(ctx); len(items) != 0 {
		t.Fatalf("notice from an interrupted restore: %+v", items)
	}
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

// eventually polls cond for up to 10 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func resumeStateOf(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var st string
	if err := f.store.DB.QueryRow(`SELECT resume_state FROM agents WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// A clean shutdown records what each agent was doing; the next start resumes
// them (ADR 0035): coordinators first, a mid-turn worker with agent-office's
// prompt, an idle one without, within --max-agents, and one notice says what
// came back and what did not.
func TestRestoreAfterShutdown(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.RestoreAgents = true
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	busy, err := e1.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(busy.ID, hooks.Idle))
	typeLine(t, e1, busy.ID, "busy")
	log.wait(t, 5*time.Second, agentState(busy.ID, hooks.Working))
	idle, err := e1.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	goal, _ := e1.CreateTask(ctx, TaskInput{Title: "Goal"})
	lead, err := e1.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(idle.ID, hooks.Idle))
	log.wait(t, 5*time.Second, agentState(lead.ID, hooks.Idle))
	e1.Shutdown()
	for id, want := range map[string]string{busy.ID: "working", idle.ID: "idle", lead.ID: "idle"} {
		if got := resumeStateOf(t, f, id); got != want {
			t.Fatalf("%s: resume state %q, want %q", id, got, want)
		}
	}

	f.opts.MaxAgents = 2
	e2 := f.engine(t)
	eventually(t, "the restore notice", func() bool {
		items, _ := e2.Inbox(ctx)
		return len(items) == 1
	})
	items, _ := e2.Inbox(ctx)
	notice := items[0].Body
	if items[0].AgentID != "" || items[0].Kind != InboxInfo || !strings.Contains(notice, "Resumed with their conversations") ||
		!strings.Contains(notice, idle.Name+" (worker, agent id "+idle.ID+"): no free agent slot") {
		t.Fatalf("notice %+v", items[0])
	}
	for _, id := range []string{lead.ID, busy.ID} {
		got, _ := e2.Get(id)
		if !got.HasTerminal || got.ResumedFrom == "" {
			t.Fatalf("%s not restored: %+v", id, got)
		}
	}
	if got, _ := e2.Get(idle.ID); got.HasTerminal || !got.Resumable {
		t.Fatalf("idle worker beyond the slot limit: %+v", got)
	}
	out := screen(t, e2, busy.ID, "RESUMED:"+busy.SessionID)
	if !strings.Contains(out, "PROMPT:[agent-office] agent-office was restarted while you were working") {
		t.Fatalf("the mid-turn worker got no restart prompt:\n%s", out)
	}
	if out := screen(t, e2, lead.ID, "RESUMED:"+lead.SessionID); strings.Contains(out, "PROMPT:") {
		t.Fatalf("the idle lead got a prompt:\n%s", out)
	}
	for _, id := range []string{busy.ID, idle.ID, lead.ID} {
		if st := resumeStateOf(t, f, id); st != "" {
			t.Fatalf("%s: resume state %q left after the restore", id, st)
		}
	}
}

// After a crash the row's last state counts; an agent that never got as far
// as its conversation is not resumed.
func TestRestoreAfterCrash(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.RestoreAgents = true
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	w, _ := e1.Start(ctx, StartRequest{})
	s, _ := e1.Start(ctx, StartRequest{})
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	log.wait(t, 5*time.Second, agentState(s.ID, hooks.Idle))
	// w had a conversation (an agent waiting for permission got a prompt).
	from := log.mark()
	typeLine(t, e1, w.ID, "turn")
	log.waitFrom(t, 5*time.Second, from, agentState(w.ID, hooks.Idle))
	e1.Shutdown()
	// Simulate a crash: the rows still say what the agents were doing.
	for id, st := range map[string]string{w.ID: "waiting_permission", s.ID: "starting"} {
		if _, err := f.store.DB.Exec(`UPDATE agents SET state = ?, exited_at = NULL, exit_reason = '', resume_state = '' WHERE id = ?`,
			st, id); err != nil {
			t.Fatal(err)
		}
	}
	e2 := f.engine(t)
	eventually(t, "the restore notice", func() bool { items, _ := e2.Inbox(ctx); return len(items) == 1 })
	screen(t, e2, w.ID, "PROMPT:[agent-office] agent-office was restarted")
	items, _ := e2.Inbox(ctx)
	if !strings.Contains(items[0].Body, s.Name+" (worker, agent id "+s.ID+"): it had not started its conversation yet") {
		t.Fatalf("notice %q", items[0].Body)
	}
	if got, _ := e2.Get(s.ID); got.HasTerminal || got.ExitReason != ExitDaemonRestart {
		t.Fatalf("starting agent %+v", got)
	}
}

// --restore-agents=false: nobody is resumed, and the states are forgotten,
// so a later start with restore on does not bring back old agents.
func TestRestoreOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	w, _ := e1.Start(ctx, StartRequest{})
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	e1.Shutdown()
	e2, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the states cleared", func() bool { return resumeStateOf(t, f, w.ID) == "" })
	e2.Shutdown()
	if got, _ := e2.Get(w.ID); got.HasTerminal {
		t.Fatalf("resumed with restore off: %+v", got)
	}
	f.opts.RestoreAgents = true
	e3 := f.engine(t)
	time.Sleep(300 * time.Millisecond)
	if got, _ := e3.Get(w.ID); got.HasTerminal {
		t.Fatalf("an older stop was restored: %+v", got)
	}
	if items, _ := e3.Inbox(ctx); len(items) != 0 {
		t.Fatalf("notice without a restore: %+v", items)
	}
}

// An idle agent restored twice (Ozan's test: a sandboxed worker showed
// "resume failed" after its second restart). The first restore resumes it
// without a prompt, so Claude Code writes no conversation for that fork; the
// second restore must continue the original conversation, not the empty fork.
func TestRestoreTwice(t *testing.T) {
	f := newFixture(t)
	f.opts.RestoreAgents = true
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	task, _ := e1.CreateTask(ctx, TaskInput{Title: "Twice"})
	w, err := e1.Start(ctx, StartRequest{TaskID: task.ID}) // a task: its first prompt is the conversation
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	e1.Shutdown()

	for run, wantFrom := range []string{w.SessionID, w.SessionID} {
		e, err := New(ctx, f.opts)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the restore", func() bool {
			a, _ := e.Get(w.ID)
			return a.State == string(hooks.Idle) && a.SessionID != w.SessionID && a.ResumedFrom != ""
		})
		a, _ := e.Get(w.ID)
		if a.ResumedFrom != wantFrom {
			t.Fatalf("restart %d resumed %s, want the original conversation %s", run+1, a.ResumedFrom, wantFrom)
		}
		screen(t, e, w.ID, "RESUMED:"+wantFrom)
		e.Shutdown()
		if a, _ := e.Get(w.ID); a.ExitReason == ExitResumeFailed {
			t.Fatalf("restart %d ended as a failed resume", run+1)
		}
	}
	// The user resumes it once more, with a prompt: still the original conversation.
	e, _ := New(ctx, func() Options { o := f.opts; o.RestoreAgents = false; return o }())
	t.Cleanup(e.Shutdown)
	r, err := e.Resume(ctx, w.ID, "go on")
	if err != nil || r.ResumedFrom != w.SessionID {
		t.Fatalf("resume after two restores: %+v %v", r, err)
	}
	screen(t, e, w.ID, "RESUMED:"+w.SessionID)
}

// A resumed session that got a prompt and was stopped before its turn
// finished is continued by the next resume, not skipped for its parent
// (Phase 17; ADR 0035 amended): Claude Code keeps a fork's transcript from
// its first prompt on. A fork that was only shown is still skipped.
func TestResumeContinuesInterruptedSession(t *testing.T) {
	f := newFixture(t)
	e, err := New(context.Background(), f.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	ctx := context.Background()
	log := record(t, e)
	w, _ := startAndExit(t, e, log, "Interrupted")
	original := w.SessionID

	// Resumed without a prompt, then given one and stopped mid-turn.
	r1, err := e.Resume(ctx, w.ID, "")
	if err != nil || r1.ResumedFrom != original {
		t.Fatalf("first resume: %+v %v", r1, err)
	}
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	typeLine(t, e, w.ID, "work") // UserPromptSubmit, then a tool waits for permission
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.WaitingPermission))
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentGone(w.ID))

	r2, err := e.Resume(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if r2.ResumedFrom != r1.SessionID {
		t.Fatalf("resume continued %s, want the interrupted session %s (original %s)", r2.ResumedFrom, r1.SessionID, original)
	}
	screen(t, e, w.ID, "RESUMED:"+r1.SessionID)

	// r2 was only shown: stopped without a prompt, the next resume skips it.
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentGone(w.ID))
	r3, err := e.Resume(ctx, w.ID, "")
	if err != nil || r3.ResumedFrom != r1.SessionID {
		t.Fatalf("a fork without a prompt was not skipped: %+v %v", r3, err)
	}
	if a, _ := e.Get(w.ID); a.ExitReason == ExitResumeFailed {
		t.Fatal("resume failed")
	}
}
