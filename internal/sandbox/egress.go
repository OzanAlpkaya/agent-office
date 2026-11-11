package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultEgressAllow is the allowlist every worker gets: the package
// registries agents need to build and test (decided by Ozan, 2026-11-07).
// Anthropic needs no entry: its API goes through the API socket (ADR 0016).
var DefaultEgressAllow = []string{"proxy.golang.org", "sum.golang.org", "registry.npmjs.org"}

// Allowlist holds host names a worker may CONNECT to. An entry "example.com"
// matches exactly that name; ".example.com" matches its subdomains (not the
// name itself).
type Allowlist struct {
	exact    map[string]bool
	suffixes []string
}

// ParseAllowlist validates entries: lower-case DNS names (or ".suffix"), no
// IP literals, ports, wildcards or schemes.
func ParseAllowlist(entries []string) (Allowlist, error) {
	a := Allowlist{exact: map[string]bool{}}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		name, suffix := strings.CutPrefix(e, ".")
		if !validHostname(name) {
			return Allowlist{}, fmt.Errorf("sandbox: invalid egress allowlist entry %q (want a host name such as example.com or .example.com)", e)
		}
		if suffix {
			a.suffixes = append(a.suffixes, "."+name)
		} else {
			a.exact[name] = true
		}
	}
	return a, nil
}

// Allows reports whether host (a name, not an IP) is on the list.
func (a Allowlist) Allows(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !validHostname(host) {
		return false
	}
	if a.exact[host] {
		return true
	}
	for _, s := range a.suffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// ValidHost reports whether h is an exact host name the allowlist could hold:
// lower case, at least two labels, no IP literal, port, wildcard or ".suffix".
// Access requests (ADR 0019) may only name such hosts.
func ValidHost(h string) bool { return validHostname(h) }

// Grants is a per-agent set of host names a human approved at run time
// (request_access, ADR 0019). It is safe for concurrent use; the zero value is
// empty, and a nil *Grants allows nothing.
type Grants struct {
	mu    sync.Mutex
	hosts map[string]bool
}

// Add grants host; it must be a ValidHost.
func (g *Grants) Add(host string) error {
	if !validHostname(host) {
		return fmt.Errorf("sandbox: invalid host name %q", host)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hosts == nil {
		g.hosts = map[string]bool{}
	}
	g.hosts[host] = true
	return nil
}

// Set replaces the granted hosts with hosts, each a ValidHost: what a revoked
// grant leaves (Phase 17). On an invalid name nothing changes.
func (g *Grants) Set(hosts []string) error {
	next := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if !validHostname(h) {
			return fmt.Errorf("sandbox: invalid host name %q", h)
		}
		next[h] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hosts = next
	return nil
}

// Allows reports whether host was granted.
func (g *Grants) Allows(host string) bool {
	if g == nil {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hosts[host]
}

// validHostname accepts lower-case DNS names with at least two labels and
// rejects IP literals.
func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// publicOnly is a net.Dialer Control function that refuses non-public
// addresses. The proxy resolves names on the host, so an allowlisted name
// pointing at loopback or a private network would otherwise reach the host's
// own services.
func publicOnly(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	a := ap.Addr().Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || isSharedOrReserved(a) {
		return fmt.Errorf("sandbox: refusing non-public address %s", a)
	}
	return nil
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (also Tailscale)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
}

func isSharedOrReserved(a netip.Addr) bool {
	for _, p := range reserved {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// EgressProxy serves one agent's egress socket: HTTP CONNECT to port 443 of
// allowlisted names only. TLS stays end to end; the proxy never sees
// contents.
type EgressProxy struct {
	Allow   Allowlist
	Grants  *Grants                                                           // optional: hosts approved for this agent at run time (ADR 0019)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error) // nil: public addresses only, 10 s timeout
	OnEvent func(Event)                                                       // optional; called for denials and failures
	Log     *slog.Logger                                                      // carries the agent id
	MaxConn int                                                               // concurrent tunnels; 0 = 64
}

const (
	egressHeaderTimeout = 10 * time.Second
	egressMaxHeader     = 16 << 10
)

// Serve accepts connections on l until it is closed.
func (p *EgressProxy) Serve(l net.Listener) error {
	maxConn := p.MaxConn
	if maxConn <= 0 {
		maxConn = 64
	}
	sem := make(chan struct{}, maxConn)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			p.event(EgressDenied, "too many concurrent connections")
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.handle(c)
		}()
	}
}

func (p *EgressProxy) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(egressHeaderTimeout))
	br := bufio.NewReader(io.LimitReader(c, egressMaxHeader))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		p.event(EgressDenied, req.Method+" "+req.Host)
		reply(c, http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil || port != "443" || !(p.Allow.Allows(host) || p.Grants.Allows(host)) {
		p.event(EgressDenied, req.Host)
		reply(c, http.StatusForbidden)
		return
	}
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	up, err := dial(ctx, "tcp", net.JoinHostPort(strings.ToLower(host), port))
	cancel()
	if err != nil {
		p.event(EgressFailed, req.Host)
		p.Log.Warn("egress upstream failed", "target", sanitize(req.Host), "err", err)
		reply(c, http.StatusBadGateway)
		return
	}
	defer up.Close()
	_ = c.SetReadDeadline(time.Time{})
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	p.Log.Debug("egress tunnel", "target", sanitize(req.Host))
	// Bytes the client sent after the request header (normally none before
	// the 200) were read into br; forward them first.
	if n := br.Buffered(); n > 0 {
		b, _ := br.Peek(n)
		if _, err := up.Write(b); err != nil {
			return
		}
	}
	splice(c, up)
}

func (p *EgressProxy) event(kind EventKind, detail string) {
	detail = sanitize(detail)
	p.Log.Info("egress refused", "kind", kind, "target", detail)
	if p.OnEvent != nil {
		p.OnEvent(Event{Kind: kind, Detail: detail, At: time.Now().UTC()})
	}
}

func reply(c net.Conn, status int) {
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
}

// splice copies both ways until both directions are done.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
