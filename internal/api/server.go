package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

	"github.com/OzanAlpkaya/agent-office/internal/auth"
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
	// Auth holds the paired devices (ADR 0039). Required: every route that is
	// not public needs one of its devices.
	Auth *auth.Manager
	// RemoteHosts are the tailnet names `tailscale serve` forwards to this
	// daemon (e.g. "box.tail1234.ts.net", ADR 0038). Their requests arrive
	// over HTTPS on 443 and must come from a tailnet user, never Funnel.
	RemoteHosts []string
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
	if opts.Auth == nil {
		panic("api: Options.Auth is required")
	}
	s := &server{eng: eng, opts: opts, log: opts.Logger, ui: ui}
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		h := rt.handler
		if !rt.public {
			h = s.requireDevice(h)
		}
		mux.HandleFunc(rt.pattern, h)
	}
	if ui != nil {
		mux.HandleFunc("GET /", s.serveUI)
	}
	return s.logRequests(s.securityHeaders(s.guard(mux)))
}

// routes is every route of the API. Each one needs a paired device (ADR 0039)
// unless it is marked public; TestEveryRouteNeedsADevice walks this table.
func (s *server) routes() []route {
	return []route{
		{pattern: "GET /api/health", handler: s.health, public: true},
		{pattern: "GET /api/project", handler: s.getProject},
		{pattern: "GET /api/status", handler: s.getStatus},
		{pattern: "GET /api/roles", handler: s.listRoles},
		{pattern: "GET /api/usage", handler: s.getUsage},
		{pattern: "GET /api/agents", handler: s.listAgents},
		{pattern: "POST /api/agents", handler: s.startAgent},
		{pattern: "POST /api/agents/{id}/stop", handler: s.stopAgent},
		{pattern: "POST /api/agents/{id}/discard", handler: s.discardAgent},
		{pattern: "POST /api/agents/{id}/import", handler: s.importAgent},
		{pattern: "POST /api/agents/{id}/save-work", handler: s.saveWork},
		{pattern: "GET /api/agents/{id}/events", handler: s.agentEvents},
		{pattern: "GET /api/agents/{id}/messages", handler: s.agentMessages},
		{pattern: "DELETE /api/agents/{id}", handler: s.removeAgent},
		{pattern: "GET /api/agents/{id}/terminal", handler: s.terminalWS},
		{pattern: "GET /api/tasks", handler: s.listTasks},
		{pattern: "POST /api/tasks", handler: s.createTask},
		{pattern: "PATCH /api/tasks/{id}", handler: s.updateTask},
		{pattern: "DELETE /api/tasks/{id}", handler: s.deleteTask},
		{pattern: "GET /api/access-requests", handler: s.listAccess},
		{pattern: "POST /api/access-requests/{id}/approve", handler: s.decideAccess(true)},
		{pattern: "POST /api/access-requests/{id}/deny", handler: s.decideAccess(false)},
		{pattern: "GET /api/pull-requests", handler: s.listPullRequests},
		{pattern: "POST /api/tasks/{id}/pull-request", handler: s.openPullRequest},
		{pattern: "POST /api/tasks/{id}/resolve-conflict", handler: s.resolveConflict},
		{pattern: "POST /api/pull-requests/{id}/review", handler: s.prAction(s.requestReview)},
		{pattern: "POST /api/pull-requests/{id}/follow-up", handler: s.prAction(s.followUp)},
		{pattern: "POST /api/pull-requests/{id}/accept", handler: s.prAction(s.acceptFlagged)},
		{pattern: "POST /api/agents/{id}/message", handler: s.messageAgent},
		{pattern: "POST /api/agents/{id}/resume", handler: s.resumeAgent},
		{pattern: "GET /api/agents/{id}/sessions", handler: s.agentSessions},
		{pattern: "GET /api/agents/{id}/sessions/{sid}/terminal", handler: s.sessionTerminal},
		{pattern: "GET /api/inbox", handler: s.listInbox},
		{pattern: "GET /api/specs", handler: s.listSpecs},
		{pattern: "GET /api/qa-runs", handler: s.listQARuns},
		{pattern: "GET /api/memory", handler: s.listMemory},
		{pattern: "GET /api/memory/export", handler: s.exportMemory},
		{pattern: "POST /api/memory", handler: s.addMemory},
		{pattern: "PATCH /api/memory/{id}", handler: s.memoryAction("patch")},
		{pattern: "POST /api/memory/{id}/accept", handler: s.memoryAction("accept")},
		{pattern: "DELETE /api/memory/{id}", handler: s.memoryAction("delete")},
		{pattern: "POST /api/assistant/briefing", handler: s.askBriefing},
		{pattern: "PUT /api/specs/{id}", handler: s.specAction(false)},
		{pattern: "POST /api/specs/{id}/approve", handler: s.specAction(true)},
		{pattern: "POST /api/inbox/{id}/read", handler: s.inboxAction(false)},
		{pattern: "POST /api/inbox/{id}/reply", handler: s.inboxAction(true)},
		{pattern: "GET /api/events", handler: s.eventsWS},
		{pattern: "GET /api/auth/session", handler: s.authSession, public: true},
		{pattern: "POST /api/auth/pair", handler: s.authPair, public: true},
		{pattern: "POST /api/auth/logout", handler: s.authLogout},
		{pattern: "GET /api/devices", handler: s.listDevices},
		{pattern: "POST /api/devices/pairing-codes", handler: s.newPairingCode},
		{pattern: "DELETE /api/devices/{id}", handler: s.revokeDevice},
	}
}

// route is one API route; public routes need no paired device.
type route struct {
	pattern string
	handler http.HandlerFunc
	public  bool
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

// guard rejects requests whose Host is not this daemon (on loopback, or one
// of its tailnet names through tailscale serve), requests through Tailscale
// Funnel, and state-changing requests from other origins.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local, remote := s.hostKind(r.Host)
		if !local && !remote {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if remote {
			// tailscale serve marks Funnel requests and sends identity headers only for
			// tailnet users; it strips client-supplied copies of both (ADR 0038).
			if r.Header.Get("Tailscale-Funnel-Request") != "" {
				http.Error(w, "agent-office refuses requests through Tailscale Funnel", http.StatusForbidden)
				return
			}
			if r.Header.Get("Tailscale-User-Login") == "" {
				http.Error(w, "agent-office accepts remote requests only from a tailnet user's device", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.allowedOrigin(r, remote) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostKind says whether a Host header names this daemon on loopback (with
// its port) or one of its tailnet names (HTTPS on the default port).
func (s *server) hostKind(hostport string) (local, remote bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	if port == s.opts.Port && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return true, false
	}
	if port == "" || port == "443" {
		for _, h := range s.opts.RemoteHosts {
			if strings.EqualFold(host, h) {
				return false, true
			}
		}
	}
	return false, false
}

// allowedOrigin accepts requests without an Origin (curl, same-origin GETs
// from older browsers) and same-host or dev origins; a remote host's origin
// must be https.
func (s *server) allowedOrigin(r *http.Request, remote bool) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if remote {
		return u.Scheme == "https" && strings.EqualFold(u.Host, r.Host)
	}
	return u.Host == r.Host || slices.Contains(s.opts.DevOrigins, u.Host)
}

// health is public; only a paired device learns the version.
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.device(r); !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
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

// agentSessions lists an agent's sessions, newest first (ADR 0036).
func (s *server) agentSessions(w http.ResponseWriter, r *http.Request) {
	out, err := s.eng.Sessions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionTerminal returns one session's terminal output, as the bytes the
// terminal received; the UI writes them into a read-only terminal.
func (s *server) sessionTerminal(w http.ResponseWriter, r *http.Request) {
	data, err := s.eng.SessionTerminal(r.Context(), r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// exportMemory downloads the accepted project memory as Markdown (ADR 0037).
func (s *server) exportMemory(w http.ResponseWriter, r *http.Request) {
	md, err := s.eng.MemoryMarkdown(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="memory.md"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, md)
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
// statusView is the engine's status plus the daemon's tailnet names.
type statusView struct {
	engine.Status
	RemoteHosts []string `json:"remote_hosts"`
}

func (s *server) getStatus(w http.ResponseWriter, _ *http.Request) {
	hosts := s.opts.RemoteHosts
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, statusView{Status: s.eng.Status(), RemoteHosts: hosts})
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

	// Reads and writes do not use the request's context: coder/websocket drops
	// the connection when a read's context ends, and a revoked device must get
	// the StatusSignedOut close frame first (closeIfSignedOut).
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
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
		case <-r.Context().Done():
			closeIfSignedOut(c, r)
			return
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
		closeFailed(c, r, "could not load tasks")
		return
	}
	access, err := s.eng.AccessRequests(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load access requests")
		return
	}
	prs, err := s.eng.PullRequests(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load pull requests")
		return
	}
	inbox, err := s.eng.Inbox(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the inbox")
		return
	}
	specs, err := s.eng.Specs(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the plans")
		return
	}
	qaRuns, err := s.eng.QARuns(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the QA runs")
		return
	}
	memory, err := s.eng.Memory(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the project memory")
		return
	}
	// Not the request's context, as in terminalWS: a revoked device gets StatusSignedOut.
	ctx := c.CloseRead(context.WithoutCancel(r.Context()))

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
		case <-r.Context().Done():
			closeIfSignedOut(c, r)
			return
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
	// The PWA (ADR 0040): the page, the service worker and the manifest are
	// revalidated, so a new daemon's UI is picked up at once; Vite's hashed
	// assets never change.
	switch {
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "no-cache")
	}
	if path.Ext(name) == ".webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
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
