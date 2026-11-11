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
// cgroup is gone by then.

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
		n, err := sandbox.OOMKills(events)
		if err != nil {
			return // the worker ended, or this host's cgroups look different
		}
		if prev := res.oomKills.Swap(n); n > prev {
			select {
			case <-a.exited: // the agent itself died: its exit says so (ADR 0051)
				return
			default:
			}
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
