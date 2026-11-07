// Package usage reads token usage of agent sessions and prices it (ADR 0026).
//
// Usage comes from two places: API responses passing a sandboxed agent's API
// socket (Meter) and Claude Code transcripts of host-mode agents
// (FileReader). Both decode numeric fields and ids only; prompts, tool input
// and output, and assistant text are never kept. Prices come from a table
// (Prices) applied when usage is read, so changing the table re-prices
// history.
package usage

import "math"

// Tokens are the token counts of one or more API messages.
type Tokens struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Add returns the sum of t and o.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{Input: t.Input + o.Input, Output: t.Output + o.Output, CacheRead: t.CacheRead + o.CacheRead,
		CacheWrite5m: t.CacheWrite5m + o.CacheWrite5m, CacheWrite1h: t.CacheWrite1h + o.CacheWrite1h}
}

// Total is every token counted.
func (t Tokens) Total() int64 { return t.Prompt() + t.Output }

// Prompt is the prompt length of a request: input plus cache reads and
// writes. Long-context prices depend on it.
func (t Tokens) Prompt() int64 { return t.Input + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h }

// Message is the usage of one API message (one request's response).
type Message struct {
	ID        string `json:"id"`                   // the API's message id (msg_…)
	RequestID string `json:"request_id,omitempty"` // Anthropic's request-id; the transcript's requestId
	Model     string `json:"model"`
	Speed     string `json:"speed,omitempty"`         // "standard" or "fast"
	Geo       string `json:"inference_geo,omitempty"` // e.g. "global", "us"
	Tokens
}

// maxCount bounds a decoded token count, so a corrupt or hostile number
// cannot overflow sums or costs (2^40 tokens × the highest allowed price,
// 10^7 nano-USD per token, stays below 2^63).
const maxCount = 1 << 40

func count(p *int64) int64 {
	switch {
	case p == nil, *p < 0:
		return 0
	case *p > maxCount:
		return maxCount
	}
	return *p
}

// apiUsage is the API's usage object, as it appears in responses, stream
// events and transcript records. Only numbers and two enum strings.
type apiUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	Breakdown     *struct {
		E5m *int64 `json:"ephemeral_5m_input_tokens"`
		E1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Speed *string `json:"speed"`
	Geo   *string `json:"inference_geo"`
}

// apply sets the fields present in u on m. Stream deltas are cumulative, so a
// present field replaces the earlier value. Cache writes without a TTL
// breakdown count as 5-minute writes.
func (u *apiUsage) apply(m *Message) {
	if u == nil {
		return
	}
	if u.Input != nil {
		m.Input = count(u.Input)
	}
	if u.Output != nil {
		m.Output = count(u.Output)
	}
	if u.CacheRead != nil {
		m.CacheRead = count(u.CacheRead)
	}
	switch {
	case u.Breakdown != nil && (u.Breakdown.E5m != nil || u.Breakdown.E1h != nil):
		m.CacheWrite5m, m.CacheWrite1h = count(u.Breakdown.E5m), count(u.Breakdown.E1h)
	case u.CacheCreation != nil:
		m.CacheWrite5m = max(count(u.CacheCreation)-m.CacheWrite1h, 0)
	}
	if u.Speed != nil {
		m.Speed = enum(*u.Speed)
	}
	if u.Geo != nil {
		m.Geo = enum(*u.Geo)
	}
}

// enum keeps a short identifier-like string and drops anything else, so a
// field meant for "fast" or "us" can never carry text.
func enum(s string) string {
	if len(s) == 0 || len(s) > 32 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return s
}

// id keeps an API identifier (message id, request id, model id) and drops
// anything that does not look like one.
func id(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' || c == '@') {
			return ""
		}
	}
	return s
}

// finite reports whether f is a usable price.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 }
