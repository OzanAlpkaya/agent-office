package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Process trees for the background wait state (ADR 0064): which processes an
// agent's CLI has started that are still running after its turn ended. Only
// /proc is read: pids, parents and the first two words of a command line,
// never more of it, and nothing is logged or stored.

// procInfo is one process.
type procInfo struct {
	ppid int
	exe  string // argv[0]'s base name
	arg1 string // argv[1], to tell agent-office's own helpers apart
}

// procReader reads the process table; tests replace it.
type procReader func() (map[int]procInfo, error)

// readProcs reads every process from /proc.
func readProcs() (map[int]procInfo, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make(map[int]procInfo, len(ents))
	for _, d := range ents {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", d.Name(), "stat"))
		if err != nil {
			continue // gone meanwhile
		}
		// pid (comm) state ppid ...: comm may hold spaces and parentheses.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) < 2 || f[0] == "Z" || f[0] == "X" {
			continue // a zombie or a dead process has ended
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		p := procInfo{ppid: ppid}
		if cl, err := os.ReadFile(filepath.Join("/proc", d.Name(), "cmdline")); err == nil {
			args := bytes.SplitN(cl, []byte{0}, 3)
			p.exe = filepath.Base(string(args[0]))
			if len(args) > 1 {
				p.arg1 = string(args[1])
			}
		}
		out[pid] = p
	}
	return out, nil
}

// descendants lists every process below root in tab (not root itself).
func descendants(tab map[int]procInfo, root int) map[int]procInfo {
	children := map[int][]int{}
	for pid, p := range tab {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	out := map[int]procInfo{}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if _, seen := out[c]; seen || c == root {
				continue
			}
			out[c] = tab[c]
			queue = append(queue, c)
		}
	}
	return out
}

// ownHelper reports whether p is agent-office's own helper in an agent's tree:
// the team MCP bridge or a hook forwarder.
func ownHelper(p procInfo, bin string) bool {
	return p.exe == bin && (p.arg1 == "mcp" || p.arg1 == "hook")
}

// backgroundProcs are the processes below root that were not there in
// baseline (taken when the background command started) and are not
// agent-office's own helpers: what the agent left running.
func backgroundProcs(tab map[int]procInfo, root int, baseline map[int]bool, bin string) []int {
	var out []int
	for pid, p := range descendants(tab, root) {
		if !baseline[pid] && !ownHelper(p, bin) {
			out = append(out, pid)
		}
	}
	return out
}
