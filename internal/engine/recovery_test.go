package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// resumedFrom matches the agent's update once a new session continues an
// older one and is idle.
func resumedSession(id string) func(Update) bool {
	return func(u Update) bool {
		return u.Type == "agent" && u.Agent.ID == id && u.Agent.ResumedFrom != "" && u.Agent.State == string(hooks.Idle)
	}
}

func bodiesContaining(ms []Message, sub string) int {
	n := 0
	for _, m := range ms {
		if strings.Contains(m.Body, sub) {
			n++
		}
	}
	return n
}

// TestUnexpectedExitResumedOnce is incidents 1 and 2 (ADR 0051): a worker
// killed by a signal is told apart from a stop, resumed once with a message
// saying what happened, and its second failure goes to the lead with the
// reason and the tools to act, without another resume.
func TestUnexpectedExitResumedOnce(t *testing.T) {
	tf := newTeam(t)
	e, ctx := tf.e, context.Background()
	_, w := tf.worker(t, "Tags API")

	mark := tf.log.mark()
	typeLine(t, e, w.ID, "killself")
	ex := tf.log.waitFrom(t, startWait, mark, agentState(w.ID, hooks.Exited)).Agent
	if ex.ExitReason != ExitProcess || ex.ExitSignal != "KILL" || ex.OOMKilled || ex.ExitCode == nil || *ex.ExitCode != 137 ||
		!strings.Contains(ex.ExitDetail, "Killed by signal KILL") {
		t.Fatalf("exit %+v", ex)
	}
	tf.log.waitFrom(t, startWait, mark, resumedSession(w.ID))
	eventually(t, "the lead hears of the automatic resume", func() bool {
		return bodiesContaining(messagesTo(t, e, tf.lead.ID), "resumed its conversation automatically") == 1
	})
	got, _ := e.Get(w.ID)
	if got.ExitDetail != "" || got.ExitSignal != "" {
		t.Fatalf("a running agent keeps no exit detail: %+v", got)
	}

	mark = tf.log.mark()
	typeLine(t, e, w.ID, "killself")
	tf.log.waitFrom(t, startWait, mark, agentState(w.ID, hooks.Exited))
	eventually(t, "the lead hears of the second failure", func() bool {
		return bodiesContaining(messagesTo(t, e, tf.lead.ID), "died again") == 1
	})
	m := messagesTo(t, e, tf.lead.ID)
	for _, msg := range m {
		if strings.Contains(msg.Body, "died again") && (!strings.Contains(msg.Body, "Killed by signal KILL") ||
			!strings.Contains(msg.Body, `resume_agent(agent_id="`+w.ID+`"`) || !strings.Contains(msg.Body, "reclaim_task")) {
			t.Fatalf("second-failure message %q", msg.Body)
		}
	}
	got, _ = e.Get(w.ID)
	if got.State != string(hooks.Exited) || !got.Resumable {
		t.Fatalf("after the second failure: %+v", got)
	}
	var n int
	if err := tf.f.store.DB.QueryRow(`SELECT auto_resumes FROM agents WHERE id = ?`, w.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("auto_resumes %d %v", n, err)
	}
	_ = ctx
}

// TestStopIsNotAFailure: an agent the user stops is neither resumed nor
// reported as a failure.
func TestStopIsNotAFailure(t *testing.T) {
	tf := newTeam(t)
	_, w := tf.worker(t, "Tags API")
	if err := tf.e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := tf.e.Get(w.ID)
	if got.ExitReason != ExitStopped || got.ExitDetail != "Stopped by the user." || got.ResumedFrom != "" {
		t.Fatalf("stopped agent %+v", got)
	}
	if n := bodiesContaining(messagesTo(t, tf.e, tf.lead.ID), "died"); n != 0 {
		t.Fatalf("lead told about a stop: %d", n)
	}
}

// TestCleanExitGoesToTheLead: an exit 0 without a report is not retried; the
// lead is told with the next steps.
func TestCleanExitGoesToTheLead(t *testing.T) {
	tf := newTeam(t)
	_, w := tf.worker(t, "Tags API")
	typeLine(t, tf.e, w.ID, "exit")
	eventually(t, "the lead hears of the exit", func() bool {
		return bodiesContaining(messagesTo(t, tf.e, tf.lead.ID), "ended without reporting") == 1
	})
	got, _ := tf.e.Get(w.ID)
	if got.State != string(hooks.Exited) || got.ResumedFrom != "" || !strings.Contains(got.ExitDetail, "exit code 0") {
		t.Fatalf("after a clean exit: %+v", got)
	}
}

// TestExitSavesUncommittedWork is incident 6 (ADR 0051): a host worker's
// uncommitted changes are committed on its branch when it dies, the resumed
// session is told, and the message to it waits for the resume.
func TestExitSavesUncommittedWork(t *testing.T) {
	tf := newTeam(t)
	gitIn(t, tf.f.repo, "config", "user.name", "Ozan Test")
	gitIn(t, tf.f.repo, "config", "user.email", "ozan@example.com")
	e := tf.e
	_, w := tf.worker(t, "Tags API")
	typeLine(t, e, w.ID, "dirty")
	eventually(t, "the fake writes its file", func() bool {
		_, err := os.Stat(filepath.Join(w.WorktreePath, "agent-output.txt"))
		return err == nil
	})
	mark := tf.log.mark()
	typeLine(t, e, w.ID, "crash")
	tf.log.waitFrom(t, startWait, mark, resumedSession(w.ID))
	if out := gitIn(t, w.WorktreePath, "status", "--porcelain"); out != "" {
		t.Fatalf("worktree still dirty: %q", out)
	}
	if msg := gitIn(t, tf.f.repo, "log", "-1", "--format=%s%n%b", w.Branch); !strings.Contains(msg, wipMessage) ||
		!strings.Contains(msg, "Co-Authored-By: ") {
		t.Fatalf("saved commit %q", msg)
	}
	if gitIn(t, tf.f.repo, "log", "-1", "--format=%an", w.Branch) != "Ozan Test" {
		t.Fatal("saved as someone else")
	}
	// The notice was queued while it was exited and went into the resumed
	// session's first prompt.
	if !deliveredWith(t, e, w.ID, "committed the changes you had not committed", DeliveryPrompt) {
		t.Fatal("the saved-work notice was not given to the resumed session")
	}
	eventually(t, "the lead hears that the work was saved", func() bool {
		return bodiesContaining(messagesTo(t, e, tf.lead.ID), "its uncommitted work was saved as commit") == 1
	})
}

// TestMessageToStoppedWorkerWaits is incident 4: the lead's message to a
// worker that is not running is kept and delivered once it is resumed.
func TestMessageToStoppedWorkerWaits(t *testing.T) {
	tf := newTeam(t)
	e, ctx := tf.e, context.Background()
	_, w := tf.worker(t, "Tags API")
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	out, err := tf.team.SendMessage(ctx, tf.lead.ID, mcp.SendMessageInput{To: w.ID, Body: "use the v2 schema"})
	if err != nil || out.Status != "waiting_for_resume" || !strings.Contains(out.Note, "Stopped by the user") {
		t.Fatalf("send to stopped worker: %+v %v", out, err)
	}
	if _, err := e.Resume(ctx, w.ID, ""); err != nil {
		t.Fatal(err)
	}
	screen(t, e, w.ID, "use the v2 schema")
	eventually(t, "the message is marked delivered", func() bool {
		for _, m := range messagesTo(t, e, w.ID) {
			if m.Body == "use the v2 schema" && m.DeliveredAt != nil {
				return true
			}
		}
		return false
	})
}

// TestHostAutosaveRefusesAgentGitConfig: git config that runs a command on
// status or add (here core.fsmonitor) keeps the daemon from running git in the
// worktree unasked; the work stays uncommitted and is kept (security review).
func TestHostAutosaveRefusesAgentGitConfig(t *testing.T) {
	tf := newTeam(t)
	gitIn(t, tf.f.repo, "config", "user.name", "Ozan Test")
	gitIn(t, tf.f.repo, "config", "user.email", "ozan@example.com")
	e := tf.e
	_, w := tf.worker(t, "Tags API")
	marker := filepath.Join(t.TempDir(), "ran")
	gitIn(t, w.WorktreePath, "config", "core.fsmonitor", "touch "+marker+"; false")
	typeLine(t, e, w.ID, "dirty")
	eventually(t, "the fake writes its file", func() bool {
		_, err := os.Stat(filepath.Join(w.WorktreePath, "agent-output.txt"))
		return err == nil
	})
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	done := e.agents[w.ID].afterExit
	e.mu.Unlock()
	<-done                                                     // the exit's saved work ran (and refused)
	gitIn(t, tf.f.repo, "config", "--unset", "core.fsmonitor") // for gitIn below
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the agent's fsmonitor command ran")
	}
	if log := gitIn(t, tf.f.repo, "log", "-1", "--format=%s", w.Branch); strings.Contains(log, wipMessage) {
		t.Fatal("saved despite the agent's git config")
	}
}

// TestRelaunchGetsMessagesQueuedWhileEnding: a message sent while the worker
// was stopped reaches a relaunch (not only a resume) (code review).
func TestRelaunchGetsMessagesQueuedWhileEnding(t *testing.T) {
	tf := newTeam(t)
	e, ctx := tf.e, context.Background()
	_, w := tf.worker(t, "Tags API")
	if err := e.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.team.SendMessage(ctx, tf.lead.ID, mcp.SendMessageInput{To: w.ID, Body: "check the schema first"}); err != nil {
		t.Fatal(err)
	}
	mark := tf.log.mark()
	if _, err := e.relaunch(ctx, w.ID, "[agent-office] follow-up", nil); err != nil {
		t.Fatal(err)
	}
	tf.log.waitFrom(t, startWait, mark, agentState(w.ID, hooks.Idle))
	if !deliveredWith(t, e, w.ID, "check the schema first", DeliveryPrompt) {
		t.Fatal("the waiting message was not in the relaunch's first prompt")
	}
}

// deliveredWith reports whether a message to id containing sub was delivered
// by way.
func deliveredWith(t *testing.T, e *Engine, id, sub, way string) bool {
	t.Helper()
	for _, m := range messagesTo(t, e, id) {
		if strings.Contains(m.Body, sub) && m.DeliveredAt != nil && m.Delivery == way {
			return true
		}
	}
	return false
}
