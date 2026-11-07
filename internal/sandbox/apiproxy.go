package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Placeholder is the credential a sandboxed Claude Code gets instead of the
// real token (CLAUDE_CODE_OAUTH_TOKEN). The API proxy replaces it; it is not
// a secret.
const Placeholder = "agent-office-placeholder-not-a-credential"

// AnthropicAPI is the upstream of the API proxy.
var AnthropicAPI = &url.URL{Scheme: "https", Host: "api.anthropic.com"}

// MaxAPIRequestBody bounds a request body streamed through the API proxy.
const MaxAPIRequestBody = 32 << 20

// APIProxy serves one agent's API socket (ADR 0016). It forwards only
// POST /v1/messages, replaces the placeholder credential with the real token,
// and streams responses through without buffering. Everything else is
// refused, logged and reported as an Event, so a Claude Code version that
// needs another endpoint fails visibly.
type APIProxy struct {
	Token     Secret
	Upstream  *url.URL          // nil: AnthropicAPI
	Transport http.RoundTripper // nil: NewAPITransport()
	OnEvent   func(Event)
	Log       *slog.Logger // carries the agent id
	MaxBody   int64        // 0: MaxAPIRequestBody
}

// NewAPITransport returns the upstream transport: bounded dial, TLS and
// response-header times, but no overall timeout, since responses are long
// streams.
func NewAPITransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
	}
}

// Handler returns the HTTP handler to serve on the API socket.
func (p *APIProxy) Handler() http.Handler {
	up := p.Upstream
	if up == nil {
		up = AnthropicAPI
	}
	tr := p.Transport
	if tr == nil {
		tr = NewAPITransport()
	}
	maxBody := p.MaxBody
	if maxBody <= 0 {
		maxBody = MaxAPIRequestBody
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(up)
			r.Out.Host = up.Host
			r.Out.Header.Del("X-Api-Key")
			r.Out.Header.Set("Authorization", "Bearer "+p.Token.Reveal())
		},
		Transport:     tr,
		FlushInterval: -1, // stream every write straight through
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				p.event(APIRejected, fmt.Sprintf("%s %s: body larger than %d bytes", r.Method, r.URL.Path, maxBody))
				apiError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
				return
			}
			if errors.Is(err, context.Canceled) {
				return // the client went away
			}
			p.event(APIError, r.Method+" "+r.URL.Path)
			p.Log.Warn("api upstream failed", "path", sanitize(r.URL.Path), "err", err)
			apiError(w, http.StatusBadGateway, "api_error", "agent-office could not reach the Anthropic API")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			p.Log.Debug("api request", "method", sanitize(r.Method), "path", sanitize(r.URL.Path),
				"status", sw.status, "duration_ms", time.Since(start).Milliseconds())
		}()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			p.event(APIRejected, r.Method+" "+r.URL.Path)
			apiError(sw, http.StatusForbidden, "permission_error",
				fmt.Sprintf("agent-office allows only POST /v1/messages, not %s %s", sanitize(r.Method), sanitize(r.URL.Path)))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+Placeholder || len(r.Header.Values("Authorization")) != 1 ||
			r.Header.Get("X-Api-Key") != "" {
			p.event(APIAuth, r.Method+" "+r.URL.Path)
			apiError(sw, http.StatusUnauthorized, "authentication_error", "unexpected credentials on the agent-office API socket")
			return
		}
		if r.ContentLength > maxBody { // declared too large: never contact the upstream
			p.event(APIRejected, fmt.Sprintf("%s %s: body larger than %d bytes", r.Method, r.URL.Path, maxBody))
			apiError(sw, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return
		}
		r.Body = http.MaxBytesReader(sw, r.Body, maxBody) // undeclared (chunked) bodies
		rp.ServeHTTP(sw, r)
	})
}

func (p *APIProxy) event(kind EventKind, detail string) {
	detail = sanitize(detail)
	p.Log.Info("api request refused", "kind", kind, "target", detail)
	if p.OnEvent != nil {
		p.OnEvent(Event{Kind: kind, Detail: detail, At: time.Now().UTC()})
	}
}

// apiError writes an error in the Anthropic API's JSON shape, so the client
// shows a readable message.
func apiError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, typ, msg)
}

// statusWriter records the status for logging. Unwrap lets the reverse proxy
// reach the underlying writer's Flush (http.ResponseController).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
