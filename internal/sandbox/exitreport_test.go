package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitWritesExitReport: sandbox-init reports how its child ended and the
// worker cgroup's OOM kills and limit (ADR 0051), here with fake cgroup files.
func TestInitWritesExitReport(t *testing.T) {
	dir := t.TempDir()
	events, max := filepath.Join(dir, "memory.events"), filepath.Join(dir, "memory.max")
	if err := os.WriteFile(events, []byte("low 0\nhigh 0\nmax 12\noom 1\noom_kill 1\noom_group_kill 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(max, []byte("134217728\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, script, signal string
		code                 int
	}{
		{"killed", "kill -KILL $$", "KILL", 137},
		{"exit code", "exit 3", "", 3},
		{"exit 137 by itself", "exit 137", "", 137},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := initCfg(t, "sh", "-c", tc.script)
			cfg.ExitReport, cfg.MemoryEvents, cfg.MemoryMax = filepath.Join(t.TempDir(), "exit.json"), events, max
			code, err := RunInit(context.Background(), cfg)
			if err != nil || code != tc.code {
				t.Fatalf("RunInit = %d, %v; want %d", code, err, tc.code)
			}
			r, err := ReadExitReport(cfg.ExitReport)
			if err != nil || r == nil {
				t.Fatalf("ReadExitReport = %+v, %v", r, err)
			}
			want := ExitReport{Code: tc.code, Signal: tc.signal, OOMKills: 1, MemoryMax: 134217728}
			if *r != want {
				t.Fatalf("report = %+v, want %+v", *r, want)
			}
		})
	}
}

func TestReadExitReportRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if r, err := ReadExitReport(filepath.Join(dir, "missing")); r != nil || err != nil {
		t.Fatalf("missing file = %+v, %v; want no report", r, err)
	}
	for name, body := range map[string]string{
		"unknown field": `{"code":1,"oom_kills":0,"extra":1}`,
		"bad signal":    `{"code":1,"signal":"kill; rm","oom_kills":0}`,
		"negative":      `{"code":-1,"oom_kills":0}`,
		"too large":     `{"code":1,"signal":"` + strings.Repeat("A", 5000) + `"}`,
	} {
		p := filepath.Join(dir, "r.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if r, err := ReadExitReport(p); err == nil {
			t.Errorf("%s: accepted %+v", name, r)
		}
	}
	if SignalFromCode(137) != "KILL" || SignalFromCode(143) != "TERM" || SignalFromCode(1) != "" {
		t.Fatal("SignalFromCode")
	}
}

func TestCgroupEventCounts(t *testing.T) {
	dir := t.TempDir()
	mem := filepath.Join(dir, "memory.events")
	pids := filepath.Join(dir, "pids.events")
	if err := os.WriteFile(mem, []byte("low 0\nhigh 0\nmax 4\noom 2\noom_kill 1\noom_group_kill 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pids, []byte("max 37\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := OOMKills(mem); err != nil || n != 1 {
		t.Fatalf("oom_kill = %d, %v", n, err)
	}
	if n, err := PidsLimitHits(pids); err != nil || n != 37 {
		t.Fatalf("pids max = %d, %v", n, err)
	}
	if _, err := PidsLimitHits(filepath.Join(dir, "none")); err == nil {
		t.Fatal("a missing file read as a count")
	}
	if _, err := OOMKills(pids); err == nil {
		t.Fatal("a file without oom_kill read as a count")
	}
}
