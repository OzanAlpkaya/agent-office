// Package auth authenticates the UI and API with paired devices (ADR 0039).
//
// A device (a browser, or a script) is paired once with a short-lived,
// single-use pairing code and then holds a random token. Only the token's
// SHA-256 is stored; the token itself is never logged or kept in clear. Codes
// live in memory only. Revoking a device deletes it and cancels the contexts
// of its open connections (Watch).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

const (
	// CodeTTL is how long a pairing code can be used.
	CodeTTL = 5 * time.Minute
	// MaxCodes is how many unused pairing codes may exist at once.
	MaxCodes = 5
	// MaxFailures wrong codes within FailureWindow drop every open code and
	// refuse pairing until the window has passed.
	MaxFailures   = 10
	FailureWindow = 10 * time.Minute
	// IdleExpiry ends a device that has not been used for this long.
	IdleExpiry = 30 * 24 * time.Hour
	// MaxNameLen is the longest device name, in characters.
	MaxNameLen = 60

	// TokenPrefix starts every device token, so secret scanners can find one.
	TokenPrefix = "aod_"

	// Kinds of devices.
	KindBrowser = "browser"
	KindToken   = "token"

	touchEvery = time.Minute
	codeLen    = 8
	// Crockford's base32: no I, L, O or U.
	codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	ErrInvalidCode     = errors.New("the pairing code is wrong or has expired")
	ErrTooManyAttempts = errors.New("too many wrong pairing codes: every open code was dropped; try again in a few minutes with a new code")
	ErrTooManyCodes    = errors.New("too many open pairing codes: use one or wait until they expire (5 minutes)")
	ErrInvalidName     = errors.New("a device name needs 1 to 60 printable characters")
	ErrNotFound        = errors.New("no such device")
	// ErrRevoked is the cause of a watched context whose device was revoked.
	ErrRevoked = errors.New("this device was signed out")
)

// Store is the persistence the manager needs; *db.Queries implements it.
type Store interface {
	CreateDevice(ctx context.Context, arg db.CreateDeviceParams) error
	ListDevices(ctx context.Context) ([]db.Device, error)
	TouchDevice(ctx context.Context, arg db.TouchDeviceParams) error
	DeleteDevice(ctx context.Context, id string) (int64, error)
}

// Device is a paired device as the API shows it. It never carries the token.
type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`       // browser | token
	PairedVia  string    `json:"paired_via"` // "local", or "tailscale:<login>"
	PairedBy   string    `json:"paired_by"`  // the device whose code paired it; "" = agent-office pair
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Code is a pairing code and when it stops working.
type Code struct {
	Value     string    `json:"code"` // ABCD-EFGH
	ExpiresAt time.Time `json:"expires_at"`
}

// Options configures a Manager.
type Options struct {
	Logger *slog.Logger
	Now    func() time.Time // for tests; default time.Now
}

type entry struct {
	dev     Device
	touched time.Time // last time last_seen_at was written
}

type code struct {
	value   string // normalized, without the dash
	expires time.Time
	by      string // the device that created it; "" = the control socket
}

// Manager holds the paired devices and the open pairing codes.
type Manager struct {
	st  Store
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	byHash   map[string]*entry
	codes    []code
	failures []time.Time
	watchers map[string]map[int]context.CancelCauseFunc
	nextID   int
}

// New loads the paired devices and deletes those idle for longer than IdleExpiry.
func New(ctx context.Context, st Store, opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Manager{st: st, log: opts.Logger, now: opts.Now, byHash: map[string]*entry{},
		watchers: map[string]map[int]context.CancelCauseFunc{}}
	rows, err := st.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: load devices: %w", err)
	}
	now := m.now()
	for _, r := range rows {
		if now.Sub(r.LastSeenAt) > IdleExpiry {
			if _, err := st.DeleteDevice(ctx, r.ID); err != nil {
				return nil, fmt.Errorf("auth: delete expired device: %w", err)
			}
			m.log.Info("paired device expired", "device_id", r.ID, "last_seen", r.LastSeenAt)
			continue
		}
		m.byHash[r.TokenHash] = &entry{dev: fromRow(r), touched: r.LastSeenAt}
	}
	return m, nil
}

func fromRow(r db.Device) Device {
	return Device{ID: r.ID, Name: r.Name, Kind: r.Kind, PairedVia: r.PairedVia, PairedBy: r.PairedBy, CreatedAt: r.CreatedAt.UTC(),
		LastSeenAt: r.LastSeenAt.UTC()}
}

// NewCode creates a pairing code. by is the paired device asking for it, or
// "" for the control socket; devices paired with the code are revoked with it.
func (m *Manager) NewCode(by string) (Code, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	if len(m.codes) >= MaxCodes {
		return Code{}, ErrTooManyCodes
	}
	var b [5]byte // 40 bits, 8 symbols of 5 bits
	if _, err := rand.Read(b[:]); err != nil {
		return Code{}, err
	}
	bits := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	v := make([]byte, codeLen)
	for i := codeLen - 1; i >= 0; i-- {
		v[i] = codeAlphabet[bits&31]
		bits >>= 5
	}
	c := code{value: string(v), expires: now.Add(CodeTTL), by: by}
	m.codes = append(m.codes, c)
	return Code{Value: c.value[:4] + "-" + c.value[4:], ExpiresAt: c.expires.UTC()}, nil
}

// NormalizeCode turns what a person typed into a code's canonical form:
// upper case, no dashes or spaces, O as 0 and I or L as 1. It returns ""
// for anything that cannot be a code.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		}
		if r > unicode.MaxASCII || !strings.ContainsRune(codeAlphabet, r) {
			return ""
		}
		b.WriteRune(r)
		if b.Len() > codeLen {
			return ""
		}
	}
	if b.Len() != codeLen {
		return ""
	}
	return b.String()
}

// pruneLocked drops expired codes and failures outside the window.
func (m *Manager) pruneLocked(now time.Time) {
	codes := m.codes[:0]
	for _, c := range m.codes {
		if now.Before(c.expires) {
			codes = append(codes, c)
		}
	}
	m.codes = codes
	fails := m.failures[:0]
	for _, f := range m.failures {
		if now.Sub(f) < FailureWindow {
			fails = append(fails, f)
		}
	}
	m.failures = fails
}

// Pair uses a code to pair a browser named name; via says where the request
// came from. It returns the new device's token, to be set as its cookie.
func (m *Manager) Pair(ctx context.Context, rawCode, name, via string) (string, Device, error) {
	name, err := CleanName(name)
	if err != nil {
		return "", Device{}, err
	}
	by, err := m.useCode(rawCode)
	if err != nil {
		return "", Device{}, err
	}
	return m.create(ctx, name, KindBrowser, via, by)
}

// useCode consumes a code and returns the device that created it.
func (m *Manager) useCode(raw string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	if len(m.failures) >= MaxFailures {
		return "", ErrTooManyAttempts
	}
	want := NormalizeCode(raw)
	found := -1
	for i, c := range m.codes {
		// Compare against every code, so the time taken does not say which matched.
		if subtle.ConstantTimeCompare([]byte(c.value), []byte(want)) == 1 && want != "" {
			found = i
		}
	}
	if found < 0 {
		m.failures = append(m.failures, now)
		if len(m.failures) >= MaxFailures {
			m.codes = nil
			m.log.Warn("pairing refused after too many wrong codes; open codes dropped", "failures", len(m.failures))
			return "", ErrTooManyAttempts
		}
		return "", ErrInvalidCode
	}
	by := m.codes[found].by
	m.codes = append(m.codes[:found], m.codes[found+1:]...) // single use
	return by, nil
}

// CreateToken creates a device for a script: a bearer token without a
// pairing code. Only the control socket offers it (local user only).
func (m *Manager) CreateToken(ctx context.Context, name string) (string, Device, error) {
	name, err := CleanName(name)
	if err != nil {
		return "", Device{}, err
	}
	return m.create(ctx, name, KindToken, "local", "")
}

func (m *Manager) create(ctx context.Context, name, kind, via, by string) (string, Device, error) {
	var tb [32]byte
	if _, err := rand.Read(tb[:]); err != nil {
		return "", Device{}, err
	}
	var ib [8]byte
	if _, err := rand.Read(ib[:]); err != nil {
		return "", Device{}, err
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(tb[:])
	hash := HashToken(token)
	now := m.now().UTC()
	via = cleanVia(via)
	d := Device{ID: hex.EncodeToString(ib[:]), Name: name, Kind: kind, PairedVia: via, PairedBy: by, CreatedAt: now, LastSeenAt: now}
	m.mu.Lock()
	// A code whose device was revoked meanwhile pairs nothing.
	if by != "" && m.idLocked(by) == nil {
		m.mu.Unlock()
		return "", Device{}, ErrInvalidCode
	}
	m.mu.Unlock()
	if err := m.st.CreateDevice(ctx, db.CreateDeviceParams{ID: d.ID, Name: d.Name, Kind: d.Kind, TokenHash: hash,
		PairedVia: d.PairedVia, PairedBy: by, CreatedAt: now, LastSeenAt: now}); err != nil {
		return "", Device{}, fmt.Errorf("auth: store device: %w", err)
	}
	m.mu.Lock()
	m.byHash[hash] = &entry{dev: d, touched: now}
	m.mu.Unlock()
	// The Tailscale login is personal data: the log says only how the device was paired.
	how, _, _ := strings.Cut(via, ":")
	m.log.Info("device paired", "device_id", d.ID, "kind", kind, "via", how, "paired_by", by)
	return token, d, nil
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Authenticate returns the device a token belongs to. It refreshes the
// device's last use at most once a minute and ends a device idle for longer
// than IdleExpiry.
func (m *Manager) Authenticate(ctx context.Context, token string) (Device, bool) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 128 {
		return Device{}, false
	}
	hash := HashToken(token)
	now := m.now()
	m.mu.Lock()
	e, ok := m.byHash[hash]
	if !ok {
		m.mu.Unlock()
		return Device{}, false
	}
	if now.Sub(e.dev.LastSeenAt) > IdleExpiry {
		m.mu.Unlock()
		// Only this device: the ones it paired were used more recently.
		if err := m.revoke(ctx, []string{e.dev.ID}); err != nil {
			m.log.Error("delete expired device", "device_id", e.dev.ID, "err", err)
		}
		return Device{}, false
	}
	e.dev.LastSeenAt = now.UTC()
	touch := now.Sub(e.touched) >= touchEvery
	if touch {
		e.touched = now
	}
	d := e.dev
	m.mu.Unlock()
	if touch {
		if err := m.st.TouchDevice(ctx, db.TouchDeviceParams{LastSeenAt: now.UTC(), ID: d.ID}); err != nil {
			m.log.Warn("record device use", "device_id", d.ID, "err", err)
		}
	}
	return d, true
}

// Devices lists the paired devices, oldest first.
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.byHash))
	for _, e := range m.byHash {
		out = append(out, e.dev)
	}
	slices.SortFunc(out, func(a, b Device) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Revoke deletes a device, every device paired with a code it created (and
// theirs), and the codes it created, and cancels their open connections.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	m.mu.Lock()
	ids := m.lineageLocked(id)
	m.mu.Unlock()
	if len(ids) == 0 {
		// Not known in memory; a row may still exist (it cannot authenticate anyway).
		n, err := m.st.DeleteDevice(ctx, id)
		if err != nil {
			return fmt.Errorf("auth: delete device: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}
	return m.revoke(ctx, ids)
}

// RevokeOthers signs out every device except keep. It returns how many.
func (m *Manager) RevokeOthers(ctx context.Context, keep string) (int, error) {
	m.mu.Lock()
	var ids []string
	for _, e := range m.byHash {
		if e.dev.ID != keep {
			ids = append(ids, e.dev.ID)
		}
	}
	m.mu.Unlock()
	return len(ids), m.revoke(ctx, ids)
}

func (m *Manager) revoke(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if _, err := m.st.DeleteDevice(ctx, id); err != nil {
			return fmt.Errorf("auth: delete device: %w", err)
		}
	}
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	var cancels []context.CancelCauseFunc
	m.mu.Lock()
	for h, e := range m.byHash {
		if gone[e.dev.ID] {
			delete(m.byHash, h)
		}
	}
	codes := m.codes[:0]
	for _, c := range m.codes {
		if !gone[c.by] {
			codes = append(codes, c)
		}
	}
	m.codes = codes
	for id := range gone {
		for _, cancel := range m.watchers[id] {
			cancels = append(cancels, cancel)
		}
		delete(m.watchers, id)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel(ErrRevoked)
	}
	for _, id := range ids {
		m.log.Info("device signed out", "device_id", id)
	}
	return nil
}

// lineageLocked is id and every device paired, directly or not, through it.
func (m *Manager) lineageLocked(id string) []string {
	if m.idLocked(id) == nil {
		return nil
	}
	out := []string{id}
	seen := map[string]bool{id: true}
	for i := 0; i < len(out); i++ {
		for _, e := range m.byHash {
			if e.dev.PairedBy == out[i] && !seen[e.dev.ID] {
				seen[e.dev.ID] = true
				out = append(out, e.dev.ID)
			}
		}
	}
	return out
}

func (m *Manager) idLocked(id string) *entry {
	for _, e := range m.byHash {
		if e.dev.ID == id {
			return e
		}
	}
	return nil
}

// Watch returns a context that is cancelled (cause ErrRevoked) when the
// device is revoked, for long-lived connections such as WebSockets. Call the
// returned function when the connection ends.
func (m *Manager) Watch(parent context.Context, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	m.mu.Lock()
	if m.idLocked(id) == nil {
		m.mu.Unlock()
		cancel(ErrRevoked)
		return ctx, func() {}
	}
	m.nextID++
	n := m.nextID
	if m.watchers[id] == nil {
		m.watchers[id] = map[int]context.CancelCauseFunc{}
	}
	m.watchers[id][n] = cancel
	m.mu.Unlock()
	return ctx, func() {
		m.mu.Lock()
		delete(m.watchers[id], n)
		if len(m.watchers[id]) == 0 {
			delete(m.watchers, id)
		}
		m.mu.Unlock()
		cancel(context.Canceled)
	}
}

// CleanName trims a device name, turns runs of whitespace and control
// characters into one space and checks its length.
func CleanName(s string) (string, error) {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if n := utf8.RuneCountInString(out); n == 0 || n > MaxNameLen {
		return "", ErrInvalidName
	}
	return out, nil
}

// cleanVia keeps "local" or "tailscale:<login>" short and printable.
func cleanVia(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > unicode.MaxASCII {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 200 {
			break
		}
	}
	return b.String()
}
