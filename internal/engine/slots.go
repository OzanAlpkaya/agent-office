package engine

import (
	"fmt"
	"sync"
)

// slotPool is the daemon-wide agent cap across projects (ADR 0047). Each
// project engine reports how many agents it runs or is starting; a start
// reserves a slot only while the sum stays below the cap.
//
// Lock order: an engine calls the pool with its own lock held; the pool never
// calls into an engine, so two engines cannot deadlock through it, and two
// projects cannot both take the last slot (the check and the reservation are
// one critical section).
type slotPool struct {
	mu   sync.Mutex
	max  int
	used map[string]int // project id -> running + starting agents
}

func newSlotPool(max int) *slotPool { return &slotPool{max: max, used: map[string]int{}} }

// reserve takes a slot for project, whose engine counts local agents now
// (running and starting, before this one). It fails when the daemon-wide cap
// is reached and returns how many agents other projects hold.
func (p *slotPool) reserve(project string, local int) (others int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, n := range p.used {
		if id != project {
			others += n
		}
	}
	if others+local >= p.max {
		return others, false
	}
	p.used[project] = local + 1
	return others, true
}

// set records project's current count (after a start finished or failed, or
// an agent exited).
func (p *slotPool) set(project string, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n <= 0 {
		delete(p.used, project)
		return
	}
	p.used[project] = n
}

// cap is the daemon-wide cap now.
func (p *slotPool) cap() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

// setMax changes the daemon-wide cap (ADR 0061). Agents above a lowered cap
// keep running; starts wait until enough of them exit.
func (p *slotPool) setMax(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.max = n
}

// total is how many agents every project holds together.
func (p *slotPool) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.used {
		n += v
	}
	return n
}

// reserveSlotLocked checks the project's own cap and the daemon-wide one and
// takes a slot; the caller increments e.starting next, under the same lock.
func (e *Engine) reserveSlotLocked() error {
	local := e.runningLocked() + e.starting
	own, daemon := e.lv().MaxAgents, e.slots.cap()
	if local >= min(own, daemon) {
		if own < daemon {
			return fmt.Errorf("%w: this project's cap of %d agents is reached (the Tech Lead, Architect and QA count too)",
				ErrTooManyAgents, own)
		}
		if others := e.slots.total() - local; others > 0 {
			return fmt.Errorf("%w: the daemon-wide cap of %d agents is reached (%d run in other projects)",
				ErrTooManyAgents, daemon, others)
		}
		return fmt.Errorf("%w (%d; the Tech Lead, Architect and QA count too)", ErrTooManyAgents, daemon)
	}
	if others, ok := e.slots.reserve(e.project.ID, local); !ok {
		return fmt.Errorf("%w: the daemon-wide cap of %d agents is reached (%d run in other projects)",
			ErrTooManyAgents, daemon, others)
	}
	return nil
}

// syncSlotsLocked tells the pool how many agents this project holds now.
func (e *Engine) syncSlotsLocked() { e.slots.set(e.project.ID, e.runningLocked()+e.starting) }
