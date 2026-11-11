package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// The running memory watch (ADR 0051): while a sandboxed agent runs, the
// daemon reads its worker cgroup's oom_kill count. An OOM kill of one of the
// agent's processes (a go test, a build) leaves the agent running with a
// "Killed" it cannot explain; the watch tells it and the UI why. The agent's
// own death is reported by its exit (sandbox-init's exit report), since the
// cgroup is gone by then. The same watch reads pids.events: a fork refused at
// the worker's process limit ("Resource temporarily unavailable" in a test
// suite) is told the same way (Phase 17).

// memWatchEvery is how often a worker's memory events are read; tests shorten it.
var memWatchEvery = 3 * time.Second

// cgroupRoot is where the host's cgroup v2 hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// watchMemory runs until the agent exits.
func (e *Engine) watchMemory(a *agentProc, res *sandboxRes) {
	defer e.wg.Done()
	log := a.logger(e)
	var events string
	for i := 0; i < 60 && events == ""; i++ { // podman knows the cgroup once the container runs
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cg, err := e.opts.Sandbox.Podman.CgroupPath(ctx, res.container)
		cancel()
		if err == nil && strings.HasPrefix(cg, "/") && !strings.Contains(cg, "..") {
			events = filepath.Join(cgroupRoot, cg, "memory.events")
			break
		}
		select {
		case <-a.exited:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	if events == "" {
		log.Debug("memory watch: no cgroup for the worker")
		return
	}
	t := time.NewTicker(memWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-a.exited:
			return
		case <-t.C:
		}
		if hits, err := sandbox.PidsLimitHits(filepath.Join(filepath.Dir(events), "pids.events")); err == nil {
			if prev := res.pidsHits.Swap(hits); hits > prev {
				e.pidsLimitHit(a, res, hits-prev)
			}
		}
		n, err := sandbox.OOMKills(events)
		if err != nil {
			return // the worker ended, or this host's cgroups look different
		}
		if prev := res.oomKills.Swap(n); n > prev {
			// If the kill was the agent itself, its exit follows within moments
			// and says so (ADR 0051): wait for that before telling it anything.
			select {
			case <-a.exited:
				return
			case <-time.After(2 * time.Second):
			}
			res.oomTold.Add(n - prev)
			e.oomKilled(a, res, n-prev)
		}
	}
}

// oomKilled reports OOM kills inside a running worker.
func (e *Engine) oomKilled(a *agentProc, res *sandboxRes, n int64) {
	log := a.logger(e)
	log.Warn("out-of-memory kill in the worker", "count", n, "memory", res.memory)
	e.sandboxEvent(a.info.ID, sandbox.Event{Kind: sandbox.OOMKill, Detail: fmt.Sprintf("%d process(es), limit %s", n, res.memory),
		At: now()})
	body := fmt.Sprintf("The out-of-memory killer ended %d process(es) in your sandbox: it reached its %s memory limit. "+
		"A command you ran that printed \"Killed\" or exited with signal 9 ran out of memory. Use less memory (for example "+
		"go test -p 1, fewer packages or workers at once) and run it again; if the task cannot fit, tell your lead.", n, res.memory)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.sendMessage(ctx, "", a.info.ID, body, false); err != nil {
		log.Warn("tell the agent about an OOM kill", "err", err)
	}
}

// pidsLimitHit reports forks refused at the worker's process limit.
func (e *Engine) pidsLimitHit(a *agentProc, res *sandboxRes, n int64) {
	log := a.logger(e)
	log.Warn("process limit reached in the worker", "refused_forks", n, "pids", res.pids)
	e.sandboxEvent(a.info.ID, sandbox.Event{Kind: sandbox.PidsLimit,
		Detail: fmt.Sprintf("%d process start(s) refused, limit %d", n, res.pids), At: now()})
	body := fmt.Sprintf("Your sandbox reached its limit of %d processes and threads: %d process start(s) failed "+
		"(\"Resource temporarily unavailable\", \"fork\" or \"pthread_create\" errors). Run fewer at once (for example "+
		"go test -p 1 or fewer parallel jobs) and stop background processes you do not need. If the task needs more, tell "+
		"your lead: the user can raise worker_pids in the project's settings or resources.pids in the stack pack.", res.pids, n)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.sendMessage(ctx, "", a.info.ID, body, false); err != nil {
		log.Warn("tell the agent about the process limit", "err", err)
	}
}
