package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Exit reports (ADR 0051). The kernel's out-of-memory killer leaves no trace
// the daemon can rely on: Podman's State.OOMKilled stayed false for real OOM
// kills on rootless Podman 6.1.4 with runc and systemd cgroups, and the
// container's cgroup is removed within milliseconds of the kill. The agent's
// parent inside the container, sandbox-init, outlives it and can read its own
// cgroup's memory.events, so it writes what it saw into a file the daemon
// mounted for it. The file is written inside the sandbox, by a process the
// agent could imitate: it labels an exit and never grants anything.

// ExitReportFile is where sandbox-init writes its report inside a worker.
const (
	ExitReportFile   = runDir + "/exit.json"
	exitReportEnv    = "AGENT_OFFICE_EXIT_REPORT"
	maxExitReport    = 4096
	cgroupMemEvents  = "/sys/fs/cgroup/memory.events"
	cgroupMemMax     = "/sys/fs/cgroup/memory.max"
	maxMemoryEvents  = 4096
	memoryMaxUnbound = "max"
)

// ExitReport is what sandbox-init saw when the agent's process ended.
type ExitReport struct {
	Code      int    `json:"code"`                 // the agent's exit code; 128+n for signal n
	Signal    string `json:"signal,omitempty"`     // e.g. "KILL"; "" when it exited by itself
	OOMKills  int64  `json:"oom_kills"`            // the cgroup's oom_kill count since the worker started
	MemoryMax int64  `json:"memory_max,omitempty"` // the cgroup's limit in bytes; 0: none or unknown
}

var signalRe = regexp.MustCompile(`^[A-Z0-9]{1,12}$`)

// ReadExitReport reads and checks a report the daemon finds after the
// worker ended. A missing or empty file is no report (nil, nil): the worker
// may have died before sandbox-init could write it.
func ReadExitReport(path string) (*ExitReport, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxExitReport+1))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	if len(b) > maxExitReport {
		return nil, fmt.Errorf("sandbox: exit report larger than %d bytes", maxExitReport)
	}
	var r ExitReport
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("sandbox: exit report: %w", err)
	}
	if r.Code < 0 || r.Code > 255 || r.OOMKills < 0 || r.MemoryMax < 0 || (r.Signal != "" && !signalRe.MatchString(r.Signal)) {
		return nil, errors.New("sandbox: exit report out of range")
	}
	return &r, nil
}

// OOMKills reads the oom_kill count of a cgroup v2 memory.events file.
func OOMKills(memoryEvents string) (int64, error) {
	return cgroupEvent(memoryEvents, "oom_kill")
}

// PidsLimitHits reads the "max" count of a cgroup v2 pids.events file: how
// often a fork or clone failed because the cgroup reached pids.max (Phase 17).
func PidsLimitHits(pidsEvents string) (int64, error) {
	return cgroupEvent(pidsEvents, "max")
}

// cgroupEvent reads one "key count" line of a cgroup v2 events file.
func cgroupEvent(path, key string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxMemoryEvents))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok && k == key {
			return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("sandbox: no " + key + " in " + path)
}

// memoryMax reads a cgroup v2 memory.max file; 0 for "max" or an error.
func memoryMax(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	if s == memoryMaxUnbound {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// writeExitReport writes sandbox-init's report; best effort, since the
// worker is ending anyway.
func writeExitReport(path string, r ExitReport) {
	if path == "" {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// SignalName is the short name of signal number n ("KILL"), or its number.
func SignalName(n int) string {
	if s, ok := signalNames[n]; ok {
		return s
	}
	return strconv.Itoa(n)
}

var signalNames = map[int]string{1: "HUP", 2: "INT", 3: "QUIT", 4: "ILL", 6: "ABRT", 7: "BUS", 8: "FPE", 9: "KILL",
	11: "SEGV", 13: "PIPE", 14: "ALRM", 15: "TERM", 24: "XCPU", 25: "XFSZ"}

// SignalFromCode is the signal a shell-style exit code above 128 stands for
// ("KILL" for 137), or "".
func SignalFromCode(code int) string {
	if code > 128 && code < 160 {
		return SignalName(code - 128)
	}
	return ""
}
