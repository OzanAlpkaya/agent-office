package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/OzanAlpkaya/agent-office/internal/auth"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
)

// ControlInfo is what `agent-office pair` prints next to a code.
type ControlInfo struct {
	LocalURL    string // e.g. http://127.0.0.1:7680/
	RemoteHosts []string
}

// ControlCode answers POST /pairing-codes on the control socket.
type ControlCode struct {
	auth.Code
	LocalURL   string   `json:"local_url"`
	RemoteURLs []string `json:"remote_urls"`
}

// ControlToken answers POST /tokens on the control socket.
type ControlToken struct {
	Token  string      `json:"token"`
	Device auth.Device `json:"device"`
}

// NewControl is the handler of the daemon's control socket (ADR 0039): a
// unix socket only the user can open (0600 in a 0700 directory), so it can
// hand out the first pairing code and tokens for scripts. Browsers cannot
// reach it.
//
// It also manages projects for `agent-office project` (ADR 0046), so the CLI
// changes them through the daemon, the database's one writer. {project} is
// an id or a unique name. hub may be nil (no project routes).
func NewControl(m *auth.Manager, hub *engine.Hub, info ControlInfo, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	if hub != nil {
		controlProjects(mux, hub, log)
	}
	mux.HandleFunc("POST /pairing-codes", func(w http.ResponseWriter, _ *http.Request) {
		c, err := m.NewCode("")
		if errors.Is(err, auth.ErrTooManyCodes) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			log.Error("pairing code", "err", err)
			writeError(w, http.StatusInternalServerError, "could not create a pairing code")
			return
		}
		writeJSON(w, http.StatusCreated, ControlCode{Code: c, LocalURL: info.LocalURL, RemoteURLs: RemoteURLs(info.RemoteHosts)})
	})
	mux.HandleFunc("POST /tokens", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		tok, d, err := m.CreateToken(r.Context(), req.Name)
		if errors.Is(err, auth.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			log.Error("create token", "err", err)
			writeError(w, http.StatusInternalServerError, "could not create a token")
			return
		}
		writeJSON(w, http.StatusCreated, ControlToken{Token: tok, Device: d})
	})
	return mux
}

// controlProjects adds the project routes of the control socket.
func controlProjects(mux *http.ServeMux, hub *engine.Hub, log *slog.Logger) {
	s := &server{hub: hub, log: log}
	resolve := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		p, err := hub.Find(r.PathValue("project"))
		if err != nil {
			s.engineError(w, err)
			return "", false
		}
		return p.ID, true
	}
	mux.HandleFunc("GET /projects", s.listProjects)
	mux.HandleFunc("POST /projects", s.addProject)
	mux.HandleFunc("PATCH /projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := resolve(w, r); ok {
			r.SetPathValue("project", id)
			s.updateProject(w, r)
		}
	})
	mux.HandleFunc("DELETE /projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := resolve(w, r); ok {
			r.SetPathValue("project", id)
			s.removeProject(w, r)
		}
	})
	mux.HandleFunc("POST /projects/{project}/retry", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := resolve(w, r); ok {
			r.SetPathValue("project", id)
			s.retryProject(w, r)
		}
	})
	mux.HandleFunc("GET /projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := resolve(w, r); ok {
			r.SetPathValue("project", id)
			s.getProject(w, r)
		}
	})
}
