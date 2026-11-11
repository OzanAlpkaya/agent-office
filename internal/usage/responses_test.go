package usage

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// A Responses stream shaped like the one Spike G recorded (Codex 0.167.1):
// deltas, then response.completed with the usage.
const responsesStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol","usage":null}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"O"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],` +
	`"usage":{"input_tokens":12153,"input_tokens_details":{"cached_tokens":9856,"cache_write_tokens":0},"output_tokens":5,` +
	`"output_tokens_details":{"reasoning_tokens":0},"total_tokens":12158}}}` + "\n\n"

func TestMeterResponses(t *testing.T) {
	var got []Message
	body := MeterResponses(io.NopCloser(strings.NewReader(responsesStream)), "text/event-stream; charset=utf-8", "req-1",
		func(m Message) { got = append(got, m) }, func(err error) { t.Fatal(err) })
	// ChatGPT's backend sends the stream without a Content-Type.
	var untyped []Message
	_, _ = io.ReadAll(MeterResponses(io.NopCloser(strings.NewReader(responsesStream)), "", "",
		func(m Message) { untyped = append(untyped, m) }, nil))
	if len(untyped) != 1 || untyped[0].Output != 5 {
		t.Fatalf("an untyped stream is not metered: %+v", untyped)
	}
	out, err := io.ReadAll(body)
	if err != nil || string(out) != responsesStream {
		t.Fatalf("the bytes changed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reports %+v", got)
	}
	m := got[0]
	if m.ID != "resp_1" || m.Model != "gpt-6.1-sol" || m.RequestID != "req-1" || m.Input != 12153-9856 || m.CacheRead != 9856 ||
		m.Output != 5 || m.Total() != 12158 {
		t.Fatalf("usage %+v", m)
	}
}

func TestMeterResponsesPassesOtherBodies(t *testing.T) {
	called := false
	body := MeterResponses(io.NopCloser(strings.NewReader(`{"models":[]}`)), "application/json", "",
		func(Message) { called = true }, nil)
	if b, _ := io.ReadAll(body); string(b) != `{"models":[]}` || called {
		t.Fatalf("a JSON body is passed through unmetered: %q %v", b, called)
	}
	// A broken final event is reported once; the bytes still pass.
	errs := 0
	broken := "data: {\"type\":\"response.completed\",\"response\":{\n\n"
	body = MeterResponses(io.NopCloser(bytes.NewReader([]byte(broken))), "text/event-stream", "",
		func(Message) { t.Fatal("reported") }, func(error) { errs++ })
	if b, _ := io.ReadAll(body); string(b) != broken || errs != 1 {
		t.Fatalf("broken event: %q, %d errors", b, errs)
	}
	// Cached tokens beyond the input never make Input negative.
	weird := `data: {"type":"response.completed","response":{"id":"r","model":"m","usage":{"input_tokens":5,` +
		`"input_tokens_details":{"cached_tokens":9},"output_tokens":1}}}` + "\n"
	var m Message
	_, _ = io.ReadAll(MeterResponses(io.NopCloser(strings.NewReader(weird)), "text/event-stream", "", func(x Message) { m = x }, nil))
	if m.Input != 0 || m.CacheRead != 5 {
		t.Fatalf("clamped usage %+v", m)
	}
}
