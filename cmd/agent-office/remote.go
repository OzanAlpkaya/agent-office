package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/api"
)

// parseRemoteHosts checks --remote-host: tailnet names that `tailscale
// serve` forwards to this daemon (ADR 0038). Only *.ts.net names are
// accepted, so a public reverse proxy cannot be configured by mistake.
func parseRemoteHosts(s string) ([]string, error) {
	var out []string
	for _, h := range strings.Split(s, ",") {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" {
			continue
		}
		if !validTailnetName(h) {
			return nil, fmt.Errorf("--remote-host %q: want this machine's Tailscale name, e.g. box.tail1234.ts.net "+
				"(see `tailscale status --json`, Self.DNSName); no scheme, port or path", h)
		}
		out = append(out, h)
	}
	return out, nil
}

// parseRemoteUsers checks --remote-users: Tailscale logins (user@provider)
// allowed to reach the daemon through tailscale serve; empty allows every
// user of the tailnet (pairing still applies).
func parseRemoteUsers(s string) ([]string, error) {
	var out []string
	for _, u := range strings.Split(s, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if len(u) > 200 || !strings.Contains(u, "@") || strings.IndexFunc(u, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
			return nil, fmt.Errorf("--remote-users %q: want Tailscale logins such as you@github, comma-separated "+
				"(your login as the Tailscale admin console shows it)", u)
		}
		out = append(out, u)
	}
	return out, nil
}

func validTailnetName(h string) bool {
	if len(h) > 253 || !strings.HasSuffix(h, ".ts.net") {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 4 { // machine.tailnet.ts.net
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// controlSocketPath is the daemon's control socket for a data dir: one per
// data dir, so a development daemon and the real one do not collide, and
// short enough for the unix socket path limit.
func controlSocketPath(runtimeDir, dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return filepath.Join(runtimeDir, "control-"+hex.EncodeToString(sum[:6])+".sock")
}

// listenControl listens on the control socket (mode 0600). A socket another
// daemon still answers on is an error; a stale one is replaced.
func listenControl(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another agent-office daemon uses this data dir (control socket %s)", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("control socket: %w", err)
	}
	return l, nil
}

// controlClient talks HTTP to the daemon's control socket.
func controlClient(path string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}},
	}
}

// pairCommand asks the running daemon for a pairing code (or, with --token,
// a token for scripts) and prints it in the user's own terminal (ADR 0039).
// Nothing it prints goes to the daemon's log.
func pairCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", envOr("AGENT_OFFICE_DATA_DIR", defaultDataDir()),
		"data dir of the daemon to pair with [AGENT_OFFICE_DATA_DIR]")
	tokenName := fs.String("token", "", "instead of a code, create a device token for scripts with this name and print it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "agent-office pair:", err)
		return 1
	}
	rt, err := runtimeDir()
	if err != nil {
		fmt.Fprintln(stderr, "agent-office pair:", err)
		return 1
	}
	sock := controlSocketPath(rt, dir)
	c := controlClient(sock)
	if *tokenName != "" {
		var out api.ControlToken
		if err := controlPost(c, "/tokens", map[string]string{"name": *tokenName}, &out); err != nil {
			fmt.Fprintln(stderr, "agent-office pair:", explainControl(err, dir))
			return 1
		}
		fmt.Fprintf(stderr, "Token for %q (device %s). Send it as \"Authorization: Bearer <token>\"; sign it out in Devices.\n",
			out.Device.Name, out.Device.ID)
		fmt.Fprintln(stdout, out.Token)
		return 0
	}
	var out api.ControlCode
	if err := controlPost(c, "/pairing-codes", nil, &out); err != nil {
		fmt.Fprintln(stderr, "agent-office pair:", explainControl(err, dir))
		return 1
	}
	fmt.Fprintf(stdout, "Pairing code: %s (works once, until %s)\n\n", out.Value, out.ExpiresAt.Local().Format("15:04:05"))
	fmt.Fprintf(stdout, "This machine: open %s#pair=%s\n", out.LocalURL, out.Value)
	if len(out.RemoteURLs) > 0 {
		fmt.Fprintf(stdout, "Phone or other machine on your tailnet: open %s and enter the code\n", strings.Join(out.RemoteURLs, " or "))
	} else {
		fmt.Fprintln(stdout, "Phone or other machine: set up `tailscale serve` and --remote-host first (docs/remote.md)")
	}
	return 0
}

func controlPost(c *http.Client, path string, body, out any) error {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, "http://agent-office"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return errControlDown{err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("daemon answered %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

type errControlDown struct{ err error }

func (e errControlDown) Error() string { return e.err.Error() }

func explainControl(err error, dataDir string) string {
	var down errControlDown
	if errors.As(err, &down) {
		return fmt.Sprintf("no agent-office daemon is running with data dir %s (start `agent-office serve`, "+
			"or pass the daemon's --data-dir)", dataDir)
	}
	return err.Error()
}
