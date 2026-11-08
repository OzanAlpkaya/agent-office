package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"sync"
)

// Limits of the meter.
const (
	maxEventLine = 1 << 20 // SSE lines the meter looks at (message_start/delta are small)
	maxJSONBody  = 8 << 20 // non-streaming bodies the meter parses
)

// MeterBody wraps an API response body so that the usage it carries is
// reported as the client reads it (ADR 0026). The wrapper returns exactly the
// bytes of body, as soon as they arrive: streams are never buffered.
//
// For text/event-stream, report is called after message_start and after every
// message_delta with the message so far (delta counts are cumulative). For
// application/json, report is called once the whole body was read. Other
// content types pass through unmetered (report is never called). onError is
// called at most once if the usage cannot be parsed; the bytes still pass.
func MeterBody(body io.ReadCloser, contentType, requestID string, report func(Message), onError func(error)) io.ReadCloser {
	mt, _, _ := mime.ParseMediaType(contentType)
	m := &meter{body: body, report: report, onError: onError, msg: Message{RequestID: id(requestID)}}
	switch mt {
	case "text/event-stream":
		m.sse = true
	case "application/json":
	default:
		return body
	}
	return m
}

type meter struct {
	body    io.ReadCloser
	report  func(Message)
	onError func(error)
	sse     bool

	mu      sync.Mutex
	msg     Message
	line    []byte // SSE: the current line
	long    bool   // SSE: the current line is longer than maxEventLine; ignored
	json    bytes.Buffer
	tooBig  bool
	done    bool
	errSent bool
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 {
		m.mu.Lock()
		if m.sse {
			m.scan(p[:n])
		} else if !m.tooBig {
			if m.json.Len()+n > maxJSONBody {
				m.tooBig = true
				m.json = bytes.Buffer{}
			} else {
				m.json.Write(p[:n])
			}
		}
		m.mu.Unlock()
	}
	if err == io.EOF {
		m.finish()
	}
	return n, err
}

func (m *meter) Close() error { return m.body.Close() }

// finish parses a JSON body once it was read to the end.
func (m *meter) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done || m.sse {
		return
	}
	m.done = true
	if m.tooBig {
		m.fail(errTooBig)
		return
	}
	var body struct {
		Type  string    `json:"type"`
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage *apiUsage `json:"usage"`
	}
	if err := json.Unmarshal(m.json.Bytes(), &body); err != nil {
		m.fail(err)
		return
	}
	m.json = bytes.Buffer{}
	if body.Type != "message" || body.Usage == nil {
		return // an error body or another shape: nothing billed
	}
	m.msg.ID, m.msg.Model = id(body.ID), id(body.Model)
	body.Usage.apply(&m.msg)
	if m.msg.ID != "" && m.msg.Model != "" {
		m.report(m.msg)
	}
}

// scan feeds stream bytes into the line splitter.
func (m *meter) scan(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		part := b
		if i >= 0 {
			part = b[:i]
		}
		if !m.long {
			if len(m.line)+len(part) > maxEventLine {
				m.long, m.line = true, m.line[:0]
			} else {
				m.line = append(m.line, part...)
			}
		}
		if i < 0 {
			return
		}
		if !m.long {
			m.event(bytes.TrimRight(m.line, "\r"))
		}
		m.line, m.long = m.line[:0], false
		b = b[i+1:]
	}
}

var dataPrefix = []byte("data:")

// event handles one complete SSE line.
func (m *meter) event(line []byte) {
	if !bytes.HasPrefix(line, dataPrefix) {
		return
	}
	data := bytes.TrimSpace(line[len(dataPrefix):])
	// Cheap filter: most lines are content deltas.
	if !bytes.Contains(data, []byte(`"message_start"`)) && !bytes.Contains(data, []byte(`"message_delta"`)) {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			ID    string    `json:"id"`
			Model string    `json:"model"`
			Usage *apiUsage `json:"usage"`
		} `json:"message"`
		Usage *apiUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		m.fail(err)
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message == nil {
			return
		}
		m.msg.ID, m.msg.Model = id(ev.Message.ID), id(ev.Message.Model)
		ev.Message.Usage.apply(&m.msg)
	case "message_delta":
		if m.msg.ID == "" {
			return
		}
		ev.Usage.apply(&m.msg)
	default:
		return
	}
	if m.msg.ID != "" && m.msg.Model != "" {
		m.report(m.msg)
	}
}

func (m *meter) fail(err error) {
	if !m.errSent && m.onError != nil {
		m.errSent = true
		m.onError(err)
	}
}

type meterError string

func (e meterError) Error() string { return string(e) }

const errTooBig = meterError("usage: response body too large to meter")
