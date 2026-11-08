package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/OzanAlpkaya/agent-office/internal/auth"
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
func NewControl(m *auth.Manager, info ControlInfo, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pairing-codes", func(w http.ResponseWriter, _ *http.Request) {
		c, err := m.NewCode()
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
