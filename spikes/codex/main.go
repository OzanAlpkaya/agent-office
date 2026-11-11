// Spike G (throwaway): Codex CLI behind a credential-injecting proxy.
//
//	spike proxy -listen 127.0.0.1:18080|unix:/path -auth ~/.codex/auth.json -log out/proxy.log
//	spike placeholder -out <CODEX_HOME>/auth.json
//	spike hooklog -out out/hooks.jsonl   (a hook command: appends a reduced payload)
//
// The proxy reads the real access token and account id from the auth file at
// every request and puts them on requests to chatgpt.com; it never logs a
// token. Logs carry methods, paths, header names, model names and usage
// numbers only.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: spike proxy|placeholder|hooklog")
	}
	switch os.Args[1] {
	case "proxy":
		proxy(os.Args[2:])
	case "placeholder":
		placeholder(os.Args[2:])
	case "hooklog":
		hooklog(os.Args[2:])
	case "tls":
		makeTLS(os.Args[2:])
	default:
		log.Fatal("unknown command")
	}
}

const Placeholder = "ao-placeholder"

type authFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func readAuth(p string) (token, account string, err error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", "", err
	}
	var a authFile
	if err := json.Unmarshal(b, &a); err != nil {
		return "", "", fmt.Errorf("auth file: not JSON")
	}
	return a.Tokens.AccessToken, a.Tokens.AccountID, nil
}

func proxy(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:18080", "")
	authPath := fs.String("auth", "", "real auth.json (read per request, never logged)")
	logPath := fs.String("log", "out/proxy.log", "")
	certFile := fs.String("tls-cert", "", "serve TLS with this certificate chain (PEM)")
	privFile := fs.String("tls-priv", "", "its private part (PEM)")
	_ = fs.Parse(args)
	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	lg := log.New(lf, "", log.Lmicroseconds)
	upstream := &http.Client{Timeout: 10 * time.Minute}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names := []string{}
		for k := range r.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		auth := r.Header.Get("Authorization")
		acct := r.Header.Get("Chatgpt-Account-Id")
		body, _ := io.ReadAll(r.Body)
		model := ""
		var m struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &m) == nil {
			model = m.Model
		}
		lg.Printf("req %s %s headers=%v auth_placeholder=%v acct_placeholder=%v model=%q bytes=%d",
			r.Method, r.URL.Path, names, strings.Contains(auth, Placeholder) || strings.HasSuffix(auth, ".x"),
			acct == "00000000-0000-4000-8000-000000000000", model, len(body))
		if !strings.HasPrefix(r.URL.Path, "/backend-api/") {
			lg.Printf("refused %s (not the ChatGPT backend)", r.URL.Path)
			http.Error(w, "spike: refused", http.StatusForbidden)
			return
		}
		tok, account, err := readAuth(*authPath)
		if err != nil || tok == "" {
			lg.Printf("no real credential: %v", err)
			http.Error(w, "spike: no credential", http.StatusBadGateway)
			return
		}
		u := "https://chatgpt.com" + r.URL.RequestURI()
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, u, bytes.NewReader(body))
		for k, vs := range r.Header {
			switch strings.ToLower(k) {
			case "authorization", "chatgpt-account-id", "host", "content-length", "accept-encoding":
				continue
			}
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if account != "" {
			req.Header.Set("ChatGPT-Account-Id", account)
		}
		resp, err := upstream.Do(req)
		if err != nil {
			lg.Printf("upstream error: %v", err)
			http.Error(w, "spike: upstream", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		lg.Printf("resp %s %d %s", r.URL.Path, resp.StatusCode, resp.Header.Get("Content-Type"))
		if strings.Contains(r.URL.Path, "accounts/check") {
			b, _ := io.ReadAll(resp.Body)
			var v any
			if json.Unmarshal(b, &v) == nil {
				sh, _ := json.Marshal(shapeOf(v))
				lg.Printf("shape %s %s", r.URL.Path, sh)
			}
			for k, vs := range resp.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Del("Content-Length")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(b)
			return
		}
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		fl, _ := w.(http.Flusher)
		rd := bufio.NewReader(resp.Body)
		for {
			line, err := rd.ReadBytes('\n')
			if len(line) > 0 {
				_, _ = w.Write(line)
				if fl != nil {
					fl.Flush()
				}
				meter(lg, line)
			}
			if err != nil {
				return
			}
		}
	})
	var l net.Listener
	if p, ok := strings.CutPrefix(*listen, "unix:"); ok {
		_ = os.Remove(p)
		l, err = net.Listen("unix", p)
	} else {
		l, err = net.Listen("tcp", *listen)
	}
	if err != nil {
		log.Fatal(err)
	}
	lg.Printf("listening on %s tls=%v", *listen, *certFile != "")
	if *certFile != "" {
		log.Fatal(http.ServeTLS(l, h, *certFile, *privFile))
	}
	log.Fatal(http.Serve(l, h))
}

// meter logs the usage numbers of a Responses stream's final event.
func meter(lg *log.Logger, line []byte) {
	s, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data: "))
	if !ok {
		return
	}
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(s, &ev) != nil || ev.Type == "" {
		return
	}
	if ev.Type == "response.completed" || ev.Type == "response.failed" || ev.Type == "response.incomplete" {
		lg.Printf("usage %s id_len=%d model=%q usage=%s", ev.Type, len(ev.Response.ID), ev.Response.Model, ev.Response.Usage)
	}
}

func jwt(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + enc(claims) + ".x"
}

// placeholder writes an auth.json in Codex's ChatGPT shape with tokens that
// carry no credential: unsigned JWTs with far-future expiry and a fake account.
func placeholder(args []string) {
	fs := flag.NewFlagSet("placeholder", flag.ExitOnError)
	out := fs.String("out", "", "")
	plan := fs.String("plan", "plus", "")
	from := fs.String("account-from", "", "real auth.json to take the account id (not a credential) from")
	_ = fs.Parse(args)
	now := time.Now().Unix()
	exp := now + 10*365*24*3600
	acct := "00000000-0000-4000-8000-000000000000"
	if *from != "" {
		if _, a, err := readAuth(*from); err == nil && a != "" {
			acct = a
		}
	}
	auth := map[string]any{"chatgpt_account_id": acct, "chatgpt_plan_type": *plan, "chatgpt_user_id": "user-placeholder",
		"user_id": "user-placeholder"}
	doc := map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": jwt(map[string]any{"iss": "https://auth.openai.com", "sub": "placeholder", "aud": []string{"app_placeholder"},
				"email": "agent@agent-office.invalid", "email_verified": true, "iat": now, "exp": exp,
				"https://api.openai.com/auth": auth}),
			"access_token": jwt(map[string]any{"iss": "https://auth.openai.com", "sub": "placeholder", "iat": now, "exp": exp,
				"https://api.openai.com/auth": auth, "scp": []string{"openid"}}),
			"refresh_token": Placeholder,
			"account_id":    acct,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		log.Fatal(err)
	}
}

// hooklog appends a reduced hook payload: field names and short values only
// (prompts and messages replaced by their length).
func hooklog(args []string) {
	fs := flag.NewFlagSet("hooklog", flag.ExitOnError)
	out := fs.String("out", "out/hooks.jsonl", "")
	event := fs.String("event", "", "the event this hook was registered for")
	_ = fs.Parse(args)
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	var m map[string]any
	red := map[string]any{"_raw_bytes": len(b), "_at": time.Now().Format(time.RFC3339Nano), "_registered": *event}
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			switch x := v.(type) {
			case string:
				if len(x) > 80 || k == "prompt" || k == "last_assistant_message" {
					red[k] = fmt.Sprintf("<str len=%d>", len(x))
				} else {
					red[k] = x
				}
			case map[string]any:
				keys := []string{}
				for kk := range x {
					keys = append(keys, kk)
				}
				sort.Strings(keys)
				red[k] = map[string]any{"_keys": keys}
				if k == "token_usage" || k == "usage" {
					red[k] = x
				}
			default:
				red[k] = v
			}
		}
	}
	line, _ := json.Marshal(red)
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		f.Close()
	}
}

// shapeOf keeps a JSON value's structure: keys, types, and only URL-like
// strings and booleans (no ids, names or tokens).
func shapeOf(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, vv := range x {
			switch k {
			case "workspace_backend_origin", "account_routing_override", "structure", "plan_type", "account_residency_region":
				out[k] = vv // routing values, not identities or credentials
			default:
				out[k] = shapeOf(vv)
			}
		}
		return out
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{shapeOf(x[0]), fmt.Sprintf("<%d items>", len(x))}
	case string:
		if strings.HasPrefix(x, "http") {
			return x
		}
		return fmt.Sprintf("<str len=%d>", len(x))
	case bool, nil:
		return x
	default:
		return "<num>"
	}
}
