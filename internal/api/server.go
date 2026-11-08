package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
)

// Options configures the API server.
type Options struct {
	// Port is the port the daemon listens on. Only Host headers naming a
	// loopback host with this port are accepted (DNS rebinding defence).
	Port string
	// DevOrigins are extra allowed origins as host:port (e.g. the Vite dev
	// server "localhost:5173"). Empty outside --dev.
	DevOrigins []string
	Version    string
	Logger     *slog.Logger
}

const (
	maxClientMessage = 64 << 10
	maxTermDimension = 1000
)

type server struct {
	eng  *engine.Engine
	opts Options
	log  *slog.Logger
	ui   fs.FS
}

// New returns the HTTP handler: JSON API under /api, the embedded UI elsewhere.
// ui may be nil (API only).
func New(eng *engine.Engine, ui fs.FS, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &server{eng: eng, opts: opts, log: opts.Logger, ui: ui}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/project", s.getProject)
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("GET /api/roles", s.listRoles)
	mux.HandleFunc("GET /api/usage", s.getUsage)
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("POST /api/agents", s.startAgent)
	mux.HandleFunc("POST /api/agents/{id}/stop", s.stopAgent)
	mux.HandleFunc("POST /api/agents/{id}/discard", s.discardAgent)
	mux.HandleFunc("POST /api/agents/{id}/import", s.importAgent)
	mux.HandleFunc("POST /api/agents/{id}/save-work", s.saveWork)
	mux.HandleFunc("GET /api/agents/{id}/events", s.agentEvents)
	mux.HandleFunc("GET /api/agents/{id}/messages", s.agentMessages)
	mux.HandleFunc("DELETE /api/agents/{id}", s.removeAgent)
	mux.HandleFunc("GET /api/agents/{id}/terminal", s.terminalWS)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("GET /api/access-requests", s.listAccess)
	mux.HandleFunc("POST /api/access-requests/{id}/approve", s.decideAccess(true))
	mux.HandleFunc("POST /api/access-requests/{id}/deny", s.decideAccess(false))
	mux.HandleFunc("GET /api/pull-requests", s.listPullRequests)
	mux.HandleFunc("POST /api/tasks/{id}/pull-request", s.openPullRequest)
	mux.HandleFunc("POST /api/tasks/{id}/resolve-conflict", s.resolveConflict)
	mux.HandleFunc("POST /api/pull-requests/{id}/review", s.prAction(s.requestReview))
	mux.HandleFunc("POST /api/pull-requests/{id}/follow-up", s.prAction(s.followUp))
	mux.HandleFunc("POST /api/pull-requests/{id}/accept", s.prAction(s.acceptFlagged))
	mux.HandleFunc("POST /api/agents/{id}/message", s.messageAgent)
	mux.HandleFunc("POST /api/agents/{id}/resume", s.resumeAgent)
	mux.HandleFunc("GET /api/inbox", s.listInbox)
	mux.HandleFunc("GET /api/specs", s.listSpecs)
	mux.HandleFunc("GET /api/qa-runs", s.listQARuns)
	mux.HandleFunc("GET /api/memory", s.listMemory)
	mux.HandleFunc("POST /api/memory", s.addMemory)
	mux.HandleFunc("PATCH /api/memory/{id}", s.memoryAction("patch"))
	mux.HandleFunc("POST /api/memory/{id}/accept", s.memoryAction("accept"))
	mux.HandleFunc("DELETE /api/memory/{id}", s.memoryAction("delete"))
	mux.HandleFunc("POST /api/assistant/briefing", s.askBriefing)
	mux.HandleFunc("PUT /api/specs/{id}", s.specAction(false))
	mux.HandleFunc("POST /api/specs/{id}/approve", s.specAction(true))
	mux.HandleFunc("POST /api/inbox/{id}/read", s.inboxAction(false))
	mux.HandleFunc("POST /api/inbox/{id}/reply", s.inboxAction(true))
	mux.HandleFunc("GET /api/events", s.eventsWS)
	if ui != nil {
		mux.HandleFunc("GET /", s.serveUI)
	}
	return s.logRequests(s.guard(mux))
}

// logRequests gives every request an id, returned as X-Request-Id and logged
// with method, path, status and duration (ADR 0028). Debug level: the UI
// makes many small requests.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		s.log.Debug("http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// statusWriter records the status. Unwrap lets the WebSocket upgrade reach
// the connection's Hijacker.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// guard rejects requests whose Host is not this daemon on loopback, and
// state-changing requests from other origins.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.allowedOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) allowedHost(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != s.opts.Port {
		return false
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// allowedOrigin accepts requests without an Origin (curl, same-origin GETs
// from older browsers) and same-host or dev origins.
func (s *server) allowedOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return u.Host == r.Host || slices.Contains(s.opts.DevOrigins, u.Host)
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.opts.Version})
}

func (s *server) listAgents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.List())
}

// startRequest is the optional JSON body of POST /api/agents.
type startRequest struct {
	TaskID string `json:"task_id"`
	Role   string `json:"role"` // a role name (ADR 0029): worker (default), tech-lead, architect, assistant, ...
}

func (s *server) startAgent(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.eng.Start(r.Context(), engine.StartRequest{TaskID: req.TaskID, Role: req.Role})
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) stopAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.Stop(id); err != nil {
		s.engineError(w, err)
		return
	}
	a, _ := s.eng.Get(id)
	writeJSON(w, http.StatusOK, a)
}

// importAgent brings a sandboxed agent's branch into the repository now
// ("Import now"; also while it runs, ADR 0014).
func (s *server) importAgent(w http.ResponseWriter, r *http.Request) {
	imp, err := s.eng.Import(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

// saveWork commits an exited sandboxed agent's uncommitted work and imports it.
func (s *server) saveWork(w http.ResponseWriter, r *http.Request) {
	imp, err := s.eng.SaveWork(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

// agentEvents lists an agent's recent sandbox events, newest first (ADR 0016).
func (s *server) agentEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.eng.SandboxEvents(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

// agentMessages lists an agent's recent messages, sent and received, newest
// first (ADR 0018). Bodies come from agents: the UI renders them as text.
func (s *server) agentMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.eng.Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// listAccess lists access requests, pending first (ADR 0019).
func (s *server) listAccess(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.AccessRequests(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// decideAccess approves or denies an access request: the human decision
// ADR 0019 requires. POST, so the origin guard applies.
// resolveConflict relaunches a task's worker to merge the base branch and
// resolve the conflict (ADR 0032).
func (s *server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ResolveConflict(r.Context(), r.PathValue("id")); err != nil {
		s.engineError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// bodyRequest is a JSON body with one text field: a message or a reply.
type bodyRequest struct {
	Body string `json:"body"`
}

// messageAgent sends the user's words to a running agent (ADR 0030).
func (s *server) messageAgent(w http.ResponseWriter, r *http.Request) {
	var req bodyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m, err := s.eng.MessageAgent(r.Context(), r.PathValue("id"), req.Body)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// resumeAgent continues an exited agent's conversation, with an optional
// prompt from the user (ADR 0035).
func (s *server) resumeAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.eng.Resume(r.Context(), r.PathValue("id"), req.Prompt)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) listMemory(w http.ResponseWriter, r *http.Request) {
	m, err := s.eng.Memory(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) addMemory(w http.ResponseWriter, r *http.Request) {
	var in engine.MemoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	m, err := s.eng.AddMemory(r.Context(), in)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// memoryAction edits, accepts or deletes a project memory entry (ADR 0034).
func (s *server) memoryAction(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid memory id")
			return
		}
		var m engine.MemoryEntry
		switch op {
		case "delete":
			if err := s.eng.DeleteMemory(r.Context(), id); err != nil {
				s.engineError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "accept":
			m, err = s.eng.UpdateMemory(r.Context(), id, engine.MemoryInput{Status: engine.MemoryAccepted})
		default:
			var in engine.MemoryInput
			if !decodeJSON(w, r, &in) {
				return
			}
			m, err = s.eng.UpdateMemory(r.Context(), id, in)
		}
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}

// askBriefing asks the assistant for a briefing, starting it if needed.
func (s *server) askBriefing(w http.ResponseWriter, r *http.Request) {
	a, err := s.eng.AskBriefing(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) listQARuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.eng.QARuns(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *server) listSpecs(w http.ResponseWriter, r *http.Request) {
	specs, err := s.eng.Specs(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, specs)
}

// specAction saves the user's edit of a plan as a new version (PUT), or
// approves a proposed version (ADR 0031).
func (s *server) specAction(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid spec id")
			return
		}
		if approve {
			res, err := s.eng.ApproveSpec(r.Context(), id)
			if err != nil {
				s.engineError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, res)
			return
		}
		var p engine.Plan
		if !decodeJSON(w, r, &p) {
			return
		}
		spec, err := s.eng.EditSpec(r.Context(), id, p)
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, spec)
	}
}

func (s *server) listInbox(w http.ResponseWriter, r *http.Request) {
	items, err := s.eng.Inbox(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// inboxAction marks an inbox item read, or answers it (reply).
func (s *server) inboxAction(reply bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid inbox id")
			return
		}
		var it engine.InboxItem
		if reply {
			var req bodyRequest
			if !decodeJSON(w, r, &req) {
				return
			}
			it, err = s.eng.ReplyInbox(r.Context(), id, req.Body)
		} else {
			it, err = s.eng.MarkInboxRead(r.Context(), id)
		}
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, it)
	}
}

func (s *server) decideAccess(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid access request id")
			return
		}
		ar, err := s.eng.DecideAccess(r.Context(), id, approve)
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ar)
	}
}

// listPullRequests lists the project's pull requests with their reviews
// (ADR 0020, 0021). Review text comes from agents, bots or people: the UI
// renders it as text.
func (s *server) listPullRequests(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.PullRequests(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// openPullRequest pushes the task's branch and opens or updates its pull
// request ("Open PR"). POST, so the origin guard applies.
func (s *server) openPullRequest(w http.ResponseWriter, r *http.Request) {
	pr, err := s.eng.OpenPullRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		s.prError(w, pr, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// prError reports a refused or failed pull request operation together with
// the pull request's state, so the UI can show what blocks it.
func (s *server) prError(w http.ResponseWriter, pr engine.PullRequest, err error) {
	if pr.ID != 0 && (errors.Is(err, engine.ErrFlagged) || errors.Is(err, engine.ErrUpstream)) {
		status := http.StatusConflict
		if errors.Is(err, engine.ErrUpstream) {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "pull_request": pr})
		return
	}
	s.engineError(w, err)
}

// prAction parses the pull request id of a POST /api/pull-requests/{id}/...
func (s *server) prAction(fn func(w http.ResponseWriter, r *http.Request, id int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid pull request id")
			return
		}
		fn(w, r, id)
	}
}

// requestReview asks the reviewer for another review of the current head.
func (s *server) requestReview(w http.ResponseWriter, r *http.Request, id int64) {
	pr, err := s.eng.RequestReview(r.Context(), id)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// followUp sends a review to the pull request's original worker ("Send to
// worker", ADR 0022).
func (s *server) followUp(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ReviewID int64 `json:"review_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ReviewID < 1 {
		writeError(w, http.StatusBadRequest, "review_id is required")
		return
	}
	pr, err := s.eng.SendToWorker(r.Context(), id, req.ReviewID)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// acceptFlagged accepts the listed flagged commits and pushes (ADR 0014).
func (s *server) acceptFlagged(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Commits []string `json:"commits"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	pr, err := s.eng.AcceptFlagged(r.Context(), id, req.Commits)
	if err != nil {
		s.prError(w, pr, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// removeAgent cleans up the worktree (keeping uncommitted work, ADR 0010).
func (s *server) removeAgent(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.Remove(r.Context(), r.PathValue("id"))
	if errors.Is(err, engine.ErrWorktreeDirty) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": res})
		return
	}
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// discardAgent force-removes the worktree, dropping uncommitted changes. The
// UI asks for confirmation before calling it.
func (s *server) discardAgent(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.Discard(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) getProject(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Project())
}

// listRoles lists the team roles of this daemon run (ADR 0029).
func (s *server) listRoles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Roles())
}

// getStatus reports the sandbox mode, the worker image and warnings (e.g. an
// untested Claude Code version) for the UI.
func (s *server) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Status())
}

// getUsage is every session's token usage and estimated cost (ADR 0026).
func (s *server) getUsage(w http.ResponseWriter, r *http.Request) {
	rep, err := s.eng.Usage(r.Context())
	if err != nil {
		s.log.Error("usage report", "err", err)
		writeError(w, http.StatusInternalServerError, "usage report failed")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.eng.ListTasks(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *server) createTask(w http.ResponseWriter, r *http.Request) {
	var in engine.TaskInput
	if !decodeJSON(w, r, &in) {
		return
	}
	t, err := s.eng.CreateTask(r.Context(), in)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) updateTask(w http.ResponseWriter, r *http.Request) {
	var p engine.TaskPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	t, err := s.eng.UpdateTask(r.Context(), r.PathValue("id"), p)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteTask(r.Context(), r.PathValue("id")); err != nil {
		s.engineError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxBody bounds JSON request bodies: a 20 000-character description can take
// up to ~80 KB in UTF-8, more with JSON escaping.
const maxBody = 256 << 10

// decodeJSON requires a JSON content type (a cross-site form post cannot set
// it without a CORS preflight), bounds the body and rejects unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (s *server) engineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, engine.ErrNoTerminal):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, engine.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, engine.ErrLimit):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, engine.ErrStillRunning), errors.Is(err, engine.ErrTooManyAgents),
		errors.Is(err, engine.ErrWorktreeDirty), errors.Is(err, engine.ErrTaskHasRunning),
		errors.Is(err, engine.ErrLeadRunning), errors.Is(err, engine.ErrDecided),
		errors.Is(err, git.ErrBranchCheckedOut), errors.Is(err, engine.ErrFlagged), errors.Is(err, engine.ErrNoGitHub):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrUpstream):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		s.log.Error("engine", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *server) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	// Same-origin is enforced by default; dev origins are added explicitly.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.opts.DevOrigins})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxClientMessage)
	return c, nil
}

// clientMsg is the browser -> daemon terminal protocol (text frames).
// Daemon -> browser is raw PTY output in binary frames.
type clientMsg struct {
	Type string `json:"type"` // "input" | "resize"
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (s *server) terminalWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	term, err := s.eng.Terminal(id)
	if err != nil {
		s.engineError(w, err)
		return
	}
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	log := s.log.With("agent_id", id)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	snapshot, sub, running := term.Attach()
	if len(snapshot) > 0 {
		if err := c.Write(ctx, websocket.MessageBinary, snapshot); err != nil {
			return
		}
	}
	if !running {
		c.Close(websocket.StatusNormalClosure, "process exited")
		return
	}
	defer sub.Close()

	go s.readTerminalInput(ctx, cancel, c, id, term, log)
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-sub.C:
			if !ok {
				// Closed either because the process exited (Done follows
				// immediately) or because this viewer fell behind.
				select {
				case <-term.Done():
					c.Close(websocket.StatusNormalClosure, "process exited")
				case <-time.After(500 * time.Millisecond):
					c.Close(websocket.StatusTryAgainLater, "viewer too slow")
				}
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	}
}

func (s *server) readTerminalInput(ctx context.Context, cancel context.CancelFunc, c *websocket.Conn,
	id string, term *terminal.Session, log *slog.Logger) {
	defer cancel()
	firstResize := true
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var m clientMsg
		if err := json.Unmarshal(data, &m); err != nil {
			log.Warn("bad terminal message", "err", err)
			continue
		}
		switch m.Type {
		case "input":
			// Through the engine, so it can notice interrupts (Esc / Ctrl-C).
			_ = s.eng.Input(id, []byte(m.Data))
		case "resize":
			if m.Cols < 1 || m.Rows < 1 || m.Cols > maxTermDimension || m.Rows > maxTermDimension {
				continue
			}
			sz := terminal.Size{Cols: uint16(m.Cols), Rows: uint16(m.Rows)}
			if firstResize && term.Size() == sz {
				// Same size as before: no SIGWINCH would be sent, so nudge a
				// redraw. Raw scrollback is not a screen snapshot (ADR 0002).
				_ = term.Redraw()
			} else {
				_ = term.Resize(sz)
			}
			firstResize = false
		}
	}
}

// eventsWS streams agent updates: one snapshot, then one message per change.
func (s *server) eventsWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	updates, unsubscribe := s.eng.Subscribe() // before the snapshot, so no update is missed
	defer unsubscribe()
	tasks, err := s.eng.ListTasks(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load tasks")
		return
	}
	access, err := s.eng.AccessRequests(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load access requests")
		return
	}
	prs, err := s.eng.PullRequests(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load pull requests")
		return
	}
	inbox, err := s.eng.Inbox(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load the inbox")
		return
	}
	specs, err := s.eng.Specs(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load the plans")
		return
	}
	qaRuns, err := s.eng.QARuns(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load the QA runs")
		return
	}
	memory, err := s.eng.Memory(r.Context())
	if err != nil {
		c.Close(websocket.StatusInternalError, "could not load the project memory")
		return
	}
	ctx := c.CloseRead(r.Context())

	write := func(v any) error {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}
	if err := write(map[string]any{"type": "snapshot", "agents": s.eng.List(), "tasks": tasks,
		"access_requests": access, "pull_requests": prs, "inbox": inbox,
		"specs": specs, "qa_runs": qaRuns, "memory": memory}); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-updates:
			if !ok {
				c.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			if err := write(u); err != nil {
				return
			}
		}
	}
}

// serveUI serves the embedded single-page app, falling back to index.html.
func (s *server) serveUI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if _, err := fs.Stat(s.ui, name); err != nil {
		name = "index.html"
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeFileFS(w, r, s.ui, name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
