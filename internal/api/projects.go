package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/office"
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

// remote reports whether a request came through tailscale serve rather
// than from this machine; the control socket (the CLI) is local.
func (s *server) remote(r *http.Request) bool {
	if s.opts.Port == "" && len(s.opts.RemoteHosts) == 0 {
		return false // the control socket
	}
	_, remote := s.hostKind(r.Host)
	return remote
}

// localOnly is the refusal of a change that widens what agents can reach,
// asked from a remote device (found in the Phase 14 security review).
const localOnly = "adding a repository and changing a project's pack, secrets_dir, egress_allow or GitHub settings " +
	"need this machine: use the UI on localhost or `agent-office project`"

func (s *server) addProject(w http.ResponseWriter, r *http.Request) {
	var req projectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.remote(r) {
		writeError(w, http.StatusForbidden, localOnly)
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
	if req.Settings != nil && s.remote(r) {
		cur, err := s.hub.Project(r.PathValue("project"))
		if err != nil {
			s.engineError(w, err)
			return
		}
		if changed := cur.Settings.SandboxChanges(*req.Settings); len(changed) > 0 {
			writeError(w, http.StatusForbidden, localOnly+" (changed: "+strings.Join(changed, ", ")+")")
			return
		}
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
// from its events stream. The page says on it whether it is visible
// ({"type":"presence","visible":bool}); the stream carries the user's
// presence back (Phase 18, ADR 0058).
func (s *server) hubEventsWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	updates, unsubscribe := s.hub.Subscribe() // before the snapshot, so no update is missed
	defer unsubscribe()
	dev, _ := s.device(r)
	pageID := randomID()
	defer s.hub.DropPage(pageID)
	// Not the request's context, as in terminalWS: a revoked device gets StatusSignedOut.
	ctx, stop := context.WithCancel(context.WithoutCancel(r.Context()))
	defer stop()
	c.SetReadLimit(maxPresenceMessage)
	go func() {
		defer stop() // the page closed the stream or sent something else
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				Type    string `json:"type"`
				Visible *bool  `json:"visible"`
			}
			if json.Unmarshal(b, &m) != nil || m.Type != "presence" || m.Visible == nil {
				s.log.Debug("hub events: ignoring a message from the page")
				continue
			}
			s.hub.SetPage(pageID, dev.Name, *m.Visible)
		}
	}()
	write := func(v any) error {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}
	if err := write(map[string]any{"type": "projects", "projects": s.hub.Projects()}); err != nil {
		return
	}
	if err := write(engine.HubUpdate{Type: "presence", Presence: ptr(s.hub.Presence())}); err != nil {
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

// maxPresenceMessage bounds what a page may send on the hub stream.
const maxPresenceMessage = 1024

func ptr[T any](v T) *T { return &v }

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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

// officeLayouts lists the shipped office layouts (Phase 18, ADR 0057).
func (s *server) officeLayouts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, office.Presets())
}

// layoutCheck is a layout's YAML, parsed for the settings' preview: the
// layout when it is valid, else every problem.
type layoutCheck struct {
	Layout   *office.Layout `json:"layout,omitempty"`
	Problems []string       `json:"problems"`
}

// checkOfficeLayout parses a layout without storing it, for the preview.
func (s *server) checkOfficeLayout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	l, err := office.Parse(body.YAML)
	if err != nil {
		writeJSON(w, http.StatusOK, layoutCheck{Problems: office.Problems(err)})
		return
	}
	writeJSON(w, http.StatusOK, layoutCheck{Layout: &l, Problems: []string{}})
}
