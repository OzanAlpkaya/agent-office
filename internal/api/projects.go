package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
)

type projectKey struct{}

// inProject resolves {project} to its engine (ADR 0046): an unknown project
// is 404, one that could not open is 409 with the reason. Every id further
// down the path is looked up in that project only.
func (s *server) inProject(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e, err := s.hub.Engine(r.PathValue("project"))
		if err != nil {
			s.engineError(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), projectKey{}, e)))
	}
}

// eng is the engine of the request's project; only routes marked project
// call it.
func eng(r *http.Request) *engine.Engine { return r.Context().Value(projectKey{}).(*engine.Engine) }

// daemonStatusView is GET /api/status: the daemon-wide state.
type daemonStatusView struct {
	engine.HubStatus
	RemoteHosts []string `json:"remote_hosts"`
	Version     string   `json:"version"`
}

func (s *server) daemonStatus(w http.ResponseWriter, _ *http.Request) {
	hosts := s.opts.RemoteHosts
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, daemonStatusView{HubStatus: s.hub.Status(), RemoteHosts: hosts, Version: s.opts.Version})
}

// daemonUsage is the usage totals per project and overall.
func (s *server) daemonUsage(w http.ResponseWriter, r *http.Request) {
	u, err := s.hub.Usage(r.Context())
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *server) listProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.Projects())
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.Project(r.PathValue("project"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// projectRequest is the body of POST /api/projects.
type projectRequest struct {
	Repo     string                 `json:"repo"`
	Name     string                 `json:"name"`
	Settings engine.ProjectSettings `json:"settings"`
}

func (s *server) addProject(w http.ResponseWriter, r *http.Request) {
	var req projectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.hub.Add(r.Context(), engine.AddProject{Repo: req.Repo, Name: req.Name, Settings: req.Settings})
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// projectPatch is the body of PATCH /api/projects/{project}: settings
// replace the project's overrides as a whole.
type projectPatch struct {
	Name     *string                 `json:"name"`
	Settings *engine.ProjectSettings `json:"settings"`
}

func (s *server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req projectPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.hub.Update(r.Context(), r.PathValue("project"), engine.UpdateProject{Name: req.Name, Settings: req.Settings})
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) removeProject(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Remove(r.Context(), r.PathValue("project")); err != nil {
		s.engineError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) retryProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.Retry(r.Context(), r.PathValue("project"))
	if err != nil {
		s.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// hubEventsWS streams the project list: a snapshot, then one message per
// change ("project", "project_removed"). Each project's own updates come
// from its events stream.
func (s *server) hubEventsWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	updates, unsubscribe := s.hub.Subscribe() // before the snapshot, so no update is missed
	defer unsubscribe()
	ctx := c.CloseRead(context.WithoutCancel(r.Context()))
	write := func(v any) error {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}
	if err := write(map[string]any{"type": "projects", "projects": s.hub.Projects()}); err != nil {
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

// projectErrorStatus maps the registry's errors.
func projectErrorStatus(err error) (int, bool) {
	switch {
	case errors.Is(err, engine.ErrProjectExists), errors.Is(err, engine.ErrProjectBusy),
		errors.Is(err, engine.ErrProjectUnavailable):
		return http.StatusConflict, true
	case errors.Is(err, engine.ErrShuttingDown):
		return http.StatusServiceUnavailable, true
	}
	return 0, false
}
