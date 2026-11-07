package engine

import (
	"context"
	"log/slog"
	"sync"
)

// sessionIDs maps a running agent's id to its current session and task, for
// sessionHandler.
type sessionIDs struct{ m sync.Map } // agent id -> sessionRef

type sessionRef struct{ session, task string }

func (s *sessionIDs) set(agentID, sessionID, taskID string) {
	s.m.Store(agentID, sessionRef{sessionID, taskID})
}

func (s *sessionIDs) get(agentID string) (sessionRef, bool) {
	v, ok := s.m.Load(agentID)
	if !ok {
		return sessionRef{}, false
	}
	return v.(sessionRef), true
}

// sessionHandler adds session_id and task_id to every record that names an
// agent (agent_id) but not its session, so all lines about an agent's run can
// be followed by the session id (ADR 0028), including lines logged far from
// the session's own logger. Records of agents that do not run in this daemon
// are left as they are.
type sessionHandler struct {
	inner      slog.Handler
	ids        *sessionIDs
	agentID    string // from WithAttrs
	hasSession bool   // WithAttrs already carried session_id
	hasTask    bool   // WithAttrs already carried task_id
	group      bool   // inside WithGroup: attrs are not top-level any more
}

func newSessionHandler(inner slog.Handler, ids *sessionIDs) *sessionHandler {
	return &sessionHandler{inner: inner, ids: ids}
}

func (h *sessionHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *sessionHandler) Handle(ctx context.Context, r slog.Record) error {
	agentID, hasSession, hasTask := h.agentID, h.hasSession, h.hasTask
	if !h.group {
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "agent_id":
				agentID = a.Value.String()
			case "session_id":
				hasSession = true
			case "task_id":
				hasTask = true
			}
			return true
		})
	}
	if agentID != "" && !hasSession && !h.group {
		if ref, ok := h.ids.get(agentID); ok {
			r = r.Clone()
			r.AddAttrs(slog.String("session_id", ref.session))
			if ref.task != "" && !hasTask {
				r.AddAttrs(slog.String("task_id", ref.task))
			}
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *sessionHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.inner = h.inner.WithAttrs(attrs)
	if !h.group {
		for _, a := range attrs {
			switch a.Key {
			case "agent_id":
				n.agentID = a.Value.String()
			case "session_id":
				n.hasSession = true
			case "task_id":
				n.hasTask = true
			}
		}
	}
	return &n
}

func (h *sessionHandler) WithGroup(name string) slog.Handler {
	n := *h
	n.inner = h.inner.WithGroup(name)
	if name != "" {
		n.group = true
	}
	return &n
}
