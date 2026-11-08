// Package terminal runs a process in a pseudo-terminal and lets any number of
// viewers attach to it. A session outlives its viewers: detaching never stops
// the process, and a viewer that attaches later first receives the scrollback.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Size is a terminal size in character cells.
type Size struct {
	Cols uint16
	Rows uint16
}

// Options configures a session. Zero values select defaults.
type Options struct {
	Size             Size // default 80x24
	ScrollbackBytes  int  // default 256 KiB
	SubscriberBuffer int  // chunks buffered per viewer before it is dropped; default 256
}

// ErrExited is returned when writing to or resizing a session whose process has exited.
var ErrExited = errors.New("terminal: process exited")

// drainGrace bounds how long the session keeps reading after the process
// exits. Background children can keep the PTY open forever; they must not keep
// the session alive.
const drainGrace = 300 * time.Millisecond

// Session is one process running in a PTY.
type Session struct {
	ptmx *os.File
	cmd  *exec.Cmd
	done chan struct{}

	mu         sync.Mutex
	scrollback []byte
	gen        uint64 // output chunks received; Snapshot callers compare it to skip unchanged saves
	limit      int
	subBuf     int
	subs       map[*Subscriber]struct{}
	size       Size
	exited     bool
	exitErr    error
}

// Subscriber receives live output. C is closed when the subscriber is dropped
// (too slow), detached, or the process exits.
type Subscriber struct {
	C <-chan []byte
	c chan []byte
	s *Session
}

// Start runs cmd in a new PTY. cmd must not have been started.
func Start(cmd *exec.Cmd, opts Options) (*Session, error) {
	if opts.Size.Cols == 0 || opts.Size.Rows == 0 {
		opts.Size = Size{Cols: 80, Rows: 24}
	}
	if opts.ScrollbackBytes <= 0 {
		opts.ScrollbackBytes = 256 << 10
	}
	if opts.SubscriberBuffer <= 0 {
		opts.SubscriberBuffer = 256
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: opts.Size.Cols, Rows: opts.Size.Rows})
	if err != nil {
		return nil, fmt.Errorf("terminal: start %s: %w", cmd.Path, err)
	}
	s := &Session{
		ptmx:   ptmx,
		cmd:    cmd,
		done:   make(chan struct{}),
		limit:  opts.ScrollbackBytes,
		subBuf: opts.SubscriberBuffer,
		subs:   map[*Subscriber]struct{}{},
		size:   opts.Size,
	}
	readDone := make(chan struct{})
	go s.readLoop(readDone)
	go s.waitLoop(readDone)
	return s, nil
}

// readLoop copies PTY output into the scrollback and fans it out. Appending and
// broadcasting happen under one lock; Attach snapshots under the same lock, so
// a viewer sees scrollback + live output with no gap and no duplicates.
func (s *Session) readLoop(readDone chan<- struct{}) {
	defer close(readDone)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.broadcast(append([]byte(nil), buf[:n]...))
		}
		if err != nil { // EIO once no process holds the PTY, or closed by waitLoop
			return
		}
	}
}

func (s *Session) broadcast(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrollback = append(s.scrollback, chunk...)
	s.gen++
	if over := len(s.scrollback) - s.limit; over > 0 {
		s.scrollback = append([]byte(nil), s.scrollback[over:]...)
	}
	for sub := range s.subs {
		select {
		case sub.c <- chunk:
		default: // a slow viewer must never block the process
			delete(s.subs, sub)
			close(sub.c)
		}
	}
}

func (s *Session) waitLoop(readDone <-chan struct{}) {
	err := s.cmd.Wait()
	select {
	case <-readDone:
	case <-time.After(drainGrace):
	}
	_ = s.ptmx.Close()
	<-readDone

	s.mu.Lock()
	s.exited = true
	s.exitErr = err
	for sub := range s.subs {
		close(sub.c)
	}
	s.subs = nil
	s.mu.Unlock()
	close(s.done)
}

// Attach returns a copy of the scrollback and, if the process is still
// running, a subscriber for live output. ok is false once the process exited.
func (s *Session) Attach() (snapshot []byte, sub *Subscriber, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot = append([]byte(nil), s.scrollback...)
	if s.exited {
		return snapshot, nil, false
	}
	c := make(chan []byte, s.subBuf)
	sub = &Subscriber{C: c, c: c, s: s}
	s.subs[sub] = struct{}{}
	return snapshot, sub, true
}

// Snapshot returns a copy of the scrollback and a counter that changes
// whenever output arrives, so a caller saving the scrollback can skip
// unchanged snapshots.
func (s *Session) Snapshot() ([]byte, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.scrollback...), s.gen
}

// Close detaches the subscriber. It is safe to call more than once.
func (sub *Subscriber) Close() {
	s := sub.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		close(sub.c)
	}
}

// Write sends input to the process.
func (s *Session) Write(p []byte) (int, error) {
	if s.Exited() {
		return 0, ErrExited
	}
	return s.ptmx.Write(p)
}

// Resize changes the terminal size; the process receives SIGWINCH.
func (s *Session) Resize(sz Size) error {
	if sz.Cols == 0 || sz.Rows == 0 {
		return fmt.Errorf("terminal: invalid size %dx%d", sz.Cols, sz.Rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return ErrExited
	}
	s.size = sz
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: sz.Cols, Rows: sz.Rows})
}

// Redraw asks a full-screen program to repaint by briefly changing the
// terminal height. Raw scrollback is not a screen snapshot, so this is used
// after a viewer attaches.
func (s *Session) Redraw() error {
	s.mu.Lock()
	sz := s.size
	s.mu.Unlock()
	if sz.Rows < 2 {
		return nil
	}
	if err := s.Resize(Size{Cols: sz.Cols, Rows: sz.Rows - 1}); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	return s.Resize(sz)
}

// Size returns the current terminal size.
func (s *Session) Size() Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Pid returns the process id.
func (s *Session) Pid() int { return s.cmd.Process.Pid }

// Done is closed after the process exited and all output was delivered.
func (s *Session) Done() <-chan struct{} { return s.done }

// Exited reports whether the process has exited.
func (s *Session) Exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// ExitErr returns the error from waiting for the process (nil for exit code 0).
// Only meaningful after Done is closed.
func (s *Session) ExitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

// Stop sends SIGHUP and SIGTERM to the process group (like closing a terminal
// window; interactive shells ignore SIGTERM alone) and SIGKILL if it has not
// exited when ctx is done. It returns after the session is done.
func (s *Session) Stop(ctx context.Context) {
	if s.Exited() {
		return
	}
	pgid := s.cmd.Process.Pid // the child is a session leader (Setsid), so pgid == pid
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-s.done
	}
}
