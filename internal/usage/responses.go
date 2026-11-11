package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"sync"
)

// maxResponsesLine bounds the SSE lines the Responses meter parses: the
// final event carries the whole response, so it is larger than Anthropic's
// usage events.
const maxResponsesLine = 16 << 20

// MeterResponses is MeterBody for OpenAI's Responses API stream, which Codex
// uses (ADR 0049): usage arrives once, in the final response.completed (or
// .incomplete / .failed) event. input_tokens includes the cached tokens, so
// Input is what was not read from the cache. The bytes pass through
// unchanged and unbuffered. ChatGPT's backend sends the stream without a
// Content-Type (found in the Phase 15 e2e run), so an untyped body is
// metered as a stream too.
func MeterResponses(body io.ReadCloser, contentType, requestID string, report func(Message), onError func(error)) io.ReadCloser {
	if mt, _, _ := mime.ParseMediaType(contentType); contentType != "" && mt != "text/event-stream" {
		return body
	}
	return &responsesMeter{body: body, report: report, onError: onError, requestID: id(requestID)}
}

type responsesMeter struct {
	body      io.ReadCloser
	report    func(Message)
	onError   func(error)
	requestID string

	mu      sync.Mutex
	line    []byte
	long    bool
	errSent bool
}

func (m *responsesMeter) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 {
		m.mu.Lock()
		m.scan(p[:n])
		m.mu.Unlock()
	}
	return n, err
}

func (m *responsesMeter) Close() error { return m.body.Close() }

func (m *responsesMeter) scan(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		part := b
		if i >= 0 {
			part = b[:i]
		}
		if !m.long {
			if len(m.line)+len(part) > maxResponsesLine {
				m.long, m.line = true, m.line[:0]
				m.fail(errTooBig)
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

// responsesUsage is the Responses API's usage object: numbers only.
type responsesUsage struct {
	Input       *int64 `json:"input_tokens"`
	InputDetail *struct {
		Cached     *int64 `json:"cached_tokens"`
		CacheWrite *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	Output *int64 `json:"output_tokens"`
}

func (m *responsesMeter) event(line []byte) {
	if !bytes.HasPrefix(line, dataPrefix) {
		return
	}
	data := bytes.TrimSpace(line[len(dataPrefix):])
	if !bytes.Contains(data, []byte(`"response.completed"`)) && !bytes.Contains(data, []byte(`"response.incomplete"`)) &&
		!bytes.Contains(data, []byte(`"response.failed"`)) {
		return
	}
	var ev struct {
		Type     string `json:"type"`
		Response *struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage *responsesUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		m.fail(err)
		return
	}
	switch ev.Type {
	case "response.completed", "response.incomplete", "response.failed":
	default:
		return
	}
	if ev.Response == nil || ev.Response.Usage == nil {
		return // a failure before any token was used
	}
	u := ev.Response.Usage
	msg := Message{ID: id(ev.Response.ID), RequestID: m.requestID, Model: id(ev.Response.Model)}
	input, cached := count(u.Input), int64(0)
	if d := u.InputDetail; d != nil {
		cached = min(count(d.Cached), input)
		msg.CacheWrite5m = count(d.CacheWrite)
	}
	msg.Input, msg.CacheRead, msg.Output = input-cached, cached, count(u.Output)
	if msg.ID != "" && msg.Model != "" {
		m.report(msg)
	}
}

func (m *responsesMeter) fail(err error) {
	if !m.errSent && m.onError != nil {
		m.errSent = true
		m.onError(err)
	}
}
