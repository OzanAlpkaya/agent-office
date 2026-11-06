// Spike B: capture real Claude Code hook payloads.
//
// Throwaway code. It starts a hook receiver on 127.0.0.1:7682, runs a Claude
// Code session in this folder (whose .claude/settings.json registers an HTTP
// hook and a command hook for each event), and records every payload to
// out/<run>/events.jsonl. out/ is gitignored: payloads contain prompts and paths.
//
// Usage (from this folder):
//
//	go run . -scenario print        # claude -p with one allowed Bash call
//	go run . -scenario inject       # Stop hook injects a message once
//	go run . -scenario interactive  # interactive TUI in a PTY: permission prompt, idle, exit
//	go run . -summarize out/<run>   # print event shapes (names, enums, key types; no values)
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const addr = "127.0.0.1:7682"

type record struct {
	Seq         int             `json:"seq"`
	Ms          int64           `json:"ms"` // since run start
	Via         string          `json:"via"`
	Method      string          `json:"method"`
	ContentType string          `json:"content_type"`
	HeaderNames []string        `json:"header_names"`
	Payload     json.RawMessage `json:"payload"`
}

type event struct {
	via   string
	name  string
	field map[string]any
}

type receiver struct {
	start  time.Time
	out    *os.File
	events chan event

	mu        sync.Mutex
	seq       int
	inject    string // non-empty: block the first Stop (on injectVia) with this reason
	injectVia string
	injected  bool
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("non-JSON hook body", "len", len(body))
		p = map[string]any{}
	}
	via := r.URL.Query().Get("via")
	names := make([]string, 0, len(r.Header))
	for k := range r.Header {
		names = append(names, k)
	}
	sort.Strings(names)

	rc.mu.Lock()
	rc.seq++
	rec := record{Seq: rc.seq, Ms: time.Since(rc.start).Milliseconds(), Via: via, Method: r.Method,
		ContentType: r.Header.Get("Content-Type"), HeaderNames: names, Payload: body}
	line, _ := json.Marshal(rec)
	_, _ = rc.out.Write(append(line, '\n'))
	name, _ := p["hook_event_name"].(string)
	resp := []byte("{}")
	if rc.inject != "" && !rc.injected && via == rc.injectVia && name == "Stop" && p["stop_hook_active"] != true {
		rc.injected = true
		resp, _ = json.Marshal(map[string]string{"decision": "block", "reason": rc.inject})
		slog.Info("injecting message via Stop hook")
	}
	rc.mu.Unlock()

	slog.Info("hook", "seq", rec.Seq, "ms", rec.Ms, "via", via, "event", name, "kind", kind(p))
	select {
	case rc.events <- event{via: via, name: name, field: p}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// kind returns the enum-like discriminator of an event, never free text.
func kind(p map[string]any) string {
	for _, k := range []string{"source", "reason", "notification_type", "tool_name", "trigger"} {
		if v, ok := p[k].(string); ok {
			return k + "=" + v
		}
	}
	return ""
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// childEnv is the parent env without the variables Claude Code sets for its own
// children. The daemon will not have them either, and some are credentials.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || k == "CLAUDE_PID" || k == "CLAUDE_EFFORT" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func main() {
	scenario := flag.String("scenario", "", "print | inject | interactive")
	summarize := flag.String("summarize", "", "run dir to summarize")
	injectVia := flag.String("inject-via", "http", "inject scenario: which hook transport answers the Stop (http | command)")
	flag.Parse()
	if *summarize != "" {
		if err := summary(*summarize); err != nil {
			slog.Error("summarize", "err", err)
			os.Exit(1)
		}
		return
	}

	run := filepath.Join("out", time.Now().Format("20060102-150405")+"-"+*scenario)
	if err := os.MkdirAll(run, 0o700); err != nil {
		panic(err)
	}
	f, err := os.OpenFile(filepath.Join(run, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	rc := &receiver{start: time.Now(), out: f, events: make(chan event, 512)}
	srv := &http.Server{Addr: addr, Handler: rc}
	go func() { _ = srv.ListenAndServe() }()
	defer srv.Close()
	time.Sleep(200 * time.Millisecond)

	sid := uuid()
	base := []string{"--model", "haiku", "--session-id", sid}
	var runErr error
	switch *scenario {
	case "print":
		runErr = runPrint(run, append(base, "--allowedTools", "Bash(echo spike-ok)"),
			"Run exactly this shell command: echo spike-ok. Then reply with only the word done.")
	case "inject":
		rc.injectVia = *injectVia
		rc.inject = "New message from the lead agent: reply with only the word PINEAPPLE."
		runErr = runPrint(run, base, "Reply with only the word ready.")
	case "interactive":
		runErr = runInteractive(run, base, rc.events)
	default:
		runErr = fmt.Errorf("unknown scenario %q", *scenario)
	}
	time.Sleep(2 * time.Second) // let late hooks (SessionEnd) arrive
	if runErr != nil {
		slog.Error("scenario failed", "err", runErr)
	}
	slog.Info("done", "run", run, "session_id", sid)
}

func runPrint(run string, args []string, prompt string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args = append([]string{"-p", "--output-format", "stream-json", "--verbose", "--include-hook-events"}, args...)
	// The prompt goes on stdin: --allowedTools is variadic and would swallow a
	// trailing positional prompt.
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = childEnv()
	cmd.Stdin = strings.NewReader(prompt)
	stdout, _ := os.Create(filepath.Join(run, "stream.jsonl"))
	stderr, _ := os.Create(filepath.Join(run, "stderr.log"))
	defer stdout.Close()
	defer stderr.Close()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// ptyLog collects raw PTY output and lets the driver wait for text in it.
type ptyLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *ptyLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *ptyLog) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Contains(bytes.ToLower(l.buf.Bytes()), []byte(strings.ToLower(s)))
}

func waitFor(events <-chan event, d time.Duration, match func(event) bool) (event, bool) {
	deadline := time.After(d)
	for {
		select {
		case e := <-events:
			if match(e) {
				return e, true
			}
		case <-deadline:
			return event{}, false
		}
	}
}

func isEvent(name string, kv ...string) func(event) bool {
	return func(e event) bool {
		if e.name != name { // either transport: SessionStart only arrives via the command hook
			return false
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if v, _ := e.field[kv[i]].(string); v != kv[i+1] {
				return false
			}
		}
		return true
	}
}

func runInteractive(run string, args []string, events <-chan event) error {
	cmd := exec.Command("claude", args...)
	cmd.Env = append(childEnv(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return err
	}
	defer ptmx.Close()
	logFile, _ := os.OpenFile(filepath.Join(run, "pty.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	defer logFile.Close()
	pl := &ptyLog{}
	go func() { _, _ = io.Copy(io.MultiWriter(logFile, pl), ptmx) }()
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	step := func(msg string) { slog.Info("driver: " + msg) }
	typeLine := func(s string) {
		_, _ = io.WriteString(ptmx, s)
		time.Sleep(700 * time.Millisecond)
		_, _ = io.WriteString(ptmx, "\r")
	}

	step("waiting for SessionStart")
	if _, ok := waitFor(events, 25*time.Second, isEvent("SessionStart")); !ok {
		if pl.contains("trust") {
			return fmt.Errorf("no SessionStart: a workspace trust dialog seems to be shown; not accepting it automatically")
		}
		return fmt.Errorf("no SessionStart within 25s")
	}
	time.Sleep(4 * time.Second) // let the TUI finish rendering

	step("prompt that needs a permission (Write)")
	typeLine("Create a file named note.txt containing the single word hi. Do not do anything else.")
	if _, ok := waitFor(events, 60*time.Second, isEvent("PermissionRequest")); !ok {
		return fmt.Errorf("no PermissionRequest")
	}
	step("PermissionRequest seen; waiting for Notification(permission_prompt)")
	if _, ok := waitFor(events, 15*time.Second, isEvent("Notification", "notification_type", "permission_prompt")); !ok {
		step("no permission_prompt notification within 15s")
	}
	step("approving with Enter (default option)")
	_, _ = io.WriteString(ptmx, "\r")
	if _, ok := waitFor(events, 60*time.Second, isEvent("Stop")); !ok {
		return fmt.Errorf("no Stop after approval")
	}
	step("Stop seen; waiting up to 90s for Notification(idle_prompt)")
	if _, ok := waitFor(events, 90*time.Second, isEvent("Notification", "notification_type", "idle_prompt")); !ok {
		step("no idle_prompt notification within 90s")
	}
	step("exiting with /exit")
	typeLine("/exit")
	if _, ok := waitFor(events, 15*time.Second, isEvent("SessionEnd")); !ok {
		step("no SessionEnd after /exit; sending Ctrl-C twice")
		_, _ = io.WriteString(ptmx, "\x03")
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(ptmx, "\x03")
		if _, ok := waitFor(events, 10*time.Second, isEvent("SessionEnd")); !ok {
			return fmt.Errorf("no SessionEnd")
		}
	}
	return nil
}

// summary prints, per received hook, the event name, enum discriminators and the
// payload's key names with JSON types. It never prints free-text values.
func summary(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return err
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		var p map[string]any
		_ = json.Unmarshal(rec.Payload, &p)
		name, _ := p["hook_event_name"].(string)
		fmt.Printf("#%-3d %6dms %-7s %-4s %-18s %-32s %s\n", rec.Seq, rec.Ms, rec.Via, rec.Method, name, kind(p), shape(p))
		if rec.Seq == 1 {
			fmt.Printf("     content-type=%q headers=%v\n", rec.ContentType, rec.HeaderNames)
		}
	}
	return nil
}

var enumKeys = map[string]bool{"permission_mode": true, "stop_hook_active": true, "source": true,
	"reason": true, "notification_type": true, "trigger": true, "tool_name": true, "hook_event_name": true}

func shape(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := p[k]
		var t string
		switch x := v.(type) {
		case string:
			t = "str"
		case bool:
			t = fmt.Sprintf("bool(%v)", x)
		case float64:
			t = "num"
		case nil:
			t = "null"
		case []any:
			t = fmt.Sprintf("arr[%d]", len(x))
		case map[string]any:
			sub := make([]string, 0, len(x))
			for sk := range x {
				sub = append(sub, sk)
			}
			sort.Strings(sub)
			t = "{" + strings.Join(sub, ",") + "}"
		}
		if s, ok := v.(string); ok && enumKeys[k] {
			t = "=" + s
		}
		parts = append(parts, k+":"+t)
	}
	return strings.Join(parts, " ")
}
