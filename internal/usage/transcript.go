package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Limits of one transcript read.
const (
	MaxLine = 32 << 20 // longer lines are skipped (an assistant line holds tool input)
	MaxRead = 64 << 20 // per file and read; the rest is read next time

	maxSubagentFiles = 256
)

// Reading is what a transcript read found.
type Reading struct {
	// Messages are deduplicated by message id; the last line of a message
	// wins (Claude Code repeats the usage on every line of a multi-block
	// message). Order: first appearance.
	Messages []Message
	// CLICostUSD is the last cost-state record's totalCostUSD: Claude Code's
	// own estimate for the whole session. Nil when the read saw none.
	CLICostUSD *float64
	Skipped    int // lines that were too long or not JSON
}

func (r *Reading) merge(o Reading) {
	seen := make(map[string]int, len(r.Messages))
	for i, m := range r.Messages {
		seen[m.ID] = i
	}
	for _, m := range o.Messages {
		if i, ok := seen[m.ID]; ok {
			r.Messages[i] = m
			continue
		}
		seen[m.ID] = len(r.Messages)
		r.Messages = append(r.Messages, m)
	}
	if o.CLICostUSD != nil {
		r.CLICostUSD = o.CLICostUSD
	}
	r.Skipped += o.Skipped
}

// record holds the only transcript fields agent-office reads. Every other
// field (prompts, content, tool input and output) is skipped by the decoder
// and never kept.
type record struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage *apiUsage `json:"usage"`
	} `json:"message"`
	TotalCostUSD *float64 `json:"totalCostUSD"`
}

// parseLine adds what one transcript line holds to r.
func parseLine(line []byte, r *Reading, seen map[string]int) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		r.Skipped++
		return
	}
	switch rec.Type {
	case "assistant":
		if rec.Message == nil || rec.Message.Usage == nil {
			return
		}
		m := Message{ID: id(rec.Message.ID), RequestID: id(rec.RequestID), Model: id(rec.Message.Model)}
		// Synthetic messages (model "<synthetic>", e.g. an API error shown in
		// the TUI) have no id or a model that is not an id; they cost nothing.
		if m.ID == "" || m.Model == "" {
			return
		}
		rec.Message.Usage.apply(&m)
		if i, ok := seen[m.ID]; ok {
			r.Messages[i] = m
			return
		}
		seen[m.ID] = len(r.Messages)
		r.Messages = append(r.Messages, m)
	case "cost-state":
		if c := rec.TotalCostUSD; c != nil && finite(*c) {
			v := *c
			r.CLICostUSD = &v
		}
	}
}

// ReadTranscript reads a whole transcript (tests, the e2e run).
func ReadTranscript(src io.Reader) (Reading, error) {
	var f FileReader
	r, _, err := f.read(src)
	return r, err
}

// FileReader reads one transcript file incrementally: every call reads the
// complete lines added since the previous call. It is not safe for
// concurrent use.
type FileReader struct {
	Path     string
	offset   int64
	skipping bool // inside a line longer than MaxLine, across reads
}

// ReadNew reads the lines added since the last call. The file must be a
// regular file owned by the current user; it is opened without following a
// symlink.
func (f *FileReader) ReadNew() (Reading, error) {
	fh, err := openOwned(f.Path)
	if err != nil {
		return Reading{}, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return Reading{}, errors.New("usage: transcript: stat failed")
	}
	if fi.Size() < f.offset { // truncated or replaced: start over (rows are upserts)
		f.offset, f.skipping = 0, false
	}
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return Reading{}, errors.New("usage: transcript: seek failed")
	}
	r, n, err := f.read(io.LimitReader(fh, MaxRead))
	f.offset += n
	return r, err
}

// read parses complete lines from src and returns how many bytes it
// consumed. A partial last line is left for the next read unless it is
// already longer than MaxLine; then the rest of it is skipped next time.
func (f *FileReader) read(src io.Reader) (Reading, int64, error) {
	var r Reading
	seen := map[string]int{}
	br := bufio.NewReaderSize(src, 64<<10)
	var consumed, pending int64
	var line []byte
	tooLong, counted := f.skipping, f.skipping // a line skipped by an earlier read was counted there
	for {
		chunk, err := br.ReadSlice('\n')
		pending += int64(len(chunk))
		if !tooLong {
			if len(line)+len(chunk) > MaxLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil { // EOF (or a read error) before the newline
			if tooLong {
				consumed += pending
				f.skipping = true
				if !counted {
					r.Skipped++
				}
			}
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return r, consumed, err
		}
		consumed += pending
		pending = 0
		if tooLong {
			if !counted {
				r.Skipped++
			}
		} else if l := bytes.TrimSpace(line); len(l) > 0 {
			parseLine(l, &r, seen)
		}
		line, tooLong, counted, f.skipping = line[:0], false, false, false
	}
}

// openOwned opens a regular file owned by the current user without
// following a symlink; the checks run on the opened file. Errors never name
// the path (it is a local path, ADR 0026); errors.Is still matches the
// underlying error, e.g. os.ErrNotExist.
func openOwned(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("usage: transcript: not a clean absolute path")
	}
	fh, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, fmt.Errorf("usage: transcript: %w", err)
	}
	fi, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, errors.New("usage: transcript: stat failed")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || st.Uid != uint32(os.Getuid()) {
		fh.Close()
		return nil, errors.New("usage: transcript: not a regular file owned by you")
	}
	return fh, nil
}

// Session reads a host-mode session's transcript and its subagents' files
// incrementally (ADR 0026). Not safe for concurrent use.
type Session struct {
	main *FileReader
	dir  string                 // <session-id>/subagents
	subs map[string]*FileReader // by file name
}

// SessionTranscript validates a transcript path from a hook payload: it must
// be a clean absolute path named <sessionID>.jsonl. The file is checked when
// read.
func SessionTranscript(path, sessionID string) (*Session, error) {
	if sessionID == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(path) != sessionID+".jsonl" {
		return nil, errors.New("usage: transcript path is not <session-id>.jsonl")
	}
	return &Session{main: &FileReader{Path: path},
		dir: filepath.Join(filepath.Dir(path), sessionID, "subagents"), subs: map[string]*FileReader{}}, nil
}

// Path is the main transcript's path.
func (s *Session) Path() string { return s.main.Path }

// ReadNew reads what was added to the transcript and its subagent files
// since the last call. A missing subagent directory is normal.
func (s *Session) ReadNew() (Reading, error) {
	r, err := s.main.ReadNew()
	if err != nil {
		return r, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return r, nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".jsonl") || s.subs[n] == nil && len(s.subs) >= maxSubagentFiles {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fr := s.subs[n]
		if fr == nil {
			fr = &FileReader{Path: filepath.Join(s.dir, n)}
			s.subs[n] = fr
		}
		sub, err := fr.ReadNew()
		if err != nil {
			continue // a vanished or foreign file costs nothing
		}
		sub.CLICostUSD = nil // cost-state of the whole session lives in the main file
		r.merge(sub)
	}
	return r, nil
}
