package engine

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// A turn that ends while its background command runs shows "waiting on
// background task", not idle, until the command ends (ADR 0064).
func TestWaitingOnBackgroundTask(t *testing.T) {
	old := bgPollEvery
	bgPollEvery = 100 * time.Millisecond
	t.Cleanup(func() { bgPollEvery = old })
	f := newFixture(t, "AO_FAKE_BG_SECONDS=2")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	a, err := e.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a.ID, hooks.Idle))
	// A plain turn stays idle: no background command was started.
	typeLine(t, e, a.ID, "turn")
	time.Sleep(300 * time.Millisecond)
	if v, _ := e.Get(a.ID); v.State != string(hooks.Idle) {
		t.Fatalf("after a plain turn: %s", v.State)
	}
	mark := log.mark()
	typeLine(t, e, a.ID, "bgturn")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.State(StateWaitingBackground)))
	if v, _ := e.Get(a.ID); v.Activity != "background task" {
		t.Fatalf("activity %q", v.Activity)
	}
	screen(t, e, a.ID, "bg done")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.Idle))
	// The run info counts the wait as waiting time.
	evs, _ := e.q.ListAgentStateEvents(ctx, a.ID)
	if !slices.ContainsFunc(evs, func(r db.ListAgentStateEventsRow) bool { return r.State == StateWaitingBackground }) {
		t.Fatal("the wait was not recorded")
	}
	// A new turn while it waits moves it on at once.
	mark = log.mark()
	typeLine(t, e, a.ID, "bgturn")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.State(StateWaitingBackground)))
	typeLine(t, e, a.ID, "busy")
	log.waitFrom(t, 5*time.Second, mark, agentState(a.ID, hooks.Working))
}

func TestBackgroundProcs(t *testing.T) {
	tab := map[int]procInfo{
		10: {ppid: 1, exe: "claude"},
		11: {ppid: 10, exe: "agent-office", arg1: "mcp"},
		12: {ppid: 10, exe: "node"}, // an MCP server that ran before
		13: {ppid: 10, exe: "bash"},
		14: {ppid: 13, exe: "sleep"},
		15: {ppid: 10, exe: "agent-office", arg1: "hook"},
		20: {ppid: 1, exe: "other"},
	}
	if d := descendants(tab, 10); len(d) != 5 {
		t.Fatalf("descendants %v", d)
	}
	got := backgroundProcs(tab, 10, map[int]bool{11: true, 12: true}, "agent-office")
	slices.Sort(got)
	if !slices.Equal(got, []int{13, 14}) {
		t.Fatalf("background %v", got)
	}
	if got := backgroundProcs(tab, 10, map[int]bool{12: true, 13: true, 14: true}, "agent-office"); len(got) != 0 {
		t.Fatalf("nothing new: %v", got)
	}
}

// readProcs sees a child of this test and its parent, and not a reaped one.
func TestReadProcs(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tab, err := readProcs()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := tab[cmd.Process.Pid]
	if !ok || p.ppid != os.Getpid() || p.exe != "sleep" || p.arg1 != "5" {
		t.Fatalf("child %+v %v", p, ok)
	}
	if _, ok := descendants(tab, os.Getpid())[cmd.Process.Pid]; !ok {
		t.Fatal("not a descendant")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	tab, _ = readProcs()
	if _, ok := tab[cmd.Process.Pid]; ok {
		t.Fatal("a reaped child is still listed")
	}
}
