// Spike A: PTY <-> WebSocket <-> xterm.js round-trip.
//
// Throwaway code. It answers: can we run a process in a PTY, stream it to a
// browser, accept input and resize, and let a browser tab detach and re-attach
// without killing the process (scrollback replay)?
//
// Run: go run . then open http://127.0.0.1:7681/?session=demo
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

//go:embed index.html
var indexHTML []byte

const scrollbackLimit = 256 << 10 // 256 KiB replayed on re-attach

// session is one long-lived process in a PTY. It outlives WebSocket connections.
type session struct {
	ptmx *os.File
	cmd  *exec.Cmd

	mu         sync.Mutex
	scrollback []byte
	subs       map[chan []byte]struct{}
	exited     bool
}

func startSession(argv []string) (*session, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "PS1=$ ")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, err
	}
	s := &session{ptmx: ptmx, cmd: cmd, subs: map[chan []byte]struct{}{}}
	go s.pump()
	return s, nil
}

// pump copies PTY output into the scrollback and fans it out to subscribers.
// Appending and broadcasting happen under one lock, so a subscriber that
// attaches gets scrollback + live output with no gap and no duplicates.
func (s *session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.scrollback = append(s.scrollback, chunk...)
			if over := len(s.scrollback) - scrollbackLimit; over > 0 {
				s.scrollback = s.scrollback[over:]
			}
			for ch := range s.subs {
				select {
				case ch <- chunk:
				default: // slow client: drop it rather than block the PTY
					close(ch)
					delete(s.subs, ch)
				}
			}
			s.mu.Unlock()
		}
		if err != nil { // EIO when the child exits
			break
		}
	}
	_ = s.cmd.Wait()
	s.mu.Lock()
	s.exited = true
	for ch := range s.subs {
		close(ch)
	}
	s.subs = nil
	s.mu.Unlock()
}

// attach returns a snapshot of the scrollback and a channel of live output.
func (s *session) attach() ([]byte, chan []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return append([]byte(nil), s.scrollback...), nil, false
	}
	ch := make(chan []byte, 256)
	s.subs[ch] = struct{}{}
	return append([]byte(nil), s.scrollback...), ch, true
}

func (s *session) detach(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// clientMsg is the browser -> server protocol (text frames). Server -> browser
// is raw PTY bytes in binary frames.
type clientMsg struct {
	Type string `json:"type"` // "input" | "resize"
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type server struct {
	argv     []string
	mu       sync.Mutex
	sessions map[string]*session
}

func (srv *server) get(id string) (*session, error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if s, ok := srv.sessions[id]; ok {
		return s, nil
	}
	s, err := startSession(srv.argv)
	if err != nil {
		return nil, err
	}
	srv.sessions[id] = s
	return s, nil
}

func (srv *server) handleWS(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if id == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}
	// Default AcceptOptions enforce same-origin, which blocks other websites
	// from driving a local shell (cross-site WebSocket hijacking).
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	s, err := srv.get(id)
	if err != nil {
		c.Close(websocket.StatusInternalError, "start failed")
		return
	}
	snapshot, live, ok := s.attach()
	ctx := r.Context()
	if len(snapshot) > 0 {
		if err := c.Write(ctx, websocket.MessageBinary, snapshot); err != nil {
			return
		}
	}
	if !ok {
		c.Close(websocket.StatusNormalClosure, "process exited")
		return
	}
	defer s.detach(live)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // browser -> PTY
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m clientMsg
			if err := json.Unmarshal(data, &m); err != nil {
				slog.Warn("bad client message", "err", err)
				continue
			}
			switch m.Type {
			case "input":
				_, _ = io.WriteString(s.ptmx, m.Data)
			case "resize":
				if m.Cols > 0 && m.Rows > 0 {
					_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: m.Cols, Rows: m.Rows})
				}
			}
		}
	}()
	for { // PTY -> browser
		select {
		case <-ctx.Done():
			return
		case chunk, open := <-live:
			if !open {
				c.Close(websocket.StatusNormalClosure, "process exited")
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	}
}

func newMux(argv []string) http.Handler {
	srv := &server{argv: argv, sessions: map[string]*session{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /ws", srv.handleWS)
	return mux
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7681", "listen address (keep it on loopback)")
	flag.Parse()
	argv := []string{"bash", "--norc", "--noprofile"}
	slog.Info("listening", "url", "http://"+*addr+"/?session=demo")
	if err := http.ListenAndServe(*addr, newMux(argv)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
