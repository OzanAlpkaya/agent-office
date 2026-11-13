package engine

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// Phase 20 (ADR 0067): a turn cut off by a network drop waits for the
// network and is resumed, without the user, once the API answers again.
func TestResumeAfterNetworkLoss(t *testing.T) {
	oldEvery, oldAfter, oldMax := netProbeEvery, netResumeAfter, netMaxResumes
	netProbeEvery, netResumeAfter, netMaxResumes = 50*time.Millisecond, 10*time.Millisecond, 3
	t.Cleanup(func() { netProbeEvery, netResumeAfter, netMaxResumes = oldEvery, oldAfter, oldMax })
	var up atomic.Bool
	var probes atomic.Int32
	f := newFixture(t)
	f.opts.typingQuiet = 200 * time.Millisecond
	f.opts.enterDelay = 10 * time.Millisecond
	f.opts.netProbe = func(context.Context) error {
		probes.Add(1)
		if up.Load() {
			return nil
		}
		return errors.New("network is unreachable")
	}
	e := f.engine(t)
	log := record(t, e)
	a, err := e.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a.ID, hooks.Idle))

	// Other failures do not wait: a plain StopFailure stays idle.
	typeLine(t, e, a.ID, "stopfailure")
	time.Sleep(200 * time.Millisecond)
	if v, _ := e.Get(a.ID); v.State != string(hooks.Idle) {
		t.Fatalf("after another failure: %s", v.State)
	}

	mark := log.mark()
	typeLine(t, e, a.ID, "netlost")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.State(StateWaitingNetwork)))
	if v, _ := e.Get(a.ID); v.Activity != "connection lost" {
		t.Fatalf("activity %q", v.Activity)
	}
	// The network is still down: it keeps waiting and nothing is typed.
	time.Sleep(400 * time.Millisecond)
	if probes.Load() < 3 {
		t.Fatalf("probed %d times", probes.Load())
	}
	if v, _ := e.Get(a.ID); v.State != StateWaitingNetwork {
		t.Fatalf("while down: %s", v.State)
	}
	if strings.Contains(e.screenOf(a.ID), netResumeText) {
		t.Fatal("typed while the network is down")
	}
	// Back: the resume line is typed, once.
	up.Store(true)
	screen(t, e, a.ID, "INPUT:"+netResumeText)
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.Idle))
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(e.screenOf(a.ID), "INPUT:"+netResumeText); n != 1 {
		t.Fatalf("typed %d times", n)
	}

	// The user (or the CLI) moving it on first ends the wait without typing.
	up.Store(false)
	mark = log.mark()
	typeLine(t, e, a.ID, "netlost")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.State(StateWaitingNetwork)))
	typeLine(t, e, a.ID, "busy")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.Working))
	up.Store(true)
	time.Sleep(400 * time.Millisecond)
	if n := strings.Count(e.screenOf(a.ID), "INPUT:"+netResumeText); n != 1 {
		t.Fatalf("typed after the user moved it on: %d", n)
	}
	typeLine(t, e, a.ID, "stop")

	// At most netMaxResumes an hour: then it stays idle and the user is told.
	for i := 0; i < netMaxResumes-1; i++ {
		typeLine(t, e, a.ID, "netlost")
		want := i + 2
		deadline := time.Now().Add(5 * time.Second)
		for strings.Count(e.screenOf(a.ID), "INPUT:"+netResumeText) < want {
			if time.Now().After(deadline) {
				t.Fatalf("resume %d was not typed", want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	typeLine(t, e, a.ID, "netlost")
	time.Sleep(300 * time.Millisecond)
	if v, _ := e.Get(a.ID); v.State != string(hooks.Idle) {
		t.Fatalf("over the limit: %s", v.State)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		items, _ := e.Inbox(context.Background())
		if len(items) > 0 && strings.Contains(items[0].Body, "lost the API connection") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no notice: %+v", items)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// screenOf is an agent's terminal so far.
func (e *Engine) screenOf(id string) string {
	term, err := e.Terminal(id)
	if err != nil {
		return ""
	}
	snap, sub, _ := term.Attach()
	if sub != nil {
		sub.Close()
	}
	return strings.ReplaceAll(string(snap), "\r\n", "\n")
}
