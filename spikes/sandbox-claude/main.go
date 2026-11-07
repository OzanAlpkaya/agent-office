// Spike F: Claude Code inside a rootless Podman worker. Throwaway.
//
//	host   runs on the host: agent socket (hooks + one-shot credential), egress
//	       socket (CONNECT :443 allowlist) and, with -inject, an API socket that
//	       adds the credential to Anthropic API requests.
//	init   runs inside the container: loopback bridges to those sockets, fetches
//	       the credential over the agent socket, then runs the given command.
//	probe  runs inside the container: isolation checks without Claude Code.
//
// Nothing here ever prints or logs a credential; logs carry names, lengths and
// booleans only.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const placeholder = "ao-placeholder-not-a-credential"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spike host|init|probe ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "host":
		host(os.Args[2:])
	case "init":
		os.Exit(initCmd(os.Args[2:]))
	case "probe":
		probe()
	case "pty":
		os.Exit(ptyCmd(os.Args[2:]))
	case "screen":
		screen(os.Args[2:])
	default:
		os.Exit(2)
	}
}

// ---------- host ----------

func host(args []string) {
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	run := fs.String("run", "", "directory for the sockets")
	allow := fs.String("allow", "", "comma-separated allowed hosts (exact, or .suffix)")
	tokenFile := fs.String("token-file", "", "credential file; empty = no credential")
	inject := fs.Bool("inject", false, "serve api.sock that injects the credential")
	_ = fs.Parse(args)
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	allowed := map[string]bool{}
	var suffixes []string
	for _, h := range strings.Split(*allow, ",") {
		h = strings.TrimSpace(strings.ToLower(h))
		switch {
		case h == "":
		case strings.HasPrefix(h, "."):
			suffixes = append(suffixes, h)
		default:
			allowed[h] = true
		}
	}
	isAllowed := func(h string) bool {
		h = strings.ToLower(h)
		if allowed[h] {
			return true
		}
		for _, s := range suffixes {
			if strings.HasSuffix(h, s) {
				return true
			}
		}
		return false
	}

	var delivered sync.Once
	agent := listen(filepath.Join(*run, "agent.sock"))
	go serve(agent, func(c net.Conn) {
		b, _ := io.ReadAll(io.LimitReader(c, 1<<20))
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["agent_office_request"] == "credential" {
			if *tokenFile == "" {
				log.Printf("credential: requested, none configured")
				return
			}
			sent := false
			delivered.Do(func() {
				tok, err := readToken(*tokenFile)
				if err != nil {
					log.Printf("credential: %v", err)
					return
				}
				_, _ = c.Write([]byte(tok))
				sent = true
				log.Printf("credential: delivered (%d bytes)", len(tok))
			})
			if !sent {
				log.Printf("credential: refused (already delivered or unreadable)")
			}
			return
		}
		ev, _ := m["hook_event_name"].(string)
		tool, _ := m["tool_name"].(string)
		log.Printf("hook: %s tool=%s bytes=%d", ev, tool, len(b))
	})

	egress := listen(filepath.Join(*run, "egress.sock"))
	go serve(egress, func(c net.Conn) {
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if req.Method != http.MethodConnect {
			log.Printf("egress: DENY method=%s host=%s", req.Method, req.Host)
			fmt.Fprint(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
			return
		}
		h, port, err := net.SplitHostPort(req.Host)
		if err != nil || port != "443" || !isAllowed(h) {
			log.Printf("egress: DENY %s", req.Host)
			fmt.Fprint(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
			return
		}
		up, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
		if err != nil {
			log.Printf("egress: FAIL %s: %v", req.Host, err)
			fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		defer up.Close()
		log.Printf("egress: ALLOW %s", req.Host)
		fmt.Fprint(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		if n := br.Buffered(); n > 0 {
			b, _ := br.Peek(n)
			_, _ = up.Write(b)
		}
		splice(c, up)
	})

	if *inject {
		tok := ""
		if *tokenFile != "" {
			var err error
			if tok, err = readToken(*tokenFile); err != nil {
				log.Fatalf("inject: %v", err)
			}
		}
		target, _ := url.Parse("https://api.anthropic.com")
		rp := &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				var names []string
				for k := range r.In.Header {
					names = append(names, k)
				}
				sort.Strings(names)
				auth := r.In.Header.Get("Authorization")
				log.Printf("api: %s %s headers=%v auth_placeholder=%v auth_other=%v x_api_key=%v",
					r.In.Method, r.In.URL.Path, names, auth == "Bearer "+placeholder,
					auth != "" && auth != "Bearer "+placeholder, r.In.Header.Get("X-Api-Key") != "")
				r.SetURL(target)
				r.Out.Host = target.Host
				if auth == "Bearer "+placeholder && tok != "" {
					r.Out.Header.Set("Authorization", "Bearer "+tok)
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				log.Printf("api: -> %d %s", resp.StatusCode, resp.Request.URL.Path)
				return nil
			},
		}
		api := listen(filepath.Join(*run, "api.sock"))
		go func() { _ = http.Serve(api, rp) }()
	}

	log.Printf("host: ready (allow=%q credential=%v inject=%v)", *allow, *tokenFile != "", *inject)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

// readToken mirrors the daemon's checks in short: regular file, no symlink,
// owner-only mode. The value is returned, never printed.
func readToken(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file must be a regular file with mode 0600 (is %v)", fi.Mode())
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" || strings.ContainsAny(t, " \t\r\n") {
		return "", errors.New("token file must contain exactly one token")
	}
	return t, nil
}

func listen(path string) net.Listener {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log.Fatal(err)
	}
	return l
}

func serve(l net.Listener, h func(net.Conn)) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() { defer c.Close(); h(c) }()
	}
}

func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// ---------- init (inside the container) ----------

func initCmd(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "init: no command")
		return 2
	}
	bridge := func(addr, sock string) {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: bridge:", err)
			os.Exit(1)
		}
		go serve(l, func(c net.Conn) {
			u, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer u.Close()
			splice(c, u)
		})
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AO_") {
			env = append(env, kv)
		}
	}
	if h := os.Getenv("HOME"); h != "" {
		_ = os.MkdirAll(h, 0o700)
	}
	bridge("127.0.0.1:3128", os.Getenv("AO_EGRESS_SOCK"))
	switch os.Getenv("AO_CREDENTIAL") {
	case "socket":
		tok, err := fetchCredential(os.Getenv("AO_AGENT_SOCK"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: credential:", err)
			return 1
		}
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
	case "inject":
		bridge("127.0.0.1:8080", os.Getenv("AO_API_SOCK"))
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+placeholder, "ANTHROPIC_BASE_URL=http://127.0.0.1:8080")
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(syscall.SIGINT) // the tty delivers it to the child directly
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

func fetchCredential(sock string) (string, error) {
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(`{"agent_office_request":"credential"}`)); err != nil {
		return "", err
	}
	_ = c.(*net.UnixConn).CloseWrite()
	b, err := io.ReadAll(io.LimitReader(c, 4096))
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("daemon returned no credential")
	}
	return string(b), nil
}

// ---------- probe (inside the container) ----------

func probe() {
	res := func(ok bool, what string, detail ...any) {
		tag := "PASS"
		if !ok {
			tag = "FAIL"
		}
		fmt.Printf("%s  %s %s\n", tag, what, fmt.Sprint(detail...))
	}
	home := os.Getenv("HOME")
	for _, p := range []string{"/home/ozan", "/home/ozan/.ssh", filepath.Join(home, ".ssh"), "/root/.ssh",
		filepath.Join(home, ".gitconfig"), filepath.Join(home, ".claude"), "/home/ozan/.config/agent-office"} {
		_, err := os.Stat(p)
		res(errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission), "absent: "+p)
	}
	for _, k := range []string{"SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "GIT_ASKPASS", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		_, set := os.LookupEnv(k)
		res(!set, "env unset: "+k)
	}
	err := os.WriteFile("/etc/ao-probe", []byte("x"), 0o600)
	res(err != nil, "write /etc refused: ", err)
	err = os.WriteFile("/workspace/.ao-probe", []byte("x"), 0o600)
	res(err == nil, "write /workspace ok: ", err)
	_ = os.Remove("/workspace/.ao-probe")

	_, err = net.LookupHost("github.com")
	res(err != nil, "DNS fails: ", err)
	for _, a := range []string{"140.82.121.4:22", "1.1.1.1:443", "[2606:4700:4700::1111]:443"} {
		c, err := net.DialTimeout("tcp", a, 3*time.Second)
		if c != nil {
			c.Close()
		}
		res(err != nil, "direct dial fails "+a+": ", err)
	}

	connect := func(target string) string {
		c, err := net.DialTimeout("tcp", "127.0.0.1:3128", 3*time.Second)
		if err != nil {
			return "dial proxy: " + err.Error()
		}
		defer c.Close()
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		line, _ := bufio.NewReader(c).ReadString('\n')
		return strings.TrimSpace(line)
	}
	for _, t := range []string{"github.com:22", "github.com:443", "example.com:443", "api.anthropic.com:80"} {
		s := connect(t)
		res(strings.Contains(s, " 403 "), "proxy denies "+t+": ", s)
	}
	cl := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	for _, u := range []string{"https://proxy.golang.org/github.com/google/uuid/@v/list",
		"https://sum.golang.org/latest", "https://registry.npmjs.org/left-pad"} {
		resp, err := cl.Get(u)
		if err != nil {
			res(false, "registry reachable "+u+": ", err)
			continue
		}
		resp.Body.Close()
		res(resp.StatusCode == 200, "registry reachable "+u+": ", resp.StatusCode)
	}
	out, err := exec.Command("git", "ls-remote", "https://github.com/OzanAlpkaya/agent-office.git").CombinedOutput()
	res(err != nil, "git over https to github fails: ", strings.TrimSpace(lastLine(string(out))))
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------- pty / screen (host, interactive runs) ----------

// ptyCmd runs a command in a 120x36 PTY. Output is appended to -out (raw);
// whatever is written to the FIFO -in is typed into the PTY.
func ptyCmd(args []string) int {
	fs := flag.NewFlagSet("pty", flag.ExitOnError)
	in := fs.String("in", "", "FIFO for keystrokes")
	out := fs.String("out", "", "raw output log")
	_ = fs.Parse(args)
	argv := fs.Args()
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	logf, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer logf.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 36})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	go func() { _, _ = io.Copy(logf, f) }()
	go func() {
		for {
			fifo, err := os.Open(*in) // blocks until a writer opens it
			if err != nil {
				return
			}
			_, _ = io.Copy(f, fifo)
			fifo.Close()
		}
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

// screen prints the last lines of a raw PTY log with escape sequences removed.
// Cursor movement is lost, so this is approximate, but dialogs are readable.
func screen(args []string) {
	b, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(b) > 12000 {
		b = b[len(b)-12000:]
	}
	text := ansi.ReplaceAllString(string(b), "\n")
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	fmt.Println(strings.Join(lines, "\n"))
}
