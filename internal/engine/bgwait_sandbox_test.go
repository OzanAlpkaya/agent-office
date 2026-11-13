//go:build sandbox

package engine

import (
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// In a real worker container the background check reads the container's
// cgroup: a command left running after the turn shows as waiting on a
// background task until it ends (ADR 0064).
func TestSandboxedWaitingOnBackgroundTask(t *testing.T) {
	old, oldMem := bgPollEvery, memWatchEvery
	bgPollEvery, memWatchEvery = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { bgPollEvery, memWatchEvery = old, oldMem })
	f := sandboxFixture(t, false)
	e := f.engine(t)
	log := record(t, e)
	a := startIdle(t, e, log)
	time.Sleep(2 * time.Second) // the memory watch found the worker's cgroup
	mark := log.mark()
	typeLine(t, e, a.ID, "bgturn")
	log.waitFrom(t, 20*time.Second, mark, agentState(a.ID, hooks.State(StateWaitingBackground)))
	screen(t, e, a.ID, "bg done")
	log.waitFrom(t, 20*time.Second, mark, agentState(a.ID, hooks.Idle))
}
