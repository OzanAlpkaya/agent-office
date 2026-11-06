package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dial(t *testing.T, ctx context.Context, srvURL, session string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws?session=" + session
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func send(t *testing.T, ctx context.Context, c *websocket.Conn, m clientMsg) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readUntil reads binary frames until the accumulated output contains want.
func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, want string) string {
	t.Helper()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; got so far: %q", want, err, out.String())
		}
		out.Write(data)
	}
	return out.String()
}

func TestRoundTripResizeReattachExit(t *testing.T) {
	ts := httptest.NewServer(newMux([]string{"bash", "--norc", "--noprofile"}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Input reaches the shell and output comes back. The command line
	// echoes "$((40+2))", so "hello-42" can only come from execution.
	c := dial(t, ctx, ts.URL, "t1")
	send(t, ctx, c, clientMsg{Type: "input", Data: "echo hello-$((40+2))\n"})
	readUntil(t, ctx, c, "hello-42")

	// 2. Resize propagates to the PTY (stty reports "rows cols").
	send(t, ctx, c, clientMsg{Type: "resize", Cols: 120, Rows: 40})
	send(t, ctx, c, clientMsg{Type: "input", Data: "stty size\n"})
	readUntil(t, ctx, c, "40 120")

	// 3. Detach: the process keeps running.
	c.Close(websocket.StatusNormalClosure, "")

	// 4. Re-attach: scrollback is replayed, and the same shell is still alive
	// (a shell variable set before detaching survives).
	send2 := dial(t, ctx, ts.URL, "t1")
	readUntil(t, ctx, send2, "hello-42")
	send(t, ctx, send2, clientMsg{Type: "input", Data: "X=kept; echo got-$X\n"})
	readUntil(t, ctx, send2, "got-kept")

	// 5. Process exit closes the socket with a normal closure.
	send(t, ctx, send2, clientMsg{Type: "input", Data: "exit\n"})
	for {
		_, _, err := send2.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("expected normal closure, got %v", err)
			}
			break
		}
	}
}

func TestCrossOriginRejected(t *testing.T) {
	ts := httptest.NewServer(newMux([]string{"bash", "--norc", "--noprofile"}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?session=x"
	_, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("cross-origin dial succeeded; expected rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", resp)
	}
}
