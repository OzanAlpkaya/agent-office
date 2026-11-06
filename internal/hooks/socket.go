package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MaxPayload bounds a hook payload or reply. Claude Code payloads include tool
// inputs and outputs, so this is generous, but it is still a hard limit.
const MaxPayload = 1 << 20

// connTimeout bounds one request on the daemon side.
const connTimeout = 5 * time.Second

// Handler handles one forwarded hook payload and returns the bytes the hook
// should print to stdout (nil for nothing).
type Handler func(ctx context.Context, payload []byte) ([]byte, error)

// Listener serves one agent's hook socket. Which socket a connection arrives
// on identifies the agent (ADR 0003).
type Listener struct {
	path string
	l    net.Listener
	wg   sync.WaitGroup
	log  *slog.Logger
}

// Listen creates the socket at path. Its directory is created with mode 0700
// and the socket is chmod 0600, so only the daemon's user can connect.
func Listen(path string, h Handler, log *slog.Logger) (*Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("hooks: socket dir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("hooks: socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("hooks: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("hooks: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("hooks: chmod socket: %w", err)
	}
	ln := &Listener{path: path, l: l, log: log}
	ln.wg.Add(1)
	go ln.serve(h)
	return ln, nil
}

// Path returns the socket path.
func (ln *Listener) Path() string { return ln.path }

func (ln *Listener) serve(h Handler) {
	defer ln.wg.Done()
	for {
		c, err := ln.l.Accept()
		if err != nil {
			return // closed
		}
		ln.wg.Add(1)
		go func() {
			defer ln.wg.Done()
			ln.handle(c, h)
		}()
	}
}

func (ln *Listener) handle(c net.Conn, h Handler) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(connTimeout))
	payload, err := io.ReadAll(io.LimitReader(c, MaxPayload+1))
	if err != nil {
		ln.log.Warn("hook read failed", "err", err)
		return
	}
	if len(payload) > MaxPayload {
		ln.log.Warn("hook payload too large", "limit", MaxPayload)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connTimeout)
	defer cancel()
	reply, err := h(ctx, payload)
	if err != nil {
		ln.log.Warn("hook rejected", "err", err)
		return
	}
	if len(reply) > 0 {
		_, _ = c.Write(reply)
	}
}

// Close stops accepting, waits for in-flight hooks and removes the socket.
func (ln *Listener) Close() error {
	err := ln.l.Close()
	ln.wg.Wait()
	_ = os.Remove(ln.path)
	return err
}

// Forward is the client side used by `agent-office hook`: it sends the payload
// read from in to the socket, half-closes, and copies the reply to out.
func Forward(ctx context.Context, socketPath string, in io.Reader, out io.Writer) error {
	payload, err := io.ReadAll(io.LimitReader(in, MaxPayload+1))
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	if len(payload) > MaxPayload {
		return fmt.Errorf("payload larger than %d bytes", MaxPayload)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write(payload); err != nil {
		return fmt.Errorf("send payload: %w", err)
	}
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	reply, err := io.ReadAll(io.LimitReader(c, MaxPayload))
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}
	_, err = out.Write(reply)
	return err
}
