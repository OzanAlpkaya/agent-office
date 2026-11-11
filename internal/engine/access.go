package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Access requests (ADR 0019): an agent asks, a human decides, deterministic
// code grants. Creating a request grants nothing.

// Access request kinds and statuses (access_requests).
const (
	AccessDomain   = "domain"
	AccessPending  = "pending"
	AccessApproved = "approved"
	AccessDenied   = "denied"
	AccessExpired  = "expired"

	ScopeAgent   = "agent"   // an approval for the asking agent, every later session too (ADR 0053)
	ScopeProject = "project" // an approval for every agent of the project

	maxAccessReason  = 1000
	maxPendingAccess = 5
	maxAccessListed  = 100
)

// AccessRequest is the public view of an access request. Reason was written
// by the agent: render it as text only.
type AccessRequest struct {
	ID        int64      `json:"id"`
	AgentID   string     `json:"agent_id"`
	Kind      string     `json:"kind"`
	Value     string     `json:"value"`
	Reason    string     `json:"reason"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	DecidedBy string     `json:"decided_by,omitempty"`
}

func accessFromRow(r db.AccessRequest) AccessRequest {
	ar := AccessRequest{ID: r.ID, AgentID: r.AgentID, Kind: r.Kind, Value: r.Value, Reason: r.Reason, Status: r.Status,
		CreatedAt: r.CreatedAt.UTC(), DecidedBy: r.DecidedBy}
	if r.DecidedAt.Valid {
		t := r.DecidedAt.Time.UTC()
		ar.DecidedAt = &t
	}
	return ar
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
		return accessFromRow(r), nil // the same request is already waiting
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
	ar := accessFromRow(row)
	e.log.Info("access requested", "agent_id", caller, "request_id", ar.ID, "kind", kind, "value", value)
	e.publish(Update{Type: "access_request", AccessRequest: &ar})
	return ar, nil
}

// DecideAccess approves or denies a pending request; a human calls it from
// the UI. An approval grants the host to that running agent only, until it
// exits. A request is decided once.
func (e *Engine) DecideAccess(ctx context.Context, id int64, approve bool) (AccessRequest, error) {
	r, err := e.q.GetAccessRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessRequest{}, ErrNotFound
	}
	if err != nil {
		return AccessRequest{}, err
	}
	e.mu.Lock()
	a, ok := e.agents[r.AgentID]
	var grants *sandbox.Grants
	if ok && a.running() && a.sandbox != nil {
		grants = a.sandbox.grants
	}
	e.mu.Unlock()
	if !ok {
		return AccessRequest{}, ErrNotFound // another project's or a removed agent's request
	}
	if r.Status != AccessPending {
		return AccessRequest{}, ErrDecided
	}
	status := AccessDenied
	if approve {
		status = AccessApproved
		if grants == nil {
			e.mu.Lock()
			e.expireAccessLocked(ctx, r.AgentID)
			e.mu.Unlock()
			return AccessRequest{}, fmt.Errorf("%w: agent %s is no longer running; the request expired", ErrInvalid, r.AgentID)
		}
	}
	row, err := e.q.DecideAccessRequest(ctx, db.DecideAccessRequestParams{Status: status, DecidedAt: nullTime(now()),
		DecidedBy: "user", Scope: ScopeAgent, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return AccessRequest{}, ErrDecided // decided concurrently
	}
	if err != nil {
		return AccessRequest{}, err
	}
	if approve {
		if err := grants.Add(row.Value); err != nil {
			return AccessRequest{}, err
		}
	}
	ar := accessFromRow(row)
	e.log.Info("access request decided", "agent_id", ar.AgentID, "request_id", ar.ID, "status", ar.Status, "value", ar.Value)
	e.publish(Update{Type: "access_request", AccessRequest: &ar})
	verdict := "denied: do not retry; continue without it or explain in your report"
	if approve {
		verdict = "approved for this session: you can retry now"
	}
	body := fmt.Sprintf("Your access request #%d (network access to %s) was %s.", ar.ID, ar.Value, verdict)
	if _, err := e.sendMessage(ctx, "", ar.AgentID, body, false); err != nil {
		e.log.Warn("notify access decision", "agent_id", ar.AgentID, "err", err)
	}
	return ar, nil
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
		out = append(out, accessFromRow(r))
	}
	return out, nil
}

// expireAccessLocked expires an exited agent's pending requests: nothing can
// be granted to it any more.
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
