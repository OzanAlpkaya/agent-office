package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

const fixtureSession = "11111111-2222-4333-8444-555555555555"

// The fixture's three messages (testdata/README.md).
var (
	msgA = Message{ID: "msg_01AAAA", RequestID: "req_01AAAA", Model: "claude-opus-5-5", Speed: "standard", Geo: "global",
		Tokens: Tokens{Input: 12, Output: 480, CacheRead: 20000, CacheWrite1h: 5000}}
	msgB = Message{ID: "msg_01BBBB", RequestID: "req_01BBBB", Model: "claude-haiku-5-5", Speed: "standard", Geo: "global",
		Tokens: Tokens{Input: 300, Output: 40, CacheWrite5m: 1200}}
	msgC = Message{ID: "msg_01CCCC", RequestID: "req_01CCCC", Model: "claude-sonnet-5-5", Speed: "standard", Geo: "global",
		Tokens: Tokens{Input: 8, Output: 900, CacheRead: 10000, CacheWrite5m: 2000}}
)

func fixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "session", fixtureSession+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readSession(t *testing.T, path string) Reading {
	t.Helper()
	s, err := SessionTranscript(path, fixtureSession)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadNew()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTranscriptFixture(t *testing.T) {
	r := readSession(t, fixturePath(t))
	want := []Message{msgA, msgB, msgC}
	if len(r.Messages) != len(want) {
		t.Fatalf("messages = %+v, want %d", r.Messages, len(want))
	}
	for i := range want {
		if r.Messages[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, r.Messages[i], want[i])
		}
	}
	if r.CLICostMicroUSD == nil || *r.CLICostMicroUSD != 68864 {
		t.Errorf("cli cost = %v", r.CLICostMicroUSD)
	}
	if r.Skipped != 0 {
		t.Errorf("skipped = %d", r.Skipped)
	}
	noSensitive(t, r)
}

// The DoD check: the same messages read from the session files and metered
// from the API responses give the same usage, and the price table reproduces
// the transcript's own cost.
func TestTranscriptMatchesAPIResponses(t *testing.T) {
	fromTranscript := readSession(t, fixturePath(t))
	byID := map[string]Message{}
	for _, m := range fromTranscript.Messages {
		byID[m.ID] = m
	}
	var metered []Message
	for _, f := range []struct{ file, ctype, req string }{
		{"stream_a.sse", "text/event-stream; charset=utf-8", "req_01AAAA"},
		{"message_b.json", "application/json", "req_01BBBB"},
		{"stream_c.sse", "text/event-stream", "req_01CCCC"},
	} {
		got := meterFile(t, f.file, f.ctype, f.req)
		if len(got) == 0 {
			t.Fatalf("%s: nothing metered", f.file)
		}
		metered = append(metered, got[len(got)-1])
	}
	var total Tokens
	var cost int64 // nano-USD
	p := Builtin()
	for _, m := range metered {
		tm, ok := byID[m.ID]
		// The SSE fixture of C has no TTL breakdown and no speed/geo (older
		// response shape): the tokens must still match.
		if !ok || tm.Tokens != m.Tokens || tm.Model != m.Model || tm.RequestID != m.RequestID {
			t.Errorf("metered %+v, transcript %+v", m, tm)
		}
		total = total.Add(m.Tokens)
		c, ok := p.Cost(m)
		if !ok {
			t.Fatalf("no price for %s", m.Model)
		}
		cost += c
	}
	if want := (Tokens{Input: 320, Output: 1420, CacheRead: 30000, CacheWrite5m: 3200, CacheWrite1h: 5000}); total != want {
		t.Errorf("total = %+v, want %+v", total, want)
	}
	if cost != 68_864_000 || MicroUSD(cost) != *fromTranscript.CLICostMicroUSD {
		t.Errorf("cost = %d nano-USD, transcript cost-state = %d micro-USD", cost, *fromTranscript.CLICostMicroUSD)
	}
}

func meterFile(t *testing.T, name, ctype, req string) []Message {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var got []Message
	var errs []error
	body := MeterBody(io.NopCloser(iotest.OneByteReader(bytes.NewReader(b))), ctype, req,
		func(m Message) { got = append(got, m) }, func(err error) { errs = append(errs, err) })
	out, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, b) {
		t.Fatalf("%s: the meter changed the body", name)
	}
	if len(errs) > 0 {
		t.Fatalf("%s: %v", name, errs)
	}
	return got
}

func TestMeterStreamCumulative(t *testing.T) {
	got := meterFile(t, "stream_a.sse", "text/event-stream", "req_01AAAA")
	if len(got) != 2 {
		t.Fatalf("reports = %d, want 2 (start, delta)", len(got))
	}
	if got[0].Output != 3 || got[0].CacheRead != 20000 {
		t.Errorf("after message_start: %+v", got[0])
	}
	if got[1] != msgA {
		t.Errorf("after message_delta: %+v, want %+v", got[1], msgA)
	}
}

// Bytes reach the client as they arrive: the meter never waits for more.
func TestMeterDoesNotBuffer(t *testing.T) {
	pr, pw := io.Pipe()
	body := MeterBody(pr, "text/event-stream", "", func(Message) {}, nil)
	go pw.Write([]byte("event: ping\n"))
	buf := make([]byte, 64)
	n, err := body.Read(buf)
	if err != nil || string(buf[:n]) != "event: ping\n" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
	pw.Close()
}

func TestMeterIgnoresOtherShapes(t *testing.T) {
	for _, c := range []struct{ ctype, body string }{
		{"application/json", `{"type":"error","error":{"type":"overloaded_error","message":"SENSITIVE"}}`},
		{"text/plain", `{"type":"message","id":"msg_1","model":"m","usage":{"input_tokens":1}}`},
		{"text/event-stream", "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n"}, // delta without a start
	} {
		reported := false
		body := MeterBody(io.NopCloser(strings.NewReader(c.body)), c.ctype, "", func(Message) { reported = true }, nil)
		if _, err := io.ReadAll(body); err != nil {
			t.Fatal(err)
		}
		if reported {
			t.Errorf("%s %q: reported usage", c.ctype, c.body)
		}
	}
}

func TestMeterReportsBrokenJSONOnce(t *testing.T) {
	var errs int
	body := MeterBody(io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\", broken\n\ndata: {\"type\":\"message_delta\" also broken\n")),
		"text/event-stream", "", func(Message) {}, func(error) { errs++ })
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if errs != 1 {
		t.Errorf("errors = %d, want 1", errs)
	}
}

func TestMeterIgnoresHugeLines(t *testing.T) {
	huge := "data: {\"type\":\"message_start\"," + strings.Repeat("x", maxEventLine) + "\n"
	start := `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-5-5","usage":{"input_tokens":7}}}` + "\n"
	var got []Message
	body := MeterBody(io.NopCloser(strings.NewReader(huge+start)), "text/event-stream", "", func(m Message) { got = append(got, m) }, nil)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Input != 7 {
		t.Errorf("got %+v", got)
	}
}

func TestIncrementalReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fixtureSession+".jsonl")
	src, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := SessionTranscript(path, fixtureSession)
	if err != nil {
		t.Fatal(err)
	}
	// First half, plus half of the next line: the partial line waits.
	half := len(lines) / 2
	for _, l := range lines[:half] {
		f.Write(l)
	}
	f.Write(lines[half][:10])
	r1, err := s.ReadNew()
	if err != nil {
		t.Fatal(err)
	}
	f.Write(lines[half][10:])
	for _, l := range lines[half+1:] {
		f.Write(l)
	}
	r2, err := s.ReadNew()
	if err != nil {
		t.Fatal(err)
	}
	r1.merge(r2)
	if len(r1.Messages) != 2 || r1.Messages[0] != msgA || r1.Messages[1] != msgB || r1.Skipped != 0 {
		t.Errorf("merged reads = %+v", r1)
	}
	r3, err := s.ReadNew()
	if err != nil || len(r3.Messages) != 0 {
		t.Errorf("third read = %+v, %v", r3, err)
	}
}

func TestOversizeLineIsSkippedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fixtureSession+".jsonl")
	line := func(id string) string {
		return `{"type":"assistant","message":{"id":"` + id + `","model":"claude-haiku-5-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	}
	big := `{"type":"assistant","x":"` + strings.Repeat("y", MaxLine) // no newline yet
	if err := os.WriteFile(path, []byte(line("msg_1")+big), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := SessionTranscript(path, fixtureSession)
	r1, err := s.ReadNew()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`"}` + "\n" + line("msg_2"))
	f.Close()
	r2, err := s.ReadNew()
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Messages) != 1 || len(r2.Messages) != 1 || r2.Messages[0].ID != "msg_2" || r1.Skipped+r2.Skipped != 1 {
		t.Errorf("r1 = %+v, r2 = %+v", r1, r2)
	}
}

func TestSessionTranscriptPath(t *testing.T) {
	for _, p := range []string{
		"relative/" + fixtureSession + ".jsonl",
		"/a/../b/" + fixtureSession + ".jsonl",
		"/a/other.jsonl",
		"/a/" + fixtureSession + ".json",
	} {
		if _, err := SessionTranscript(p, fixtureSession); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if _, err := SessionTranscript("/a/"+fixtureSession+".jsonl", ""); err == nil {
		t.Error("empty session accepted")
	}
}

func TestTranscriptRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fixtureSession+".jsonl")
	if err := os.Symlink(fixturePath(t), path); err != nil {
		t.Fatal(err)
	}
	s, _ := SessionTranscript(path, fixtureSession)
	if _, err := s.ReadNew(); err == nil {
		t.Error("read through a symlink")
	}
}

func TestPricesBuiltin(t *testing.T) {
	p := Builtin()
	p.Models["huge"] = Price{Rates: Rates{Output: 10_000_000}, Fast: 100_000} // the largest allowed price and multiplier
	if p.AsOf == "" || !strings.HasPrefix(p.Source, "https://") || len(p.Models) < 15 {
		t.Fatalf("builtin = %+v", p)
	}
	cases := []struct {
		m    Message
		want int64 // nano-USD
	}{
		{msgA, 53_648_000},
		{msgB, 200_000},
		{msgC, 15_016_000},
		// Date suffix matches the base id.
		{Message{Model: "claude-haiku-4-5-20251001", Tokens: Tokens{Input: 1_000_000}}, 1_000_000_000},
		// Haiku 5.5 above 100 000 prompt tokens (cache reads count): 1000×500 + 100000×50 + 1000×2500.
		{Message{Model: "claude-haiku-5-5", Tokens: Tokens{Input: 1000, CacheRead: 100_000, Output: 1000}}, 8_000_000},
		// At or below: 1000×100 + 99000×10 + 1000×500.
		{Message{Model: "claude-haiku-5-5", Tokens: Tokens{Input: 1000, CacheRead: 99_000, Output: 1000}}, 1_590_000},
		// Fast mode and US inference.
		{Message{Model: "claude-opus-5-5", Speed: "fast", Tokens: Tokens{Output: 1_000_000}}, 40_000_000_000},
		{Message{Model: "claude-opus-5-5", Geo: "us", Tokens: Tokens{Output: 1_000_000}}, 22_000_000_000},
		{Message{Model: "claude-opus-4-5", Geo: "us", Tokens: Tokens{Output: 1_000_000}}, 25_000_000_000},
		// The largest token counts cannot overflow.
		{Message{Model: "claude-opus-4-1", Tokens: Tokens{Output: maxCount}}, maxCount * 75_000},
		{Message{Model: "claude-opus-4-1", Tokens: Tokens{Input: maxCount, Output: maxCount, CacheRead: maxCount,
			CacheWrite5m: maxCount, CacheWrite1h: maxCount}}, 15_000*maxCount + 75_000*maxCount + 1_500*maxCount + 18_750*maxCount + 30_000*maxCount},
		{Message{Model: "huge", Speed: "fast", Tokens: Tokens{Output: maxCount}}, math.MaxInt64 / 1000},
	}
	for _, c := range cases {
		got, ok := p.Cost(c.m)
		if !ok || got != c.want {
			t.Errorf("Cost(%+v) = %d, %v; want %d", c.m, got, ok, c.want)
		}
	}
	if MicroUSD(1499) != 1 || MicroUSD(1500) != 2 || MicroUSD(0) != 0 {
		t.Error("MicroUSD rounding")
	}
	if AddNano(math.MaxInt64-1, 5) != math.MaxInt64 {
		t.Error("AddNano does not saturate")
	}
	if _, ok := p.Cost(Message{Model: "claude-unknown-9", Tokens: Tokens{Input: 1}}); ok {
		t.Error("unknown model priced")
	}
}

func writePrices(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "prices.yaml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPricesUserFile(t *testing.T) {
	path := writePrices(t, `as_of: "2026-12-01"
models:
  claude-haiku-5-5: {input: 1, output: 2, cache_read: 0.1, cache_write_5m: 1.25, cache_write_1h: 2}
  my-local-model: {input: 0, output: 0, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0}
`, 0o644)
	p, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.AsOf != "2026-12-01" || p.File != path || p.Models["claude-haiku-5-5"].Input != 1000 || p.Models["claude-haiku-5-5"].Long != nil {
		t.Errorf("merged = %+v", p.Models["claude-haiku-5-5"])
	}
	if _, ok := p.Lookup("my-local-model"); !ok {
		t.Error("added model missing")
	}
	if _, ok := p.Lookup("claude-opus-5-5"); !ok {
		t.Error("built-in model lost")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), false); err != nil {
		t.Errorf("optional missing file: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true); err == nil {
		t.Error("required missing file accepted")
	}
}

func TestPricesInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown field":     "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1, extra: 1}\n",
		"missing rate":      "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1}\n",
		"negative":          "models:\n  m: {input: -1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n",
		"bad id":            "models:\n  'Bad Model': {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n",
		"bad multiplier":    "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1, fast: 0.5}\n",
		"long without size": "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1, long_context: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}}\n",
		"bad date":          "as_of: yesterday\nmodels:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n",
		"empty":             "",
		"two documents":     "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n---\nmodels: {}\n",
		"too precise":       "models:\n  m: {input: 0.0001, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n",
	}
	for name, body := range cases {
		if _, err := Load(writePrices(t, body, 0o600), true); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "prices.yaml") {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
	}
	// Every problem at once.
	_, err := Load(writePrices(t, "models:\n  a: {input: -1, output: 1, cache_read: 1, cache_write_5m: 1}\n", 0o600), true)
	var pe *PricesError
	if !asPricesError(err, &pe) || len(pe.Problems) != 2 {
		t.Errorf("problems = %v", err)
	}
	if _, err := Load(writePrices(t, "models:\n  m: {input: 1, output: 1, cache_read: 1, cache_write_5m: 1, cache_write_1h: 1}\n", 0o666), true); err == nil {
		t.Error("world-writable file accepted")
	}
}

func asPricesError(err error, pe **PricesError) bool {
	e, ok := err.(*PricesError)
	*pe = e
	return ok
}

// noSensitive fails if any marker text from the fixtures reached r.
func noSensitive(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("SENSITIVE")) || bytes.Contains(b, []byte("redacted")) {
		t.Errorf("content leaked: %s", b)
	}
}
