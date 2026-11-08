package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Session history (ADR 0036): every session's terminal output, saved as the
// scrollback the user saw, so it stays readable after the process, the
// daemon or the machine restarted. Files live in the data dir, user-only:
// <HistoryDir>/<project>/<agent>/<session>.log.

// historyEvery is how often a changed scrollback is saved while the session
// runs; it is saved once more when the process exits. Tests shorten it.
var historyEvery = 5 * time.Second

// MaxSavedTerminal bounds what is read back (the scrollback ring is 256 KiB).
const MaxSavedTerminal = 1 << 20

// SessionInfo is one of an agent's sessions, for its history.
type SessionInfo struct {
	ID          string     `json:"id"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ResumedFrom string     `json:"resumed_from,omitempty"`
	Turns       int64      `json:"turns"`
	ToolCalls   int64      `json:"tool_calls"`
	Live        bool       `json:"live"`     // the running process's session
	Terminal    bool       `json:"terminal"` // its terminal output was saved (or is live)
}

func (e *Engine) historyDir(agentID string) string {
	return filepath.Join(e.opts.HistoryDir, e.project.Slug, agentID)
}

func (e *Engine) historyPath(agentID, sid string) string {
	return filepath.Join(e.historyDir(agentID), sid+".log")
}

// keepHistory saves a's scrollback while its process runs and once at exit.
func (e *Engine) keepHistory(a *agentProc) {
	if e.opts.HistoryDir == "" || a.term == nil {
		return
	}
	path, term, log := e.historyPath(a.info.ID, a.info.SessionID), a.term, a.logger(e)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		var last uint64
		failed := false
		save := func() {
			data, gen := term.Snapshot()
			if gen == last {
				return
			}
			if err := writeFileAtomic(path, data); err != nil {
				if !failed {
					log.Warn("terminal history not saved", "err", err) // logged once per session
				}
				failed = true
				return
			}
			last = gen
		}
		t := time.NewTicker(historyEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				save()
			case <-term.Done():
				save()
				return
			}
		}
	}()
}

// writeFileAtomic writes data to path through a user-only temp file in the
// same directory, so a crash never leaves half a file under the final name.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*") // mode 0600
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// Sessions lists agent id's sessions, newest first.
func (e *Engine) Sessions(ctx context.Context, id string) ([]SessionInfo, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	live := ""
	if ok && a.running() {
		live = a.info.SessionID
	}
	e.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := e.q.ListAgentSessions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(rows))
	for _, r := range rows {
		s := SessionInfo{ID: r.ID, StartedAt: r.StartedAt.UTC(), ResumedFrom: r.ResumedFrom, Turns: r.Turns,
			ToolCalls: r.ToolCalls, Live: r.ID == live}
		if r.EndedAt.Valid {
			t := r.EndedAt.Time.UTC()
			s.EndedAt = &t
		}
		if s.Live {
			s.Terminal = true
		} else if e.opts.HistoryDir != "" {
			fi, err := os.Lstat(e.historyPath(id, r.ID))
			s.Terminal = err == nil && fi.Mode().IsRegular()
		}
		out = append(out, s)
	}
	return out, nil
}

// SessionTerminal returns the terminal output of one of agent id's sessions:
// the live scrollback for the running session, else the saved one.
func (e *Engine) SessionTerminal(ctx context.Context, id, sid string) ([]byte, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	var term interface{ Snapshot() ([]byte, uint64) }
	if ok && a.term != nil && a.info.SessionID == sid {
		term = a.term
	}
	e.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	s, err := e.q.GetSession(ctx, sid)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.AgentID != id) {
		return nil, fmt.Errorf("session %s of agent %s: %w", sid, id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if term != nil {
		data, _ := term.Snapshot()
		return data, nil
	}
	if e.opts.HistoryDir == "" {
		return nil, fmt.Errorf("%w: terminal history is off", ErrNotFound)
	}
	return readSaved(e.historyPath(id, s.ID))
}

// readSaved reads a saved terminal: a regular file, not followed if it is a
// symlink, at most MaxSavedTerminal bytes (its tail if longer).
func readSaved(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: this session's terminal was not saved", ErrNotFound)
	}
	if err != nil {
		return nil, errors.New("saved terminal not readable") // never the path
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("saved terminal not readable")
	}
	if over := fi.Size() - MaxSavedTerminal; over > 0 {
		if _, err := f.Seek(over, io.SeekStart); err != nil {
			return nil, errors.New("saved terminal not readable")
		}
	}
	return io.ReadAll(io.LimitReader(f, MaxSavedTerminal))
}

// removeHistory deletes an agent's saved terminals (when it is removed).
func (e *Engine) removeHistory(id string) {
	if e.opts.HistoryDir == "" {
		return
	}
	if err := os.RemoveAll(e.historyDir(id)); err != nil {
		e.log.Warn("remove terminal history", "agent_id", id, "err", err)
	}
}

// pruneHistory deletes saved terminals older than the event retention
// (ADR 0028), and directories left empty.
func (e *Engine) pruneHistory() {
	if e.opts.HistoryDir == "" || e.opts.EventRetention <= 0 {
		return
	}
	root := filepath.Join(e.opts.HistoryDir, e.project.Slug)
	cutoff, n := time.Now().Add(-e.opts.EventRetention), 0
	agents, err := os.ReadDir(root)
	if err != nil {
		return // nothing saved yet
	}
	for _, d := range agents {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		files, _ := os.ReadDir(dir)
		left := len(files)
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || !strings.HasSuffix(f.Name(), ".log") || info.ModTime().After(cutoff) {
				continue
			}
			if os.Remove(filepath.Join(dir, f.Name())) == nil {
				n++
				left--
			}
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
	if n > 0 {
		e.log.Info("old terminal histories deleted", "count", n, "older_than_days", int(e.opts.EventRetention.Hours()/24))
	}
}
