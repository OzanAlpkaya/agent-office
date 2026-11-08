package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newManager(t *testing.T) (*Manager, *store.Store, *clock) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	m, err := New(context.Background(), st.Q, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return m, st, c
}

func TestPairWithCode(t *testing.T) {
	ctx := context.Background()
	m, st, c := newManager(t)
	code, err := m.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Value) != 9 || code.Value[4] != '-' || !code.ExpiresAt.Equal(c.now().Add(CodeTTL)) {
		t.Fatalf("code %+v", code)
	}
	// Typed in lower case, without the dash, with spaces.
	typed := strings.ToLower(strings.ReplaceAll(code.Value, "-", " "))
	token, d, err := m.Pair(ctx, typed, "  Pixel\tphone  ", "tailscale:ozan@github")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Pixel phone" || d.Kind != KindBrowser || d.PairedVia != "tailscale:ozan@github" {
		t.Fatalf("device %+v", d)
	}
	if !strings.HasPrefix(token, TokenPrefix) || len(token) < 40 {
		t.Fatalf("token shape: %d chars", len(token))
	}
	// Single use.
	if _, _, err := m.Pair(ctx, code.Value, "again", "local"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second use: %v", err)
	}
	got, ok := m.Authenticate(ctx, token)
	if !ok || got.ID != d.ID {
		t.Fatalf("authenticate: %v %+v", ok, got)
	}
	if _, ok := m.Authenticate(ctx, token+"x"); ok {
		t.Fatal("a different token authenticated")
	}
	// Only the hash is stored.
	rows, err := st.Q.ListDevices(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v %v", rows, err)
	}
	if rows[0].TokenHash != HashToken(token) || strings.Contains(rows[0].TokenHash, token[len(TokenPrefix):]) {
		t.Fatal("stored token is not its hash")
	}
	// A new manager (daemon restart) still knows the device.
	m2, err := New(ctx, st.Q, Options{Logger: m.log, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Authenticate(ctx, token); !ok {
		t.Fatal("device lost across a restart")
	}
}

func TestCodesExpireAndAreLimited(t *testing.T) {
	ctx := context.Background()
	m, _, c := newManager(t)
	first, _ := m.NewCode()
	for i := 1; i < MaxCodes; i++ {
		if _, err := m.NewCode(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.NewCode(); !errors.Is(err, ErrTooManyCodes) {
		t.Fatalf("code %d: %v", MaxCodes+1, err)
	}
	c.add(CodeTTL)
	if _, _, err := m.Pair(ctx, first.Value, "late", "local"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code: %v", err)
	}
	if _, err := m.NewCode(); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestWrongCodesLockPairing(t *testing.T) {
	ctx := context.Background()
	m, _, c := newManager(t)
	good, _ := m.NewCode()
	for i := 1; i < MaxFailures; i++ {
		if _, _, err := m.Pair(ctx, "0000-0000", "x", "local"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := m.Pair(ctx, "0000-0000", "x", "local"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("attempt %d: %v", MaxFailures, err)
	}
	// Every open code was dropped, and pairing stays refused within the window.
	if _, _, err := m.Pair(ctx, good.Value, "x", "local"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("good code during lockout: %v", err)
	}
	c.add(FailureWindow)
	if _, _, err := m.Pair(ctx, good.Value, "x", "local"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("dropped code after lockout: %v", err)
	}
	fresh, _ := m.NewCode()
	if _, _, err := m.Pair(ctx, fresh.Value, "x", "local"); err != nil {
		t.Fatalf("new code after lockout: %v", err)
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{
		"ABCD-EFGH": "ABCDEFGH", "abcd efgh": "ABCDEFGH", "O1IL-0000": "01110000", "ABCD-EFG": "",
		"ABCD-EFGHJ": "", "ABCU-EFGH": "", "ÄBCD-EFGH": "", "": "", "ABCD\x00EFGH": "",
	} {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Pixel 8": "Pixel 8", "  a \x1b[31m b ": "a [31m b", "Ozan’s iPhone": "Ozan’s iPhone",
	} {
		if got, err := CleanName(in); err != nil || got != want {
			t.Errorf("CleanName(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "  \t", strings.Repeat("x", MaxNameLen+1)} {
		if _, err := CleanName(in); !errors.Is(err, ErrInvalidName) {
			t.Errorf("CleanName(%q): %v", in, err)
		}
	}
}

func TestIdleDevicesExpire(t *testing.T) {
	ctx := context.Background()
	m, st, c := newManager(t)
	token, d, err := m.CreateToken(ctx, "e2e")
	if err != nil || d.Kind != KindToken {
		t.Fatal(d, err)
	}
	c.add(IdleExpiry - time.Hour)
	if _, ok := m.Authenticate(ctx, token); !ok {
		t.Fatal("expired too early")
	}
	// Use refreshed it, in the database too.
	rows, _ := st.Q.ListDevices(ctx)
	if !rows[0].LastSeenAt.Equal(c.now()) {
		t.Fatalf("last seen %v, want %v", rows[0].LastSeenAt, c.now())
	}
	c.add(IdleExpiry + time.Second)
	if _, ok := m.Authenticate(ctx, token); ok {
		t.Fatal("idle device still works")
	}
	if rows, _ := st.Q.ListDevices(ctx); len(rows) != 0 {
		t.Fatal("expired device not deleted")
	}
	// Expired devices are dropped when the daemon starts, too.
	token2, _, _ := m.CreateToken(ctx, "old")
	c.add(IdleExpiry + time.Second)
	m2, err := New(ctx, st.Q, Options{Logger: m.log, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Authenticate(ctx, token2); ok || len(m2.Devices()) != 0 {
		t.Fatal("expired device loaded")
	}
}

func TestTouchIsThrottled(t *testing.T) {
	ctx := context.Background()
	m, st, c := newManager(t)
	token, _, _ := m.CreateToken(ctx, "t")
	created := c.now()
	c.add(30 * time.Second)
	m.Authenticate(ctx, token)
	if rows, _ := st.Q.ListDevices(ctx); !rows[0].LastSeenAt.Equal(created) {
		t.Fatal("wrote last seen within a minute")
	}
	if d := m.Devices()[0]; !d.LastSeenAt.Equal(c.now()) {
		t.Fatalf("in-memory last seen %v", d.LastSeenAt)
	}
	c.add(time.Minute)
	m.Authenticate(ctx, token)
	if rows, _ := st.Q.ListDevices(ctx); !rows[0].LastSeenAt.Equal(c.now()) {
		t.Fatal("last seen not written after a minute")
	}
}

func TestRevokeCancelsWatchers(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	token, d, _ := m.CreateToken(ctx, "phone")
	_, other, _ := m.CreateToken(ctx, "laptop")
	wctx, done := m.Watch(ctx, d.ID)
	defer done()
	octx, odone := m.Watch(ctx, other.ID)
	defer odone()
	if err := m.Revoke(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wctx.Done():
		if !errors.Is(context.Cause(wctx), ErrRevoked) {
			t.Fatalf("cause %v", context.Cause(wctx))
		}
	case <-time.After(time.Second):
		t.Fatal("watcher not cancelled")
	}
	if octx.Err() != nil {
		t.Fatal("another device's watcher was cancelled")
	}
	if _, ok := m.Authenticate(ctx, token); ok {
		t.Fatal("revoked token works")
	}
	if err := m.Revoke(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	// Watching an unknown device gives a cancelled context.
	uctx, _ := m.Watch(ctx, d.ID)
	if uctx.Err() == nil {
		t.Fatal("watch of a revoked device is live")
	}
	if ds := m.Devices(); len(ds) != 1 || ds[0].ID != other.ID {
		t.Fatalf("devices %+v", ds)
	}
}
