package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/auth"
)

// noAuth makes do() send no credential.
var noAuth = map[string]string{"Authorization": ""}

func withHeaders(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// TestEveryRouteNeedsADevice walks the route table: every route that is not
// explicitly public answers 401 without a paired device's credential, so a
// route added later is covered by default (ADR 0039).
func TestEveryRouteNeedsADevice(t *testing.T) {
	f := newFixture(t)
	s := &server{opts: Options{Auth: f.auth}}
	public := map[string]bool{}
	for _, rt := range s.routes() {
		if rt.public {
			public[rt.pattern] = true
			continue
		}
		method, p, _ := strings.Cut(rt.pattern, " ")
		p = strings.NewReplacer("{id}", "x1", "{sid}", "s1", "{project}", f.pid).Replace(p)
		for name, hdr := range map[string]map[string]string{
			"none":         noAuth,
			"wrong bearer": {"Authorization": "Bearer aod_not-a-token"},
			"wrong cookie": {"Authorization": "", "Cookie": CookieName + "=aod_not-a-token"},
			"not bearer":   {"Authorization": "Basic " + f.token},
		} {
			body := ""
			if method != "GET" && method != "DELETE" {
				hdr = withHeaders(hdr, "Content-Type", "application/json")
				body = "{}"
			}
			r := f.doBody(t, method, p, hdr, body)
			if r.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s (%s): %d, want 401", method, p, name, r.StatusCode)
			}
		}
	}
	want := map[string]bool{"GET /api/health": true, "GET /api/auth/session": true, "POST /api/auth/pair": true}
	if len(public) != len(want) {
		t.Fatalf("public routes %v, want %v", public, want)
	}
	for p := range want {
		if !public[p] {
			t.Fatalf("public routes %v, want %v", public, want)
		}
	}
	// Both WebSockets refuse the handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range []string{"/api/events", f.pp + "/events", f.pp + "/agents/x1/terminal"} {
		_, resp, err := websocket.Dial(ctx, f.ws+p, nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("WebSocket %s without a device: err=%v resp=%v", p, err, resp)
		}
	}
	// The UI's static files are public.
	if r := f.do(t, "GET", "/", noAuth); r.StatusCode != http.StatusOK {
		t.Fatalf("UI without a device: %d", r.StatusCode)
	}
}

func sessionCookie(t *testing.T, r *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", CookieName, r.Header.Values("Set-Cookie"))
	return nil
}

func pair(t *testing.T, f *fixture, hdr map[string]string, code, name string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"code": code, "name": name})
	return f.doBody(t, "POST", "/api/auth/pair", withHeaders(hdr, "Content-Type", "application/json"), string(b))
}

func TestPairingAndSignOut(t *testing.T) {
	f := newFixture(t)
	var sv sessionView
	r := f.do(t, "GET", "/api/auth/session", noAuth)
	if err := json.NewDecoder(r.Body).Decode(&sv); err != nil || sv.Authenticated || sv.Device != nil {
		t.Fatalf("unpaired session %+v %v", sv, err)
	}
	var h map[string]string
	_ = json.NewDecoder(f.do(t, "GET", "/api/health", noAuth).Body).Decode(&h)
	if h["status"] != "ok" || h["version"] != "" {
		t.Fatalf("public health %v", h)
	}

	// The paired fixture device creates a code, as the Devices panel does.
	r = f.do(t, "POST", "/api/devices/pairing-codes", nil)
	var cv codeView
	if err := json.NewDecoder(r.Body).Decode(&cv); err != nil || r.StatusCode != http.StatusCreated || len(cv.Value) != 9 {
		t.Fatalf("code %d %+v %v", r.StatusCode, cv, err)
	}
	if r := pair(t, f, noAuth, "0000-0000", "phone"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", r.StatusCode)
	}
	r = pair(t, f, noAuth, cv.Value, "Pixel 8")
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("pair: %d", r.StatusCode)
	}
	c := sessionCookie(t, r)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure || c.Path != "/" || c.MaxAge <= 0 ||
		!strings.HasPrefix(c.Value, auth.TokenPrefix) {
		t.Fatalf("cookie %+v", c)
	}
	var paired sessionView
	_ = json.NewDecoder(r.Body).Decode(&paired)
	if !paired.Authenticated || paired.Device == nil || paired.Device.Name != "Pixel 8" || paired.Device.PairedVia != "local" {
		t.Fatalf("paired %+v", paired)
	}
	// The response never carries the token.
	if r := pair(t, f, noAuth, cv.Value, "again"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code used twice: %d", r.StatusCode)
	}

	cookie := withHeaders(noAuth, "Cookie", CookieName+"="+c.Value)
	if r := f.do(t, "GET", f.pp+"/agents", cookie); r.StatusCode != http.StatusOK {
		t.Fatalf("API with the cookie: %d", r.StatusCode)
	}
	_ = json.NewDecoder(f.do(t, "GET", "/api/health", cookie).Body).Decode(&h)
	if h["version"] != "test" {
		t.Fatalf("paired health %v", h)
	}
	var ds []deviceView
	_ = json.NewDecoder(f.do(t, "GET", "/api/devices", cookie).Body).Decode(&ds)
	if len(ds) != 2 || ds[0].Current || !ds[1].Current || ds[1].Name != "Pixel 8" || ds[0].Kind != auth.KindToken {
		t.Fatalf("devices %+v", ds)
	}
	raw, _ := io.ReadAll(f.do(t, "GET", "/api/devices", cookie).Body)
	if strings.Contains(string(raw), c.Value) || strings.Contains(string(raw), f.token) || strings.Contains(string(raw), "hash") {
		t.Fatalf("device list leaks a credential: %s", raw)
	}

	// Sign out revokes the device and clears the cookie.
	r = f.do(t, "POST", "/api/auth/logout", cookie)
	if r.StatusCode != http.StatusNoContent || sessionCookie(t, r).MaxAge >= 0 {
		t.Fatalf("logout %d %v", r.StatusCode, r.Header.Values("Set-Cookie"))
	}
	if r := f.do(t, "GET", f.pp+"/agents", cookie); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API after sign out: %d", r.StatusCode)
	}
	if len(f.auth.Devices()) != 1 {
		t.Fatalf("devices after sign out: %+v", f.auth.Devices())
	}
}

func TestPairingLocksAfterWrongCodes(t *testing.T) {
	f := newFixture(t)
	code, _ := f.auth.NewCode("")
	var last int
	for i := 0; i < auth.MaxFailures; i++ {
		last = pair(t, f, noAuth, "0000-0000", "x").StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes: %d", auth.MaxFailures, last)
	}
	if r := pair(t, f, noAuth, code.Value, "x"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("good code during the lockout: %d", r.StatusCode)
	}
	if r := pair(t, f, noAuth, "", ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty name: %d", r.StatusCode)
	}
}

func TestRevokeClosesWebSockets(t *testing.T) {
	f := newFixture(t)
	a := f.start(t)
	code, _ := f.auth.NewCode("")
	c := sessionCookie(t, pair(t, f, noAuth, code.Value, "phone"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{CookieName + "=" + c.Value}}}
	events, _, err := websocket.Dial(ctx, f.ws+f.pp+"/events", opts)
	if err != nil {
		t.Fatal(err)
	}
	term, _, err := websocket.Dial(ctx, f.ws+f.pp+"/agents/"+a.ID+"/terminal", opts)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, term, "fake-agent-ready")
	readUntil(t, ctx, events, `"snapshot"`)
	// The fixture's other device signs the phone out.
	var phone string
	for _, d := range f.auth.Devices() {
		if d.Name == "phone" {
			phone = d.ID
		}
	}
	if r := f.do(t, "DELETE", "/api/devices/"+phone, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	for name, conn := range map[string]*websocket.Conn{"events": events, "terminal": term} {
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) != StatusSignedOut {
					t.Fatalf("%s closed with %v, want %d", name, err, StatusSignedOut)
				}
				break
			}
		}
	}
	if r := f.do(t, "DELETE", "/api/devices/"+phone, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("second revoke: %d", r.StatusCode)
	}
}

const tailnetHost = "box.tail1234.ts.net"

func remoteFixture(t *testing.T) *fixture {
	return newFixtureWith(t, func(o *Options) { o.RemoteHosts = []string{tailnetHost} })
}

func TestRemoteHostRules(t *testing.T) {
	f := remoteFixture(t)
	viaServe := map[string]string{"Host": tailnetHost, "Tailscale-User-Login": "ozan@github"}
	for name, c := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"tailnet user":            {viaServe, http.StatusOK},
		"tailnet user on 443":     {withHeaders(viaServe, "Host", tailnetHost+":443"), http.StatusOK},
		"upper case":              {withHeaders(viaServe, "Host", strings.ToUpper(tailnetHost)), http.StatusOK},
		"no identity (tagged)":    {map[string]string{"Host": tailnetHost}, http.StatusForbidden},
		"funnel":                  {withHeaders(viaServe, "Tailscale-Funnel-Request", "?1"), http.StatusForbidden},
		"other port":              {withHeaders(viaServe, "Host", tailnetHost+":8443"), http.StatusForbidden},
		"other tailnet name":      {withHeaders(viaServe, "Host", "other.tail1234.ts.net"), http.StatusForbidden},
		"loopback without port":   {withHeaders(viaServe, "Host", "127.0.0.1"), http.StatusForbidden},
		"remote, no credential":   {withHeaders(viaServe, "Authorization", ""), http.StatusUnauthorized},
		"loopback still works":    {map[string]string{"Host": "localhost:" + mustPort(f)}, http.StatusOK},
		"remote name, local port": {withHeaders(viaServe, "Host", tailnetHost+":"+mustPort(f)), http.StatusForbidden},
	} {
		if r := f.do(t, "GET", f.pp+"/agents", c.hdr); r.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", name, r.StatusCode, c.want)
		}
	}
	// Writes from a remote host need its https origin.
	for origin, want := range map[string]int{
		"https://" + tailnetHost:        http.StatusUnauthorized, // origin passes; the bad code is refused
		"http://" + tailnetHost:         http.StatusForbidden,
		"https://evil.example":          http.StatusForbidden,
		"https://" + tailnetHost + ":1": http.StatusForbidden,
	} {
		if r := pair(t, f, withHeaders(viaServe, "Origin", origin, "Authorization", ""), "0000-0000", "x"); r.StatusCode != want {
			t.Errorf("origin %s: %d, want %d", origin, r.StatusCode, want)
		}
	}
	// A device paired remotely gets a Secure cookie and records the tailnet login.
	code, _ := f.auth.NewCode("")
	r := pair(t, f, withHeaders(viaServe, "Origin", "https://"+tailnetHost, "Authorization", ""), code.Value, "phone")
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("remote pair: %d", r.StatusCode)
	}
	if c := sessionCookie(t, r); !c.Secure {
		t.Fatalf("remote cookie not Secure: %+v", c)
	}
	var sv sessionView
	_ = json.NewDecoder(r.Body).Decode(&sv)
	if !sv.Remote || sv.Device.PairedVia != "tailscale:ozan@github" {
		t.Fatalf("remote session %+v", sv)
	}
	var st statusView
	_ = json.NewDecoder(f.do(t, "GET", f.pp+"/status", nil).Body).Decode(&st)
	if len(st.RemoteHosts) != 1 || st.RemoteHosts[0] != tailnetHost {
		t.Fatalf("status remote hosts %v", st.RemoteHosts)
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/", f.pp + "/agents", "/api/health", "/missing-page"} {
		r := f.do(t, "GET", p, nil)
		h := r.Header
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"frame-ancestors 'none'", "script-src 'self';", "object-src 'none'", "base-uri 'none'",
			"connect-src 'self' ws://127.0.0.1:" + mustPort(f) + " wss://127.0.0.1:" + mustPort(f) + ";"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", p, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP allows eval", p)
		}
		if h.Get("X-Frame-Options") != "DENY" ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", p, h)
		}
		want := "no-cache"
		if strings.HasPrefix(p, "/api/") {
			want = "no-store"
		}
		if h.Get("Cache-Control") != want {
			t.Errorf("%s: Cache-Control %q, want %q", p, h.Get("Cache-Control"), want)
		}
	}
	if r := f.do(t, "GET", f.pp+"/agents", noAuth); r.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("a 401 lacks the security headers")
	}
}

// TestBehindTailscaleServe runs the API behind a TLS reverse proxy set up the
// way `tailscale serve` is (ipn/ipnlocal/serve.go, read 2026-11-08): the
// client's Host is kept, X-Forwarded-* are set, client-supplied Tailscale-*
// headers are removed and the tailnet user's login is added. A browser pairs,
// uses the API and both WebSockets through it.
func TestBehindTailscaleServe(t *testing.T) {
	f := remoteFixture(t)
	backend, _ := url.Parse(f.srv.URL)
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(backend)
		pr.Out.Host = pr.In.Host
		pr.SetXForwarded()
		for k := range pr.Out.Header {
			if strings.HasPrefix(strings.ToLower(k), "tailscale-") {
				pr.Out.Header.Del(k)
			}
		}
		pr.Out.Header.Set("Tailscale-User-Login", "ozan@github")
	}})
	defer proxy.Close()
	client := proxy.Client()
	do := func(method, p, body string, hdr http.Header) *http.Response {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, proxy.URL+p, rd)
		req.Host = tailnetHost // what the phone's browser sends
		req.Header.Set("Origin", "https://"+tailnetHost)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	// A client-supplied identity is replaced by the proxy, like tailscale serve does.
	if r := do("GET", f.pp+"/agents", "", http.Header{"Tailscale-User-Login": {"someone@else"}}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired through the proxy: %d", r.StatusCode)
	}
	code, _ := f.auth.NewCode("")
	r := do("POST", "/api/auth/pair", `{"code":"`+code.Value+`","name":"phone"}`, http.Header{"Content-Type": {"application/json"}})
	if r.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("pair through the proxy: %d %s", r.StatusCode, b)
	}
	c := sessionCookie(t, r)
	if !c.Secure {
		t.Fatal("cookie through tailscale serve is not Secure")
	}
	var sv sessionView
	_ = json.NewDecoder(r.Body).Decode(&sv)
	if sv.Device.PairedVia != "tailscale:ozan@github" {
		t.Fatalf("paired via %q (the proxy's login, not the client's)", sv.Device.PairedVia)
	}
	cookie := http.Header{"Cookie": {CookieName + "=" + c.Value}}
	if r := do("GET", f.pp+"/agents", "", cookie); r.StatusCode != http.StatusOK {
		t.Fatalf("API through the proxy: %d", r.StatusCode)
	}
	a := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "wss" + strings.TrimPrefix(proxy.URL, "https")
	opts := &websocket.DialOptions{HTTPClient: client, Host: tailnetHost,
		HTTPHeader: http.Header{"Cookie": cookie["Cookie"], "Origin": {"https://" + tailnetHost}}}
	ev, _, err := websocket.Dial(ctx, wsURL+f.pp+"/events", opts)
	if err != nil {
		t.Fatalf("events through the proxy: %v", err)
	}
	if _, data, err := ev.Read(ctx); err != nil || !strings.Contains(string(data), `"snapshot"`) {
		t.Fatalf("snapshot: %v %.80s", err, data)
	}
	term, _, err := websocket.Dial(ctx, wsURL+f.pp+"/agents/"+a.ID+"/terminal", opts)
	if err != nil {
		t.Fatalf("terminal through the proxy: %v", err)
	}
	readUntil(t, ctx, term, "fake-agent-ready")
	_ = term.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"from-the-phone\r"}`))
	readUntil(t, ctx, term, "from-the-phone")
	// A cross-site page cannot open the WebSocket, even with the cookie.
	bad := *opts
	bad.HTTPHeader = http.Header{"Cookie": cookie["Cookie"], "Origin": {"https://evil.example"}}
	if _, resp, err := websocket.Dial(ctx, wsURL+f.pp+"/events", &bad); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket through the proxy: %v %v", err, resp)
	}
}

func TestRemoteUsers(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) {
		o.RemoteHosts = []string{tailnetHost}
		o.RemoteUsers = []string{"ozan@github"}
	})
	for login, want := range map[string]int{"ozan@github": http.StatusOK, "OZAN@github": http.StatusOK, "guest@github": http.StatusForbidden} {
		if r := f.do(t, "GET", f.pp+"/agents", map[string]string{"Host": tailnetHost, "Tailscale-User-Login": login}); r.StatusCode != want {
			t.Errorf("%s: %d, want %d", login, r.StatusCode, want)
		}
	}
	// Loopback is not a tailnet request; the device token decides.
	if r := f.do(t, "GET", f.pp+"/agents", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("loopback: %d", r.StatusCode)
	}
}

func TestSignOutOtherDevices(t *testing.T) {
	f := newFixture(t)
	code, _ := f.auth.NewCode("")
	phone := sessionCookie(t, pair(t, f, noAuth, code.Value, "phone"))
	r := f.do(t, "POST", "/api/devices/sign-out-others", nil)
	var out map[string]int
	_ = json.NewDecoder(r.Body).Decode(&out)
	if r.StatusCode != http.StatusOK || out["signed_out"] != 1 {
		t.Fatalf("sign out others: %d %v", r.StatusCode, out)
	}
	if r := f.do(t, "GET", f.pp+"/agents", withHeaders(noAuth, "Cookie", CookieName+"="+phone.Value)); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("phone after sign-out-others: %d", r.StatusCode)
	}
	if r := f.do(t, "GET", f.pp+"/agents", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("the caller was signed out too: %d", r.StatusCode)
	}
}
