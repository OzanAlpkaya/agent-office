package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Access requests (ADR 0019, 0053): an agent asks; the user decides, or the
// goal's Tech Lead when the project lets it (access_decider), with a recorded
// reason; deterministic code grants. Creating a request grants nothing. An
// approval is a grant that outlives the session that asked: for the asking
// agent, or for every agent of the project.

// Access request kinds and statuses (access_requests).
const (
	AccessDomain   = "domain"
	AccessPending  = "pending"
	AccessApproved = "approved"
	AccessDenied   = "denied"
	AccessExpired  = "expired"

	ScopeAgent   = "agent"   // an approval for the asking agent, every later session too (ADR 0053)
	ScopeProject = "project" // an approval for every agent of the project

	DecidedByUser = "user"
	DecidedByLead = "lead"

	maxAccessReason  = 1000
	maxPendingAccess = 5
	maxAccessListed  = 100
)

// AccessRequest is the public view of an access request. Reason was written
// by the agent, DecisionReason by the user or the Tech Lead: render them as
// text only.
type AccessRequest struct {
	ID             int64      `json:"id"`
	AgentID        string     `json:"agent_id"`
	Kind           string     `json:"kind"`
	Value          string     `json:"value"`
	Reason         string     `json:"reason"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	DecidedBy      string     `json:"decided_by,omitempty"`       // user | lead | agent-office
	DeciderAgentID string     `json:"decider_agent_id,omitempty"` // the Tech Lead that decided
	DecisionReason string     `json:"decision_reason,omitempty"`
	Scope          string     `json:"scope,omitempty"`   // an approval's scope: agent | project
	Decider        string     `json:"decider,omitempty"` // a pending request: who is asked (user | lead)
}

func accessFromRow(r db.AccessRequest) AccessRequest {
	ar := AccessRequest{ID: r.ID, AgentID: r.AgentID, Kind: r.Kind, Value: r.Value, Reason: r.Reason, Status: r.Status,
		CreatedAt: r.CreatedAt.UTC(), DecidedBy: r.DecidedBy, DeciderAgentID: r.DeciderAgentID, DecisionReason: r.DecisionReason}
	if r.Status == AccessApproved {
		ar.Scope = r.Scope
	}
	if r.DecidedAt.Valid {
		t := r.DecidedAt.Time.UTC()
		ar.DecidedAt = &t
	}
	return ar
}

// decider is who decides the project's access requests.
func (e *Engine) decider() string {
	if d := e.opts.AccessDecider; d != "" {
		return d
	}
	return DeciderUser
}

// leadDecides returns the Tech Lead that decides a request for host from
// agent, or nil when the user decides: the project's setting, the host
// policy, and a running lead above a worker (never the lead's own request).
func (e *Engine) leadDecides(ctx context.Context, agentID, host string) *agentProc {
	d := e.decider()
	if d == DeciderUser || (d == DeciderLeadPolicy && !e.leadHosts.Allows(host)) {
		return nil
	}
	e.mu.Lock()
	a, ok := e.agents[agentID]
	var kind, taskID string
	if ok {
		kind, taskID = a.info.Kind, a.info.TaskID
	}
	e.mu.Unlock()
	if kind != KindWorker || taskID == "" {
		return nil
	}
	t, err := e.getTask(ctx, taskID)
	if err != nil || !t.ParentTaskID.Valid {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leadOfGoalLocked(t.ParentTaskID.String)
}

// requestAccess records a pending request from agent caller.
func (e *Engine) requestAccess(ctx context.Context, caller, kind, value, reason string) (AccessRequest, error) {
	if kind != AccessDomain {
		return AccessRequest{}, invalid(`kind must be "domain"; other kinds of access are not available yet`)
	}
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if !sandbox.ValidHost(value) {
		return AccessRequest{}, invalid("value must be one exact host name such as docs.example.com (no scheme, port, IP address or wildcard)")
	}
	reason, err := normalizeText(reason, maxAccessReason, "reason")
	if err != nil {
		return AccessRequest{}, err
	}
	e.mu.Lock()
	a, err := e.callerLocked(caller)
	if err == nil {
		switch {
		case a.info.Sandbox != SandboxPodman || a.sandbox == nil || e.opts.Sandbox == nil:
			err = invalid("host-mode agents are not network-restricted; nothing to request")
		case e.opts.Sandbox.Allow.Allows(value) || a.sandbox.grants.Allows(value):
			err = invalid("%s is already allowed for you", value)
		}
	}
	e.mu.Unlock()
	if err != nil {
		return AccessRequest{}, err
	}
	if r, err := e.q.FindPendingAccessRequest(ctx, db.FindPendingAccessRequestParams{AgentID: caller, Kind: kind, Value: value}); err == nil {
		return e.withDecider(ctx, accessFromRow(r)), nil // the same request is already waiting
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AccessRequest{}, err
	}
	n, err := e.q.CountPendingAccessRequests(ctx, caller)
	if err != nil {
		return AccessRequest{}, err
	}
	if n >= maxPendingAccess {
		return AccessRequest{}, fmt.Errorf("%w: at most %d pending access requests per agent", ErrLimit, maxPendingAccess)
	}
	row, err := e.q.InsertAccessRequest(ctx, db.InsertAccessRequestParams{AgentID: caller, Kind: kind, Value: value,
		Reason: reason, CreatedAt: now()})
	if err != nil {
		return AccessRequest{}, err
	}
	ar := e.withDecider(ctx, accessFromRow(row))
	e.log.Info("access requested", "agent_id", caller, "request_id", ar.ID, "kind", kind, "value", value, "decider", ar.Decider)
	e.publish(Update{Type: "access_request", AccessRequest: &ar})
	if ar.Decider == DecidedByLead {
		e.askLead(ctx, ar)
	}
	return ar, nil
}

// withDecider fills in who decides a pending request.
func (e *Engine) withDecider(ctx context.Context, ar AccessRequest) AccessRequest {
	if ar.Status != AccessPending {
		return ar
	}
	lead := e.leadDecides(ctx, ar.AgentID, ar.Value)
	ar.Decider = DecidedByUser
	if lead != nil {
		ar.Decider = DecidedByLead
	}
	return ar
}

// askLead tells the deciding Tech Lead about a request.
func (e *Engine) askLead(ctx context.Context, ar AccessRequest) {
	lead := e.leadDecides(ctx, ar.AgentID, ar.Value)
	if lead == nil {
		return
	}
	e.mu.Lock()
	who := e.labelLocked(ar.AgentID)
	e.mu.Unlock()
	body := fmt.Sprintf("Access request #%d from worker %s: network access to %s. Its reason, written by the worker: %s. "+
		"The project lets you decide: call decide_access(request_id=%d, approve=true or false, reason=\"...\", "+
		"scope=\"agent\" or \"project\"). Approve only what its sub-task needs; the user sees your decision and reason.",
		ar.ID, who, ar.Value, jsonString(ar.Reason), ar.ID)
	if _, err := e.sendMessage(ctx, "", lead.info.ID, body, false); err != nil {
		e.log.Warn("ask the lead about an access request", "request_id", ar.ID, "err", err)
	}
}

// accessDecision is one decision on a request.
type accessDecision struct {
	approve bool
	by      string // DecidedByUser | DecidedByLead
	leadID  string // the deciding lead
	reason  string // optional for the user, required for the lead
	scope   string // ScopeAgent | ScopeProject (approvals)
}

// DecideAccess approves or denies a pending request for the asking agent; the
// user calls it from the UI (ADR 0019).
func (e *Engine) DecideAccess(ctx context.Context, id int64, approve bool) (AccessRequest, error) {
	return e.DecideAccessAs(ctx, id, approve, ScopeAgent, "")
}

// DecideAccessAs is the user's decision with a scope and an optional reason
// (ADR 0053).
func (e *Engine) DecideAccessAs(ctx context.Context, id int64, approve bool, scope, reason string) (AccessRequest, error) {
	if reason != "" {
		var err error
		if reason, err = normalizeText(reason, maxAccessReason, "reason"); err != nil {
			return AccessRequest{}, err
		}
	}
	return e.decideAccess(ctx, id, accessDecision{approve: approve, by: DecidedByUser, reason: reason, scope: scope})
}

func (e *Engine) decideAccess(ctx context.Context, id int64, d accessDecision) (AccessRequest, error) {
	if d.scope == "" {
		d.scope = ScopeAgent
	}
	if d.scope != ScopeAgent && d.scope != ScopeProject {
		return AccessRequest{}, invalid("scope must be %q or %q", ScopeAgent, ScopeProject)
	}
	r, err := e.q.GetAccessRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessRequest{}, ErrNotFound
	}
	if err != nil {
		return AccessRequest{}, err
	}
	e.mu.Lock()
	a, ok := e.agents[r.AgentID]
	future := ok && (a.running() || holdsMessages(a))
	e.mu.Unlock()
	if !ok {
		return AccessRequest{}, ErrNotFound // another project's or a removed agent's request
	}
	if r.Status != AccessPending {
		return AccessRequest{}, ErrDecided
	}
	if d.approve && !future {
		e.mu.Lock()
		e.expireAccessLocked(ctx, r.AgentID)
		e.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("%w: agent %s ended and cannot be resumed; the request expired", ErrInvalid, r.AgentID)
	}
	status := AccessDenied
	if d.approve {
		status = AccessApproved
	}
	var row db.AccessRequest
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		row, err = q.DecideAccessRequest(ctx, db.DecideAccessRequestParams{Status: status, DecidedAt: nullTime(now()),
			DecidedBy: d.by, DeciderAgentID: d.leadID, DecisionReason: d.reason, Scope: d.scope, ID: id})
		if err != nil || !d.approve {
			return err
		}
		agentID := sql.NullString{String: r.AgentID, Valid: d.scope == ScopeAgent}
		return q.InsertAccessGrant(ctx, db.InsertAccessGrantParams{ProjectID: e.project.ID, AgentID: agentID, Host: r.Value,
			RequestID: id, CreatedAt: now()})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return AccessRequest{}, ErrDecided // decided concurrently
	}
	if err != nil {
		return AccessRequest{}, err
	}
	if d.approve {
		e.applyGrant(r.AgentID, r.Value, d.scope)
	}
	ar := accessFromRow(row)
	e.log.Info("access request decided", "agent_id", ar.AgentID, "request_id", ar.ID, "status", ar.Status, "value", ar.Value,
		"by", d.by, "lead", d.leadID, "scope", ar.Scope)
	e.publish(Update{Type: "access_request", AccessRequest: &ar})
	who := "the user"
	if d.by == DecidedByLead {
		who = "your Tech Lead"
	}
	verdict := "denied by " + who + ": do not retry; continue without it or explain in your report"
	if d.approve {
		verdict = "approved by " + who + ": you can retry now; it stays allowed for you in later sessions"
		if d.scope == ScopeProject {
			verdict = "approved by " + who + " for every agent of the project: you can retry now"
		}
	}
	body := fmt.Sprintf("Your access request #%d (network access to %s) was %s.", ar.ID, ar.Value, verdict)
	if d.by == DecidedByLead && d.reason != "" {
		body += " Its reason: " + jsonString(d.reason)
	}
	if _, err := e.sendMessage(ctx, "", ar.AgentID, body, false); err != nil {
		e.log.Warn("notify access decision", "agent_id", ar.AgentID, "err", err)
	}
	if d.by == DecidedByLead {
		e.noteLeadDecision(ctx, ar)
	}
	return ar, nil
}

// noteLeadDecision tells the user what the Tech Lead decided, and why.
func (e *Engine) noteLeadDecision(ctx context.Context, ar AccessRequest) {
	e.mu.Lock()
	lead, asker := e.labelLocked(ar.DeciderAgentID), e.labelLocked(ar.AgentID)
	var taskID string
	if a, ok := e.agents[ar.AgentID]; ok {
		taskID = a.info.TaskID
	}
	e.mu.Unlock()
	scope := ""
	if ar.Status == AccessApproved {
		scope = " for " + map[string]string{ScopeAgent: "that worker", ScopeProject: "every agent of the project"}[ar.Scope]
	}
	body := fmt.Sprintf("Tech Lead %s %s access request #%d from %s: %s%s. Its reason: %s", lead, ar.Status, ar.ID, asker,
		ar.Value, scope, jsonString(ar.DecisionReason))
	goal := ""
	if taskID != "" {
		if t, err := e.getTask(ctx, taskID); err == nil {
			goal = goalOfTask(t)
		}
	}
	if _, err := e.addInbox(ctx, ar.DeciderAgentID, taskID, goal, RoleTechLead, InboxInfo, body); err != nil {
		e.log.Warn("note the lead's access decision", "request_id", ar.ID, "err", err)
	}
}

// applyGrant adds an approved host to the running sessions it covers; later
// launches load it from access_grants.
func (e *Engine) applyGrant(agentID, host, scope string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, a := range e.agents {
		if (scope == ScopeAgent && id != agentID) || !a.running() || a.sandbox == nil {
			continue
		}
		if err := a.sandbox.grants.Add(host); err != nil {
			e.log.Warn("apply access grant", "agent_id", id, "err", err)
		}
	}
}

// loadGrants fills a launching agent's grants: its project's and its own
// (ADR 0053). That is what makes an approval survive a resume.
func (e *Engine) loadGrants(ctx context.Context, agentID string, g *sandbox.Grants) error {
	rows, err := e.q.ListAccessGrants(ctx, db.ListAccessGrantsParams{ProjectID: e.project.ID, AgentID: sql.NullString{String: agentID, Valid: true}})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := g.Add(r.Host); err != nil {
			return err
		}
	}
	return nil
}

// AccessRequests lists the project's access requests, pending first, then
// the most recent.
func (e *Engine) AccessRequests(ctx context.Context) ([]AccessRequest, error) {
	rows, err := e.q.ListAccessRequests(ctx, db.ListAccessRequestsParams{ProjectID: e.project.ID, Limit: maxAccessListed})
	if err != nil {
		return nil, fmt.Errorf("engine: list access requests: %w", err)
	}
	out := make([]AccessRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, e.withDecider(ctx, accessFromRow(r)))
	}
	return out, nil
}

// expireAccessLocked expires an agent's pending requests: nothing can be
// granted to it any more (it was removed, or it ended for good).
func (e *Engine) expireAccessLocked(ctx context.Context, agentID string) {
	rows, err := e.q.ExpireAccessRequests(ctx, db.ExpireAccessRequestsParams{DecidedAt: nullTime(now()), AgentID: agentID})
	if err != nil {
		e.log.Warn("expire access requests", "agent_id", agentID, "err", err)
		return
	}
	for _, r := range rows {
		ar := accessFromRow(r)
		e.publishLocked(Update{Type: "access_request", AccessRequest: &ar})
	}
}

func (t team) RequestAccess(ctx context.Context, caller string, in mcp.RequestAccessInput) (mcp.RequestAccessOutput, error) {
	r, err := t.e.requestAccess(ctx, caller, in.Kind, in.Value, in.Reason)
	if err != nil {
		return mcp.RequestAccessOutput{}, userErr(err)
	}
	next := "A human decides in the agent-office UI; you get a message with the decision. Continue with other work or end your turn."
	if r.Decider == DecidedByLead {
		next = "Your Tech Lead decides (the user can too); you get a message with the decision. Continue with other work or end your turn."
	}
	return mcp.RequestAccessOutput{RequestID: r.ID, Status: r.Status, Next: next}, nil
}

func (t team) DecideAccess(ctx context.Context, caller string, in mcp.DecideAccessInput) (mcp.DecideAccessOutput, error) {
	out, err := t.e.leadDecideAccess(ctx, caller, in)
	return out, userErr(err)
}

// leadDecideAccess is decide_access: the Tech Lead decides a request of a
// worker of its goal, when the project lets it and, with lead_policy, for a
// host in the policy. A denial is always the lead's to make.
func (e *Engine) leadDecideAccess(ctx context.Context, caller string, in mcp.DecideAccessInput) (mcp.DecideAccessOutput, error) {
	lead, goal, err := e.asLead(ctx, caller, mcp.ToolDecideAccess)
	if err != nil {
		return mcp.DecideAccessOutput{}, err
	}
	if e.decider() == DeciderUser {
		return mcp.DecideAccessOutput{}, forbidden("this project lets the user decide access requests (access_decider: user)")
	}
	reason, err := normalizeText(in.Reason, maxAccessReason, "reason")
	if err != nil {
		return mcp.DecideAccessOutput{}, err
	}
	r, err := e.q.GetAccessRequest(ctx, in.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return mcp.DecideAccessOutput{}, fmt.Errorf("access request %d: %w", in.RequestID, ErrNotFound)
	}
	if err != nil {
		return mcp.DecideAccessOutput{}, err
	}
	if _, _, err := e.workerOfGoal(ctx, goal, r.AgentID); err != nil {
		return mcp.DecideAccessOutput{}, forbidden("access request %d is not from a worker of your goal", in.RequestID)
	}
	if in.Approve && e.decider() == DeciderLeadPolicy && !e.leadHosts.Allows(r.Value) {
		return mcp.DecideAccessOutput{}, forbidden("%s is outside the hosts this project lets you approve; the user decides "+
			"it in the UI (you may deny it)", r.Value)
	}
	scope := strings.TrimSpace(in.Scope)
	if in.Approve && scope == ScopeProject && !e.leadHosts.Allows(r.Value) {
		// A project-wide grant reaches every later agent, QA and reviewers too:
		// the Tech Lead gives one only for hosts the user listed.
		return mcp.DecideAccessOutput{}, forbidden("an approval for the whole project is for hosts in the project's lead_access_hosts "+
			"only; approve %s with scope \"agent\" for this worker", r.Value)
	}
	ar, err := e.decideAccess(ctx, in.RequestID, accessDecision{approve: in.Approve, by: DecidedByLead, leadID: lead.info.ID,
		reason: reason, scope: scope})
	if err != nil {
		return mcp.DecideAccessOutput{}, err
	}
	return mcp.DecideAccessOutput{RequestID: ar.ID, Status: ar.Status, Scope: ar.Scope,
		Next: "Recorded; the worker is told and the user sees your decision and reason. End your turn or continue."}, nil
}
