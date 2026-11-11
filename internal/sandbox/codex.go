package sandbox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

// Codex in the sandbox (ADR 0049): the agent's CODEX_HOME holds a placeholder
// ChatGPT login; its ChatGPT backend base URL points at the API socket, where
// the daemon puts the user's real access token on the request. The refresh
// token is never read: refreshing stays with the user's own Codex CLI.

// ChatGPTBackend is where Codex's ChatGPT-plan requests go.
var ChatGPTBackend = &url.URL{Scheme: "https", Host: "chatgpt.com"}

// ErrCodexAuth wraps every problem with the Codex login file.
var ErrCodexAuth = errors.New("Codex login")

// maxCodexAuth bounds the login file (Spike G: about 4 KB).
const maxCodexAuth = 64 << 10

// CodexLogin is what the daemon uses of the user's Codex login.
type CodexLogin struct {
	Token   secret.Secret // the access token
	Account string        // the ChatGPT account (workspace) id: an identifier, not a credential
	Plan    string        // e.g. "plus", from the token's claims
	Expires time.Time     // the access token's exp claim; zero if unreadable
}

var accountRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// DefaultCodexAuthFile is $CODEX_HOME/auth.json, or ~/.codex/auth.json.
func DefaultCodexAuthFile() (string, error) {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCodexAuth, err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

// ReadCodexLogin reads the access token and account id of a ChatGPT login
// (`codex login`) from path, with the checks of the token files (user-only,
// no symlink). Errors name the file and the fix, never contents.
func ReadCodexLogin(path string) (CodexLogin, error) {
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := secret.ReadJSONFile(path, ErrCodexAuth, maxCodexAuth, &f); err != nil {
		return CodexLogin{}, err
	}
	fail := func(what string) (CodexLogin, error) {
		return CodexLogin{}, fmt.Errorf("%w %s: %s; sign in with `codex login` (ChatGPT)", ErrCodexAuth, path, what)
	}
	if f.Tokens == nil || f.AuthMode != "chatgpt" {
		return fail("not a ChatGPT login")
	}
	tok := f.Tokens.AccessToken
	if tok == "" || len(tok) > 16<<10 {
		return fail("no access token")
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x21 || c > 0x7e {
			return fail("the access token is not printable ASCII")
		}
	}
	if !accountRe.MatchString(f.Tokens.AccountID) {
		return fail("no account id")
	}
	l := CodexLogin{Token: secret.New(tok), Account: f.Tokens.AccountID}
	l.Plan, l.Expires = jwtClaims(tok)
	return l, nil
}

var planRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// jwtClaims reads the plan type and expiry from a JWT's claims, without
// checking its signature (the daemon only uses them for the placeholder and a
// warning; the server checks the token).
func jwtClaims(tok string) (plan string, exp time.Time) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", time.Time{}
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}
	}
	var c struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(b, &c) != nil {
		return "", time.Time{}
	}
	if planRe.MatchString(c.Auth.Plan) {
		plan = c.Auth.Plan
	}
	if c.Exp > 0 {
		exp = time.Unix(c.Exp, 0).UTC()
	}
	return plan, exp
}

// LocalCA is the daemon's throwaway certificate authority for the loopback
// TLS leg Codex insists on (its TUI refuses a plain-HTTP ChatGPT base URL).
// It lives in memory for one daemon run; containers trust its certificate
// through CODEX_CA_CERTIFICATE. Nothing but the loopback server certificate is
// ever signed with it.
type LocalCA struct {
	certPEM []byte
	server  tls.Certificate
}

// NewLocalCA creates the CA and a certificate for 127.0.0.1 and localhost.
func NewLocalCA() (*LocalCA, error) {
	caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	serial := func() *big.Int {
		n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
		return n
	}
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "agent-office loopback CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(400 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
		PermittedIPRanges: []*net.IPNet{{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(32, 32)}}, PermittedDNSDomains: []string{"localhost"},
		PermittedDNSDomainsCritical: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPriv.PublicKey, caPriv)
	if err != nil {
		return nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	srvPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	srv := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(400 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames: []string{"localhost"}}
	srvDER, err := x509.CreateCertificate(rand.Reader, srv, caCert, &srvPriv.PublicKey, caPriv)
	if err != nil {
		return nil, err
	}
	return &LocalCA{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		server:  tls.Certificate{Certificate: [][]byte{srvDER, caDER}, PrivateKey: srvPriv, Leaf: nil},
	}, nil
}

// CertPEM is the CA certificate, for the container's CODEX_CA_CERTIFICATE.
func (c *LocalCA) CertPEM() []byte { return append([]byte{}, c.certPEM...) }

// Listener serves TLS with the loopback certificate on l.
func (c *LocalCA) Listener(l net.Listener) net.Listener {
	return tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{c.server}, MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"}})
}

// codexRoutes are the ChatGPT backend requests a Codex agent may make
// (Spike G: with plugins, apps, analytics and updates off).
var codexRoutes = map[string]bool{
	"GET /backend-api/codex/models":        true,
	"GET /backend-api/wham/accounts/check": true,
	"GET /backend-api/wham/settings/user":  true,
	"GET /backend-api/wham/usage":          true, // the plan's rate limits, shown in the TUI
	"POST /backend-api/codex/responses":    true,
}

// CodexProxy is the API socket of a Codex agent: like APIProxy for Claude
// Code, it accepts only the placeholder login, allows a fixed set of routes,
// adds the real credential, meters usage and applies budgets.
type CodexProxy struct {
	Login       func() (CodexLogin, error) // read at every request
	Placeholder string                     // the placeholder login's bearer
	Upstream    *url.URL                   // nil: ChatGPTBackend
	Transport   http.RoundTripper          // nil: NewAPITransport()
	OnEvent     func(Event)
	Log         *slog.Logger
	MaxBody     int64 // 0: MaxAPIRequestBody
	OnUsage     func(usage.Message)
	Allow       func() error
}

type codexLoginKey struct{}

// Handler returns the proxy's HTTP handler; serve it with LocalCA.Listener.
func (p *CodexProxy) Handler() http.Handler {
	up := p.Upstream
	if up == nil {
		up = ChatGPTBackend
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
			login := r.In.Context().Value(codexLoginKey{}).(CodexLogin)
			r.SetURL(up)
			r.Out.Host = up.Host
			r.Out.Header.Set("Authorization", "Bearer "+login.Token.Reveal())
			r.Out.Header.Set("ChatGPT-Account-Id", login.Account)
			r.Out.Header.Del("Accept-Encoding")
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusUnauthorized {
				p.event(APIAuth, "the ChatGPT backend refused the Codex login: run `codex login` (or codex once) to refresh it")
			}
			if p.OnUsage != nil && resp.StatusCode == http.StatusOK && resp.Request.Method == http.MethodPost {
				if resp.Header.Get("Content-Encoding") != "" {
					p.event(APIMeter, resp.Request.URL.Path+": encoded response not metered")
					return nil
				}
				rid := sanitizeID(resp.Header.Get("X-Request-Id"))
				resp.Body = usage.MeterResponses(resp.Body, resp.Header.Get("Content-Type"), rid, p.OnUsage, func(err error) {
					p.event(APIMeter, resp.Request.Method+" "+resp.Request.URL.Path)
					p.Log.Warn("codex usage not readable", "err", err)
				})
			}
			return nil
		},
		Transport:     tr,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				p.event(APIRejected, fmt.Sprintf("%s %s: body larger than %d bytes", r.Method, r.URL.Path, maxBody))
				codexError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			p.event(APIError, r.Method+" "+r.URL.Path)
			p.Log.Warn("codex upstream failed", "path", sanitize(r.URL.Path), "err", err)
			codexError(w, http.StatusBadGateway, "agent-office could not reach the ChatGPT backend")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			p.Log.Debug("codex api request", "method", sanitize(r.Method), "path", sanitize(r.URL.Path), "status", sw.status,
				"duration_ms", time.Since(start).Milliseconds())
		}()
		route := r.Method + " " + r.URL.Path
		if !codexRoutes[route] {
			p.event(APIRejected, route)
			codexError(sw, http.StatusForbidden, "agent-office does not allow "+sanitize(route))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+p.Placeholder || len(r.Header.Values("Authorization")) != 1 {
			p.event(APIAuth, route)
			codexError(sw, http.StatusUnauthorized, "unexpected credentials on the agent-office API socket")
			return
		}
		if r.ContentLength > maxBody {
			p.event(APIRejected, fmt.Sprintf("%s: body larger than %d bytes", route, maxBody))
			codexError(sw, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		login, err := p.Login()
		if err != nil {
			p.event(APIAuth, "Codex login: "+err.Error())
			codexError(sw, http.StatusBadGateway, "agent-office could not read the Codex login on the host")
			return
		}
		if acct := r.Header.Get("ChatGPT-Account-Id"); acct != "" && acct != login.Account {
			p.event(APIAuth, route+": another ChatGPT account")
			codexError(sw, http.StatusUnauthorized, "unexpected ChatGPT account on the agent-office API socket")
			return
		}
		if r.Method == http.MethodPost && p.Allow != nil {
			if err := p.Allow(); err != nil {
				p.event(APIBudget, err.Error())
				codexError(sw, http.StatusForbidden, "agent-office: "+err.Error())
				return
			}
		}
		r.Body = http.MaxBytesReader(sw, r.Body, maxBody)
		rp.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), codexLoginKey{}, login)))
	})
}

func (p *CodexProxy) event(kind EventKind, detail string) {
	detail = sanitize(detail)
	p.Log.Info("api request refused", "kind", kind, "target", detail)
	if p.OnEvent != nil {
		p.OnEvent(Event{Kind: kind, Detail: detail, At: time.Now().UTC()})
	}
}

func codexError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": "agent_office"}})
	_, _ = w.Write(b)
}
