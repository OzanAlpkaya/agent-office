package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/auth"
)

// CookieName is the session cookie holding a browser's device token (ADR 0039).
const CookieName = "ao_session"

// StatusSignedOut closes a WebSocket whose device was revoked; the UI then
// shows its pairing screen.
const StatusSignedOut websocket.StatusCode = 4401

// cookieMaxAge is how long the browser keeps the cookie; the daemon ends a
// device after auth.IdleExpiry without use regardless.
const cookieMaxAge = 365 * 24 * time.Hour

type deviceKey struct{}

// token returns the credential of a request: the session cookie, or a bearer
// token for scripts.
func token(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if t, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(t)
		}
		return ""
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return c.Value
	}
	return ""
}

// device returns the paired device a request comes from.
func (s *server) device(r *http.Request) (auth.Device, bool) {
	if d, ok := r.Context().Value(deviceKey{}).(auth.Device); ok {
		return d, true
	}
	t := token(r)
	if t == "" {
		return auth.Device{}, false
	}
	return s.opts.Auth.Authenticate(r.Context(), t)
}

// requireDevice refuses a request without a paired device's credential. The
// handler's context is cancelled when the device is revoked, which ends its
// WebSockets (closeIfSignedOut).
func (s *server) requireDevice(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.device(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "this browser is not paired with agent-office: run `agent-office pair` on its machine")
			return
		}
		ctx, done := s.opts.Auth.Watch(r.Context(), d.ID)
		defer done()
		h(w, r.WithContext(context.WithValue(ctx, deviceKey{}, d)))
	}
}

// closeIfSignedOut closes a WebSocket with StatusSignedOut when its request
// ended because the device was revoked.
func closeIfSignedOut(c *websocket.Conn, r *http.Request) {
	if errors.Is(context.Cause(r.Context()), auth.ErrRevoked) {
		c.Close(StatusSignedOut, "signed out")
	}
}

// closeFailed closes a WebSocket whose setup failed: as signed out when the
// device was revoked meanwhile (its context was cancelled), else as an error.
func closeFailed(c *websocket.Conn, r *http.Request, reason string) {
	if errors.Is(context.Cause(r.Context()), auth.ErrRevoked) {
		c.Close(StatusSignedOut, "signed out")
		return
	}
	c.Close(websocket.StatusInternalError, reason)
}

// securityHeaders applies to every response: no framing (the approval
// buttons cannot be clickjacked), no sniffing, no referrer; API answers are
// never cached.
func (s *server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp(r))
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// csp limits the UI to this daemon's own scripts, styles, images and
// connections, so a rendering slip with agent-written text cannot load or
// send anything elsewhere. Inline styles stay allowed (React style props,
// xterm.js); PixiJS runs without eval (pixi.js/unsafe-eval). The WebSocket
// origin is named explicitly, for browsers whose 'self' does not cover ws:.
func (s *server) csp(r *http.Request) string {
	connect := "'self'"
	if local, remote := s.hostKind(r.Host); local || remote {
		connect += " ws://" + r.Host + " wss://" + r.Host
	}
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
		"font-src 'self' data:; connect-src " + connect + "; worker-src 'self' blob:; manifest-src 'self'; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// isRemote says whether a request came through one of the tailnet names.
func (s *server) isRemote(r *http.Request) bool {
	_, remote := s.hostKind(r.Host)
	return remote
}

func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	c := &http.Cookie{
		Name: CookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || s.isRemote(r), MaxAge: int(maxAge / time.Second),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// sessionView answers GET /api/auth/session. Unpaired callers learn nothing else.
type sessionView struct {
	Authenticated bool         `json:"authenticated"`
	Device        *auth.Device `json:"device,omitempty"`
	Remote        bool         `json:"remote"`
}

func (s *server) authSession(w http.ResponseWriter, r *http.Request) {
	v := sessionView{Remote: s.isRemote(r)}
	if d, ok := s.device(r); ok {
		v.Authenticated, v.Device = true, &d
	}
	writeJSON(w, http.StatusOK, v)
}

type pairRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// authPair pairs this browser with a code and sets its session cookie.
func (s *server) authPair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	via := "local"
	if s.isRemote(r) {
		via = "tailscale:" + r.Header.Get("Tailscale-User-Login")
	}
	tok, d, err := s.opts.Auth.Pair(r.Context(), req.Code, req.Name, via)
	switch {
	case errors.Is(err, auth.ErrTooManyAttempts):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	case errors.Is(err, auth.ErrInvalidName):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.log.Error("pair device", "err", err)
		writeError(w, http.StatusInternalServerError, "pairing failed")
		return
	}
	s.setSessionCookie(w, r, tok, cookieMaxAge)
	writeJSON(w, http.StatusCreated, sessionView{Authenticated: true, Device: &d, Remote: s.isRemote(r)})
}

// authLogout signs this device out: it is revoked, not only forgotten by the browser.
func (s *server) authLogout(w http.ResponseWriter, r *http.Request) {
	d, _ := s.device(r)
	if err := s.opts.Auth.Revoke(context.WithoutCancel(r.Context()), d.ID); err != nil && !errors.Is(err, auth.ErrNotFound) {
		s.log.Error("sign out", "err", err)
		writeError(w, http.StatusInternalServerError, "sign out failed")
		return
	}
	s.setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// deviceView is a paired device and whether it is the caller.
type deviceView struct {
	auth.Device
	Current bool `json:"current"`
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	me, _ := s.device(r)
	ds := s.opts.Auth.Devices()
	out := make([]deviceView, 0, len(ds))
	for _, d := range ds {
		out = append(out, deviceView{Device: d, Current: d.ID == me.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

// codeView is a new pairing code and where to use it.
type codeView struct {
	auth.Code
	RemoteURLs []string `json:"remote_urls"`
}

func (s *server) newPairingCode(w http.ResponseWriter, r *http.Request) {
	me, _ := s.device(r)
	// Devices paired with this code are signed out with the device that made it.
	c, err := s.opts.Auth.NewCode(me.ID)
	if errors.Is(err, auth.ErrTooManyCodes) {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if err != nil {
		s.log.Error("pairing code", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create a pairing code")
		return
	}
	writeJSON(w, http.StatusCreated, codeView{Code: c, RemoteURLs: RemoteURLs(s.opts.RemoteHosts)})
}

func (s *server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	me, _ := s.device(r)
	id := r.PathValue("id")
	// Not the request's context: revoking the caller itself cancels it.
	err := s.opts.Auth.Revoke(context.WithoutCancel(r.Context()), id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.log.Error("revoke device", "err", err)
		writeError(w, http.StatusInternalServerError, "could not sign the device out")
		return
	}
	if id == me.ID {
		s.setSessionCookie(w, r, "", -1)
	}
	w.WriteHeader(http.StatusNoContent)
}

// signOutOthers signs out every device but the caller (a lost phone, a
// device nobody recognises).
func (s *server) signOutOthers(w http.ResponseWriter, r *http.Request) {
	me, _ := s.device(r)
	n, err := s.opts.Auth.RevokeOthers(context.WithoutCancel(r.Context()), me.ID)
	if err != nil {
		s.log.Error("sign out other devices", "err", err)
		writeError(w, http.StatusInternalServerError, "could not sign the other devices out")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"signed_out": n})
}

// RemoteURLs are the https URLs of the daemon's tailnet names.
func RemoteURLs(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, "https://"+h+"/")
	}
	return out
}
