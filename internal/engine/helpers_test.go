package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// updateLog keeps every update, so a wait also matches updates that arrived
// while the test was waiting for something else.
type updateLog struct {
	mu   sync.Mutex
	all  []Update
	more chan struct{}
}

func record(t *testing.T, e *Engine) *updateLog {
	t.Helper()
	updates, cancel := e.Subscribe()
	t.Cleanup(cancel)
	l := &updateLog{more: make(chan struct{}, 1)}
	go func() {
		for u := range updates {
			l.mu.Lock()
			l.all = append(l.all, u)
			l.mu.Unlock()
			select {
			case l.more <- struct{}{}:
			default:
			}
		}
	}()
	return l
}

func (l *updateLog) wait(t *testing.T, d time.Duration, match func(Update) bool) Update {
	t.Helper()
	return l.waitFrom(t, d, 0, match)
}

// mark is the number of updates recorded so far, for waitFrom.
func (l *updateLog) mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.all)
}

// waitFrom is wait, ignoring the first from updates: a state reached
// earlier in the test does not count.
func (l *updateLog) waitFrom(t *testing.T, d time.Duration, from int, match func(Update) bool) Update {
	t.Helper()
	timeout := time.After(d)
	for {
		l.mu.Lock()
		for _, u := range l.all[from:] {
			if match(u) {
				l.mu.Unlock()
				return u
			}
		}
		l.mu.Unlock()
		select {
		case <-l.more:
		case <-timeout:
			t.Fatal("timeout waiting for update")
		}
	}
}

func agentState(id string, st hooks.State) func(Update) bool {
	return func(u Update) bool { return u.Type == "agent" && u.Agent.ID == id && u.Agent.State == string(st) }
}

func screen(t *testing.T, e *Engine, id, want string) string {
	t.Helper()
	var last string
	for i := 0; i < 100; i++ {
		term, err := e.Terminal(id)
		if err != nil {
			t.Fatal(err)
		}
		snap, sub, _ := term.Attach()
		if sub != nil {
			sub.Close()
		}
		if last = strings.ReplaceAll(string(snap), "\r\n", "\n"); strings.Contains(last, want) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("terminal never showed %q:\n%s", want, last)
	return ""
}

// setLiveForTest changes a running engine's live settings in place of a
// project settings change (ADR 0061).
func (e *Engine) setLiveForTest(change func(*liveOptions)) {
	lv := *e.lv()
	change(&lv)
	e.live.Store(&lv)
}

// engineWithOptions is a bare engine for tests of option-derived values.
func engineWithOptions(t *testing.T, o Options) *Engine {
	t.Helper()
	lv, err := liveFrom(o)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{opts: o}
	e.live.Store(lv)
	return e
}
