package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
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
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("POST /api/agents", s.startAgent)
	mux.HandleFunc("POST /api/agents/{id}/stop", s.stopAgent)
	mux.HandleFunc("POST /api/agents/{id}/discard", s.discardAgent)
	mux.HandleFunc("DELETE /api/agents/{id}", s.removeAgent)
	mux.HandleFunc("GET /api/agents/{id}/terminal", s.terminalWS)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("GET /api/events", s.eventsWS)
	if ui != nil {
		mux.HandleFunc("GET /", s.serveUI)
	}
	return s.guard(mux)
}

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
}

func (s *server) startAgent(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.eng.Start(r.Context(), engine.StartRequest{TaskID: req.TaskID})
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
	case errors.Is(err, engine.ErrStillRunning), errors.Is(err, engine.ErrTooManyAgents),
		errors.Is(err, engine.ErrWorktreeDirty), errors.Is(err, engine.ErrTaskHasRunning):
		writeError(w, http.StatusConflict, err.Error())
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
	ctx := c.CloseRead(r.Context())

	write := func(v any) error {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}
	if err := write(map[string]any{"type": "snapshot", "agents": s.eng.List(), "tasks": tasks}); err != nil {
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
