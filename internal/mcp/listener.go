package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxConns bounds concurrent MCP connections per agent. Claude Code opens one
// per session; the rest are refused.
const MaxConns = 4

// MaxFrame bounds one JSON-RPC message from an agent.
const MaxFrame = 1 << 20

// Listener serves one agent's MCP socket. Which socket a connection arrives
// on identifies the agent (ADR 0006), so the server it gets is that agent's.
type Listener struct {
	path   string
	l      net.Listener
	srv    *sdk.Server
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// Listen creates the socket at path (mode 0600; its directory must exist and
// be private) and serves srv on every connection.
func Listen(path string, srv *sdk.Server, log *slog.Logger) (*Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("mcp: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("mcp: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("mcp: chmod socket: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ln := &Listener{path: path, l: l, srv: srv, log: log, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	ln.wg.Add(1)
	go ln.serve()
	return ln, nil
}

// Path returns the socket path.
func (ln *Listener) Path() string { return ln.path }

func (ln *Listener) serve() {
	defer ln.wg.Done()
	for {
		c, err := ln.l.Accept()
		if err != nil {
			return // closed
		}
		ln.mu.Lock()
		if len(ln.conns) >= MaxConns {
			ln.mu.Unlock()
			ln.log.Warn("mcp connection refused: too many connections", "limit", MaxConns)
			c.Close()
			continue
		}
		ln.conns[c] = struct{}{}
		ln.wg.Add(1)
		ln.mu.Unlock()
		go ln.handle(c)
	}
}

func (ln *Listener) handle(c net.Conn) {
	defer ln.wg.Done()
	defer func() {
		ln.mu.Lock()
		delete(ln.conns, c)
		ln.mu.Unlock()
		c.Close()
	}()
	ss, err := ln.srv.Connect(ln.ctx, &sdk.IOTransport{Reader: c, Writer: nopClose{c}, MaxLineLength: MaxFrame}, nil)
	if err != nil {
		ln.log.Warn("mcp connect failed", "err", err)
		return
	}
	ln.log.Debug("mcp session started")
	_ = ss.Wait()
	ln.log.Debug("mcp session ended")
}

// Close stops accepting, ends every session and removes the socket.
func (ln *Listener) Close() error {
	err := ln.l.Close()
	ln.cancel()
	ln.mu.Lock()
	for c := range ln.conns {
		c.Close() // ends the session's reads, so Wait returns
	}
	ln.mu.Unlock()
	ln.wg.Wait()
	_ = os.Remove(ln.path)
	return err
}

// nopClose keeps the transport from closing the connection twice (reader and
// writer are the same conn); handle closes it.
type nopClose struct{ io.Writer }

func (nopClose) Close() error { return nil }
