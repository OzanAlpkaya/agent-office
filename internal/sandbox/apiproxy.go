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

	"github.com/OzanAlpkaya/agent-office/internal/usage"
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
	Log       *slog.Logger // carries the agent and session ids (ADR 0028)
	MaxBody   int64        // 0: MaxAPIRequestBody

	// OnUsage, if set, receives the token usage of every successful response
	// as it streams through (ADR 0026): numbers and ids only.
	OnUsage func(usage.Message)
	// Allow, if set, is asked before a request is forwarded; an error refuses
	// it with a 403 and its message (budgets, ADR 0027). It must not block.
	Allow func() error
}

// reqInfo carries what the response side learns to the request's log line.
type reqInfo struct{ requestID string }

type reqInfoKey struct{}

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
			// Without the client's Accept-Encoding, Go's transport negotiates
			// gzip itself and decompresses, so the meter reads plain text.
			r.Out.Header.Del("Accept-Encoding")
		},
		ModifyResponse: func(resp *http.Response) error {
			rid := sanitizeID(resp.Header.Get("Request-Id"))
			if info, ok := resp.Request.Context().Value(reqInfoKey{}).(*reqInfo); ok {
				info.requestID = rid
			}
			if p.OnUsage != nil && resp.StatusCode == http.StatusOK && resp.Header.Get("Content-Encoding") == "" {
				resp.Body = usage.MeterBody(resp.Body, resp.Header.Get("Content-Type"), rid, p.OnUsage, func(err error) {
					p.event(APIMeter, resp.Request.Method+" "+resp.Request.URL.Path)
					p.Log.Warn("api usage not readable", "request_id", rid, "err", err)
				})
			}
			return nil
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
		info := &reqInfo{}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, info))
		defer func() {
			p.Log.Debug("api request", "method", sanitize(r.Method), "path", sanitize(r.URL.Path),
				"status", sw.status, "duration_ms", time.Since(start).Milliseconds(), "request_id", info.requestID)
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
		if p.Allow != nil {
			if err := p.Allow(); err != nil {
				p.event(APIBudget, err.Error())
				apiError(sw, http.StatusForbidden, "permission_error", "agent-office: "+err.Error())
				return
			}
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

// sanitizeID keeps an upstream id header only if it looks like an id.
func sanitizeID(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ""
		}
	}
	return s
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
