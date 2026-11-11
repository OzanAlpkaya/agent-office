package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// TestLeadResumesAndStopsItsWorkers is incident 3 (ADR 0051): the lead stops
// and resumes workers of its goal, with its message as the first prompt;
// nobody else can, and not for another goal's workers.
func TestLeadResumesAndStopsItsWorkers(t *testing.T) {
	tf := newTeam(t)
	e, tm, ctx := tf.e, tf.team, context.Background()
	task, w := tf.worker(t, "Tags API")

	if _, err := tm.StopAgent(ctx, w.ID, mcp.StopAgentInput{AgentID: tf.lead.ID, Reason: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("a worker stopping its lead: %v", err)
	}
	if _, err := tm.StopAgent(ctx, tf.lead.ID, mcp.StopAgentInput{AgentID: w.ID}); !isUser(err, ErrInvalid) {
		t.Fatalf("stop without a reason: %v", err)
	}
	out, err := tm.StopAgent(ctx, tf.lead.ID, mcp.StopAgentInput{AgentID: w.ID, Reason: "wrong approach"})
	if err != nil || out.State != string(hooks.Exited) || !strings.Contains(out.Next, "resume_agent") {
		t.Fatalf("stop: %+v %v", out, err)
	}
	got, _ := e.Get(w.ID)
	if got.ExitReason != ExitStoppedByLead || got.ExitDetail != "Stopped by the Tech Lead: wrong approach" || got.ResumedFrom != "" {
		t.Fatalf("stopped by the lead: %+v", got)
	}
	if bodiesContaining(messagesTo(t, e, tf.lead.ID), "died") != 0 {
		t.Fatal("a stop by the lead is reported as a failure")
	}
	if tk, _ := e.getTask(ctx, task.ID); tk.Status != StatusInProgress {
		t.Fatalf("task after the stop: %s", tk.Status)
	}

	res, err := tm.ResumeAgent(ctx, tf.lead.ID, mcp.ResumeAgentInput{AgentID: w.ID, Message: "use the v2 schema"})
	if err != nil || res.AgentID != w.ID {
		t.Fatalf("resume: %+v %v", res, err)
	}
	tf.log.wait(t, startWait, resumedSession(w.ID))
	if _, err := tm.ResumeAgent(ctx, tf.lead.ID, mcp.ResumeAgentInput{AgentID: w.ID}); !isUser(err, ErrInvalid) {
		t.Fatalf("resume a running worker: %v", err)
	}

	// Another goal's worker is out of reach.
	other, err := e.CreateTask(ctx, TaskInput{Title: "Other goal"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := e.createTask(ctx, TaskInput{Title: "Other sub-task"}, other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	ow, err := e.Start(ctx, StartRequest{TaskID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(ow.ID, hooks.Idle))
	if _, err := tm.StopAgent(ctx, tf.lead.ID, mcp.StopAgentInput{AgentID: ow.ID, Reason: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("stop another goal's worker: %v", err)
	}
	if _, err := tm.ResumeAgent(ctx, w.ID, mcp.ResumeAgentInput{AgentID: w.ID}); !isUser(err, ErrForbidden) {
		t.Fatalf("a worker using resume_agent: %v", err)
	}
}

// TestReclaimTaskContinuesFromTheBranch: a sub-task taken back from an exited
// worker is assigned again, and the new worker starts from the old one's
// commits.
func TestReclaimTaskContinuesFromTheBranch(t *testing.T) {
	tf := newTeam(t)
	e, tm, ctx := tf.e, tf.team, context.Background()
	task, w := tf.worker(t, "Tags API")
	if _, err := tm.ReclaimTask(ctx, tf.lead.ID, mcp.ReclaimTaskInput{TaskID: task.ID}); !isUser(err, ErrInvalid) {
		t.Fatalf("reclaim with a running worker: %v", err)
	}
	typeLine(t, e, w.ID, "commit")
	screen(t, e, w.ID, "committed")
	head := gitIn(t, tf.f.repo, "rev-parse", w.Branch)
	if _, err := tm.StopAgent(ctx, tf.lead.ID, mcp.StopAgentInput{AgentID: w.ID, Reason: "too slow"}); err != nil {
		t.Fatal(err)
	}
	out, err := tm.ReclaimTask(ctx, tf.lead.ID, mcp.ReclaimTaskInput{TaskID: task.ID})
	if err != nil || out.Status != StatusTodo || out.FromBranch != w.Branch {
		t.Fatalf("reclaim: %+v %v", out, err)
	}
	if _, err := tm.ReclaimTask(ctx, w.ID, mcp.ReclaimTaskInput{TaskID: task.ID}); !isUser(err, ErrForbidden) {
		t.Fatalf("a worker reclaiming: %v", err)
	}
	as, err := tm.AssignTask(ctx, tf.lead.ID, mcp.AssignTaskInput{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	tf.log.wait(t, startWait, agentState(as.AgentID, hooks.Idle))
	nw, _ := e.Get(as.AgentID)
	if nw.ID == w.ID || gitIn(t, nw.WorktreePath, "rev-parse", "HEAD") != head {
		t.Fatalf("the new worker does not start from the old branch (%s)", head)
	}
}

// TestLeadDecidesAccess is incident 7 (ADR 0053): with lead_policy, a
// worker's request for a host in the policy goes to the Tech Lead, which
// approves it for the project with a recorded reason; a host outside the
// policy stays the user's to decide; the approval reaches every running agent
// and every later session.
func TestLeadDecidesAccess(t *testing.T) {
	tf := newTeamWith(t, func(o *Options) {
		o.AccessDecider, o.LeadAccessHosts = DeciderLeadPolicy, []string{"storage.googleapis.com", ".example.org"}
	})
	e, tm, ctx := tf.e, tf.team, context.Background()
	_, w := tf.worker(t, "Fetch modules")
	_, w2 := tf.worker(t, "Docs")
	g, g2 := fakeSandboxed(t, e, w.ID), fakeSandboxed(t, e, w2.ID)

	r, err := tm.RequestAccess(ctx, w.ID, mcp.RequestAccessInput{Kind: "domain", Value: "storage.googleapis.com", Reason: "module zips"})
	if err != nil || !strings.Contains(r.Next, "Tech Lead decides") {
		t.Fatalf("request: %+v %v", r, err)
	}
	eventually(t, "the lead is asked", func() bool {
		return bodiesContaining(messagesTo(t, e, tf.lead.ID), "decide_access(request_id=") == 1
	})
	if _, err := tm.DecideAccess(ctx, w.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true, Reason: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("a worker deciding: %v", err)
	}
	if _, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true}); !isUser(err, ErrInvalid) {
		t.Fatalf("a decision without a reason: %v", err)
	}
	out, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true,
		Reason: "proxy.golang.org serves module zips from there", Scope: ScopeProject})
	if err != nil || out.Status != AccessApproved || out.Scope != ScopeProject {
		t.Fatalf("lead approval: %+v %v", out, err)
	}
	if !g.Allows("storage.googleapis.com") || !g2.Allows("storage.googleapis.com") {
		t.Fatal("a project-wide approval must reach every running agent")
	}
	list, _ := e.AccessRequests(ctx)
	if list[0].DecidedBy != DecidedByLead || list[0].DeciderAgentID != tf.lead.ID || list[0].DecisionReason == "" {
		t.Fatalf("recorded decision %+v", list[0])
	}
	items, _ := e.Inbox(ctx)
	if len(items) == 0 || !strings.Contains(items[0].Body, "approved access request #") {
		t.Fatalf("the user is not told: %+v", items)
	}
	// A later session of any agent of the project gets it.
	later := &sandbox.Grants{}
	if err := e.loadGrants(ctx, w2.ID, later); err != nil || !later.Allows("storage.googleapis.com") {
		t.Fatalf("later session: %v", err)
	}

	// Outside the policy: the user decides; the lead may only deny.
	r2, err := tm.RequestAccess(ctx, w.ID, mcp.RequestAccessInput{Kind: "domain", Value: "pastebin.com", Reason: "upload"})
	if err != nil || strings.Contains(r2.Next, "Tech Lead") {
		t.Fatalf("request outside the policy: %+v %v", r2, err)
	}
	if _, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r2.RequestID, Approve: true, Reason: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("lead approving outside the policy: %v", err)
	}
	if out, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r2.RequestID, Reason: "not needed"}); err != nil || out.Status != AccessDenied {
		t.Fatalf("lead denial: %+v %v", out, err)
	}
}

// TestUserDecidesByDefault: without access_decider the lead has no say.
func TestUserDecidesByDefault(t *testing.T) {
	tf := newTeam(t)
	e, tm, ctx := tf.e, tf.team, context.Background()
	_, w := tf.worker(t, "Fetch modules")
	fakeSandboxed(t, e, w.ID)
	r, err := tm.RequestAccess(ctx, w.ID, mcp.RequestAccessInput{Kind: "domain", Value: "storage.googleapis.com", Reason: "zips"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true, Reason: "x"}); !isUser(err, ErrForbidden) {
		t.Fatalf("lead deciding with access_decider user: %v", err)
	}
	// The request #4 bug: an approval was held by the session that asked only.
	if _, err := e.DecideAccess(ctx, r.RequestID, true); err != nil {
		t.Fatal(err)
	}
	next := &sandbox.Grants{}
	if err := e.loadGrants(ctx, w.ID, next); err != nil || !next.Allows("storage.googleapis.com") {
		t.Fatalf("the approval does not reach the next session: %v", err)
	}
}

// TestLeadProjectScopeNeedsThePolicy: with access_decider lead, the Tech Lead
// approves for one worker; a project-wide grant (which later QA and
// reviewers get too) only for hosts the user listed (security review).
func TestLeadProjectScopeNeedsThePolicy(t *testing.T) {
	tf := newTeamWith(t, func(o *Options) { o.AccessDecider = DeciderLead })
	e, tm, ctx := tf.e, tf.team, context.Background()
	_, w := tf.worker(t, "Fetch modules")
	fakeSandboxed(t, e, w.ID)
	r, err := tm.RequestAccess(ctx, w.ID, mcp.RequestAccessInput{Kind: "domain", Value: "docs.example.net", Reason: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true, Reason: "docs",
		Scope: ScopeProject}); !isUser(err, ErrForbidden) {
		t.Fatalf("project-wide approval outside the policy: %v", err)
	}
	out, err := tm.DecideAccess(ctx, tf.lead.ID, mcp.DecideAccessInput{RequestID: r.RequestID, Approve: true, Reason: "docs"})
	if err != nil || out.Scope != ScopeAgent {
		t.Fatalf("approval for the worker: %+v %v", out, err)
	}
}
