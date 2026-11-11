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
	// RemoteUsers, when set, are the only Tailscale logins whose devices may
	// reach the daemon through RemoteHosts (e.g. "ozan@github").
	RemoteUsers []string
}

const (
	maxClientMessage = 64 << 10
	maxTermDimension = 1000
)

type server struct {
	hub  *engine.Hub
	opts Options
	log  *slog.Logger
	ui   fs.FS
}

// New returns the HTTP handler: JSON API under /api, the embedded UI elsewhere.
// ui may be nil (API only).
func New(hub *engine.Hub, ui fs.FS, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Auth == nil {
		panic("api: Options.Auth is required")
	}
	s := &server{hub: hub, opts: opts, log: opts.Logger, ui: ui}
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		h := rt.handler
		if rt.project {
			h = s.inProject(h)
		}
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
		{pattern: "GET /api/status", handler: s.daemonStatus},
		{pattern: "GET /api/usage", handler: s.daemonUsage},
		{pattern: "GET /api/events", handler: s.hubEventsWS},
		{pattern: "GET /api/projects", handler: s.listProjects},
		{pattern: "POST /api/projects", handler: s.addProject},
		{pattern: "GET /api/projects/{project}", handler: s.getProject},
		{pattern: "PATCH /api/projects/{project}", handler: s.updateProject},
		{pattern: "DELETE /api/projects/{project}", handler: s.removeProject},
		{pattern: "POST /api/projects/{project}/retry", handler: s.retryProject},
		{pattern: "GET /api/office-layouts", handler: s.officeLayouts},
		{pattern: "POST /api/office-layouts/check", handler: s.checkOfficeLayout},
		{pattern: "GET /api/projects/{project}/status", handler: s.getStatus, project: true},
		{pattern: "GET /api/projects/{project}/usage", handler: s.getUsage, project: true},
		{pattern: "GET /api/projects/{project}/roles", handler: s.listRoles, project: true},
		{pattern: "GET /api/projects/{project}/agents", handler: s.listAgents, project: true},
		{pattern: "POST /api/projects/{project}/agents", handler: s.startAgent, project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/stop", handler: s.stopAgent, project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/discard", handler: s.discardAgent, project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/import", handler: s.importAgent, project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/save-work", handler: s.saveWork, project: true},
		{pattern: "GET /api/projects/{project}/agents/{id}/events", handler: s.agentEvents, project: true},
		{pattern: "GET /api/projects/{project}/agents/{id}/messages", handler: s.agentMessages, project: true},
		{pattern: "DELETE /api/projects/{project}/agents/{id}", handler: s.removeAgent, project: true},
		{pattern: "GET /api/projects/{project}/agents/{id}/terminal", handler: s.terminalWS, project: true},
		{pattern: "GET /api/projects/{project}/tasks", handler: s.listTasks, project: true},
		{pattern: "POST /api/projects/{project}/tasks", handler: s.createTask, project: true},
		{pattern: "PATCH /api/projects/{project}/tasks/{id}", handler: s.updateTask, project: true},
		{pattern: "DELETE /api/projects/{project}/tasks/{id}", handler: s.deleteTask, project: true},
		{pattern: "GET /api/projects/{project}/tasks/{id}/record", handler: s.goalRecord, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/remove-agents", handler: s.removeGoalAgents, project: true},
		{pattern: "GET /api/projects/{project}/tasks/{id}/merge", handler: s.mergePreview, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/merge", handler: s.mergeGoal, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/push", handler: s.pushGoal, project: true},
		{pattern: "GET /api/projects/{project}/goal-merges", handler: s.listGoalMerges, project: true},
		{pattern: "GET /api/projects/{project}/tasks/{id}/branches", handler: s.goalBranches, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/branches/delete", handler: s.deleteBranches(true), project: true},
		{pattern: "GET /api/projects/{project}/branches", handler: s.projectBranches, project: true},
		{pattern: "POST /api/projects/{project}/branches/delete", handler: s.deleteBranches(false), project: true},
		{pattern: "GET /api/projects/{project}/access-requests", handler: s.listAccess, project: true},
		{pattern: "POST /api/projects/{project}/access-requests/{id}/approve", handler: s.decideAccess(true), project: true},
		{pattern: "POST /api/projects/{project}/access-requests/{id}/deny", handler: s.decideAccess(false), project: true},
		{pattern: "GET /api/projects/{project}/access-grants", handler: s.listGrants, project: true},
		{pattern: "DELETE /api/projects/{project}/access-grants/{id}", handler: s.revokeGrant, project: true},
		{pattern: "GET /api/projects/{project}/pull-requests", handler: s.listPullRequests, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/pull-request", handler: s.openPullRequest, project: true},
		{pattern: "POST /api/projects/{project}/tasks/{id}/resolve-conflict", handler: s.resolveConflict, project: true},
		{pattern: "POST /api/projects/{project}/pull-requests/{id}/review", handler: s.prAction(s.requestReview), project: true},
		{pattern: "POST /api/projects/{project}/pull-requests/{id}/follow-up", handler: s.prAction(s.followUp), project: true},
		{pattern: "POST /api/projects/{project}/pull-requests/{id}/accept", handler: s.prAction(s.acceptFlagged), project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/message", handler: s.messageAgent, project: true},
		{pattern: "POST /api/projects/{project}/agents/{id}/resume", handler: s.resumeAgent, project: true},
		{pattern: "GET /api/projects/{project}/agents/{id}/sessions", handler: s.agentSessions, project: true},
		{pattern: "GET /api/projects/{project}/agents/{id}/sessions/{sid}/terminal", handler: s.sessionTerminal, project: true},
		{pattern: "GET /api/projects/{project}/inbox", handler: s.listInbox, project: true},
		{pattern: "GET /api/projects/{project}/specs", handler: s.listSpecs, project: true},
		{pattern: "GET /api/projects/{project}/qa-runs", handler: s.listQARuns, project: true},
		{pattern: "GET /api/projects/{project}/memory", handler: s.listMemory, project: true},
		{pattern: "GET /api/projects/{project}/memory/export", handler: s.exportMemory, project: true},
		{pattern: "POST /api/projects/{project}/memory", handler: s.addMemory, project: true},
		{pattern: "PATCH /api/projects/{project}/memory/{id}", handler: s.memoryAction("patch"), project: true},
		{pattern: "POST /api/projects/{project}/memory/{id}/accept", handler: s.memoryAction("accept"), project: true},
		{pattern: "DELETE /api/projects/{project}/memory/{id}", handler: s.memoryAction("delete"), project: true},
		{pattern: "POST /api/projects/{project}/assistant/briefing", handler: s.askBriefing, project: true},
		{pattern: "PUT /api/projects/{project}/specs/{id}", handler: s.specAction(false), project: true},
		{pattern: "POST /api/projects/{project}/specs/{id}/approve", handler: s.specAction(true), project: true},
		{pattern: "POST /api/projects/{project}/inbox/{id}/read", handler: s.inboxAction(false), project: true},
		{pattern: "POST /api/projects/{project}/inbox/{id}/reply", handler: s.inboxAction(true), project: true},
		{pattern: "GET /api/projects/{project}/events", handler: s.eventsWS, project: true},
		{pattern: "GET /api/auth/session", handler: s.authSession, public: true},
		{pattern: "POST /api/auth/pair", handler: s.authPair, public: true},
		{pattern: "POST /api/auth/logout", handler: s.authLogout},
		{pattern: "GET /api/devices", handler: s.listDevices},
		{pattern: "POST /api/devices/pairing-codes", handler: s.newPairingCode},
		{pattern: "DELETE /api/devices/{id}", handler: s.revokeDevice},
		{pattern: "POST /api/devices/sign-out-others", handler: s.signOutOthers},
	}
}

// route is one API route; public routes need no paired device.
type route struct {
	pattern string
	handler http.HandlerFunc
	public  bool
	project bool // under /api/projects/{project}: the handler runs on that project's engine (ADR 0046)
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
			login := r.Header.Get("Tailscale-User-Login")
			if login == "" {
				http.Error(w, "agent-office accepts remote requests only from a tailnet user's device", http.StatusForbidden)
				return
			}
			if len(s.opts.RemoteUsers) > 0 && !slices.ContainsFunc(s.opts.RemoteUsers, func(u string) bool { return strings.EqualFold(u, login) }) {
				http.Error(w, "this Tailscale user may not use this agent-office (--remote-users)", http.StatusForbidden)
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

func (s *server) listAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, eng(r).List())
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
	a, err := eng(r).Start(r.Context(), engine.StartRequest{TaskID: req.TaskID, Role: req.Role})
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) stopAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := eng(r).Stop(id); err != nil {
		s.engineError(w, err)
		return
	}
	a, _ := eng(r).Get(id)
	writeJSON(w, http.StatusOK, a)
}

// importAgent brings a sandboxed agent's branch into the repository now
// ("Import now"; also while it runs, ADR 0014).
func (s *server) importAgent(w http.ResponseWriter, r *http.Request) {
	imp, err := eng(r).Import(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

// saveWork commits an exited sandboxed agent's uncommitted work and imports it.
func (s *server) saveWork(w http.ResponseWriter, r *http.Request) {
	imp, err := eng(r).SaveWork(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

// agentEvents lists an agent's recent sandbox events, newest first (ADR 0016).
func (s *server) agentEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := eng(r).SandboxEvents(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

// agentMessages lists an agent's recent messages, sent and received, newest
// first (ADR 0018). Bodies come from agents: the UI renders them as text.
func (s *server) agentMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := eng(r).Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// listAccess lists access requests, pending first (ADR 0019).
func (s *server) listAccess(w http.ResponseWriter, r *http.Request) {
	list, err := eng(r).AccessRequests(r.Context())
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
	if err := eng(r).ResolveConflict(r.Context(), r.PathValue("id")); err != nil {
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
	m, err := eng(r).MessageAgent(r.Context(), r.PathValue("id"), req.Body)
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
	a, err := eng(r).Resume(r.Context(), r.PathValue("id"), req.Prompt)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// agentSessions lists an agent's sessions, newest first (ADR 0036).
func (s *server) agentSessions(w http.ResponseWriter, r *http.Request) {
	out, err := eng(r).Sessions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionTerminal returns one session's terminal output, as the bytes the
// terminal received; the UI writes them into a read-only terminal.
func (s *server) sessionTerminal(w http.ResponseWriter, r *http.Request) {
	data, err := eng(r).SessionTerminal(r.Context(), r.PathValue("id"), r.PathValue("sid"))
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
	md, err := eng(r).MemoryMarkdown(r.Context())
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
	m, err := eng(r).Memory(r.Context())
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
	m, err := eng(r).AddMemory(r.Context(), in)
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
			if err := eng(r).DeleteMemory(r.Context(), id); err != nil {
				s.engineError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "accept":
			m, err = eng(r).UpdateMemory(r.Context(), id, engine.MemoryInput{Status: engine.MemoryAccepted})
		default:
			var in engine.MemoryInput
			if !decodeJSON(w, r, &in) {
				return
			}
			m, err = eng(r).UpdateMemory(r.Context(), id, in)
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
	a, err := eng(r).AskBriefing(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) listQARuns(w http.ResponseWriter, r *http.Request) {
	runs, err := eng(r).QARuns(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *server) listSpecs(w http.ResponseWriter, r *http.Request) {
	specs, err := eng(r).Specs(r.Context())
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
			res, err := eng(r).ApproveSpec(r.Context(), id)
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
		spec, err := eng(r).EditSpec(r.Context(), id, p)
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, spec)
	}
}

func (s *server) listInbox(w http.ResponseWriter, r *http.Request) {
	items, err := eng(r).Inbox(r.Context())
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
			it, err = eng(r).ReplyInbox(r.Context(), id, req.Body)
		} else {
			it, err = eng(r).MarkInboxRead(r.Context(), id)
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
		// Optional (ADR 0053): an approval's scope (agent or project) and a reason.
		var req struct {
			Scope  string `json:"scope"`
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
			return
		}
		ar, err := eng(r).DecideAccessAs(r.Context(), id, approve, req.Scope, req.Reason)
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ar)
	}
}

// goalRecord is a goal with everything the archive shows (ADR 0054).
func (s *server) goalRecord(w http.ResponseWriter, r *http.Request) {
	rec, err := eng(r).GoalRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// removeGoalAgents removes a done goal's exited agents in one action.
func (s *server) removeGoalAgents(w http.ResponseWriter, r *http.Request) {
	res, err := eng(r).RemoveGoalAgents(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// mergePreview is what the Merge confirmation shows (ADR 0055); it changes
// nothing.
func (s *server) mergePreview(w http.ResponseWriter, r *http.Request) {
	p, err := eng(r).MergePreview(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// goalBranches previews the clean-up of a done goal's branches (Phase 18, ADR 0059).
func (s *server) goalBranches(w http.ResponseWriter, r *http.Request) {
	p, err := eng(r).GoalBranches(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// projectBranches previews the clean-up of every agent-office branch of the project.
func (s *server) projectBranches(w http.ResponseWriter, r *http.Request) {
	p, err := eng(r).ProjectBranches(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// deleteBranches deletes the branches the user confirmed, of a goal or of
// the project; each is checked again and refused rather than forced.
func (s *server) deleteBranches(ofGoal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req engine.BranchDelete
		if !decodeJSON(w, r, &req) {
			return
		}
		var res engine.BranchDeleteResult
		var err error
		if ofGoal {
			res, err = eng(r).DeleteGoalBranches(r.Context(), r.PathValue("id"), req)
		} else {
			res, err = eng(r).DeleteProjectBranches(r.Context(), req)
		}
		if err != nil {
			s.engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// mergeGoal squash-merges a goal's QA-passed work after the user confirmed
// the heads and the message the preview showed (ADR 0055).
func (s *server) mergeGoal(w http.ResponseWriter, r *http.Request) {
	var req engine.MergeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m, err := eng(r).MergeGoal(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// pushGoal pushes the branch a goal was merged into (ADR 0055).
func (s *server) pushGoal(w http.ResponseWriter, r *http.Request) {
	m, err := eng(r).PushGoal(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// listGoalMerges lists the project's goal merges, newest first.
func (s *server) listGoalMerges(w http.ResponseWriter, r *http.Request) {
	list, err := eng(r).GoalMerges(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// listGrants lists the project's stored access grants (ADR 0053).
func (s *server) listGrants(w http.ResponseWriter, r *http.Request) {
	list, err := eng(r).AccessGrants(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// revokeGrant deletes a stored access grant (Phase 17). Revoking narrows the
// sandbox, so any paired device may; the origin guard applies to DELETE.
func (s *server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid access grant id")
		return
	}
	g, err := eng(r).RevokeAccessGrant(r.Context(), id)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// listPullRequests lists the project's pull requests with their reviews
// (ADR 0020, 0021). Review text comes from agents, bots or people: the UI
// renders it as text.
func (s *server) listPullRequests(w http.ResponseWriter, r *http.Request) {
	list, err := eng(r).PullRequests(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// openPullRequest pushes the task's branch and opens or updates its pull
// request ("Open PR"). POST, so the origin guard applies.
func (s *server) openPullRequest(w http.ResponseWriter, r *http.Request) {
	pr, err := eng(r).OpenPullRequest(r.Context(), r.PathValue("id"))
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
	pr, err := eng(r).RequestReview(r.Context(), id)
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
	pr, err := eng(r).SendToWorker(r.Context(), id, req.ReviewID)
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
	pr, err := eng(r).AcceptFlagged(r.Context(), id, req.Commits)
	if err != nil {
		s.prError(w, pr, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// removeAgent cleans up the worktree (keeping uncommitted work, ADR 0010).
func (s *server) removeAgent(w http.ResponseWriter, r *http.Request) {
	res, err := eng(r).Remove(r.Context(), r.PathValue("id"))
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
	res, err := eng(r).Discard(r.Context(), r.PathValue("id"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// listRoles lists the team roles of this daemon run (ADR 0029).
func (s *server) listRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, eng(r).Roles())
}

// getStatus reports the sandbox mode, the worker image and warnings (e.g. an
// untested Claude Code version) for the UI.
// statusView is the engine's status plus the daemon's tailnet names.
type statusView struct {
	engine.Status
	RemoteHosts []string `json:"remote_hosts"`
}

func (s *server) getStatus(w http.ResponseWriter, r *http.Request) {
	hosts := s.opts.RemoteHosts
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, statusView{Status: eng(r).Status(), RemoteHosts: hosts})
}

// getUsage is every session's token usage and estimated cost (ADR 0026).
func (s *server) getUsage(w http.ResponseWriter, r *http.Request) {
	rep, err := eng(r).Usage(r.Context())
	if err != nil {
		s.log.Error("usage report", "err", err)
		writeError(w, http.StatusInternalServerError, "usage report failed")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := eng(r).ListTasks(r.Context())
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
	t, err := eng(r).CreateTask(r.Context(), in)
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
	t, err := eng(r).UpdateTask(r.Context(), r.PathValue("id"), p)
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := eng(r).DeleteTask(r.Context(), r.PathValue("id")); err != nil {
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
	if code, ok := projectErrorStatus(err); ok {
		writeError(w, code, err.Error())
		return
	}
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
		errors.Is(err, git.ErrBranchCheckedOut), errors.Is(err, engine.ErrFlagged), errors.Is(err, engine.ErrNoGitHub),
		errors.Is(err, engine.ErrMergeRefused):
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
	term, err := eng(r).Terminal(id)
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

	go s.readTerminalInput(ctx, cancel, c, eng(r), id, term, log)
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
	e *engine.Engine, id string, term *terminal.Session, log *slog.Logger) {
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
			_ = e.Input(id, []byte(m.Data))
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
	updates, unsubscribe := eng(r).Subscribe() // before the snapshot, so no update is missed
	defer unsubscribe()
	tasks, err := eng(r).ListTasks(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load tasks")
		return
	}
	access, err := eng(r).AccessRequests(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load access requests")
		return
	}
	prs, err := eng(r).PullRequests(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load pull requests")
		return
	}
	inbox, err := eng(r).Inbox(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the inbox")
		return
	}
	specs, err := eng(r).Specs(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the plans")
		return
	}
	qaRuns, err := eng(r).QARuns(r.Context())
	if err != nil {
		closeFailed(c, r, "could not load the QA runs")
		return
	}
	memory, err := eng(r).Memory(r.Context())
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
	if err := write(map[string]any{"type": "snapshot", "project_id": eng(r).Project().ID, "agents": eng(r).List(), "tasks": tasks,
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
