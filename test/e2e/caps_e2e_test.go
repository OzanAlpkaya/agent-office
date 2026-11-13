//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// TestDaemonCapFromTheUIEndToEnd (Phase 20, item 8): two projects with Max
// agents 8 under a daemon-wide cap of 4 run 4 agents in total. Raising the
// daemon-wide cap to 8 in the real UI (a browser, paired like a user's)
// lets 6 run across the two projects, with the same daemon process: no
// restart. Agents are a fake claude that only waits, so no tokens are spent.
func TestDaemonCapFromTheUIEndToEnd(t *testing.T) {
	if os.Getenv("AO_E2E_REPO") == "" {
		t.Skip("AO_E2E_REPO not set (make e2e)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH: the browser script needs it")
	}
	tmp := t.TempDir()
	repos := []string{filepath.Join(tmp, "alpha"), filepath.Join(tmp, "beta")}
	for _, r := range repos {
		for _, args := range [][]string{{"init", "-q", "-b", "main", r}, {"-C", r, "-c", "user.name=t", "-c", "user.email=t@example.com",
			"commit", "-q", "--allow-empty", "-m", "init"}} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	fake := filepath.Join(tmp, "fake-claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n# A Claude Code stand-in that only waits.\n"+
		"case \"$1\" in --version) echo '2.1.323 (Claude Code)'; exit 0;; esac\nexec sleep 3600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "agent-office")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/agent-office").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	data := filepath.Join(tmp, "data")
	port := freePort(t)
	d := &daemon{t: t, bin: bin, base: "http://127.0.0.1:" + port, logPath: filepath.Join(tmp, "daemon.log"),
		args: []string{"serve", "--repo", repos[0], "--addr", "127.0.0.1:" + port, "--data-dir", data, "--max-agents", "4",
			"--sandbox", "host", "--claude-bin", fake, "--log-level", "debug"}}
	var started []string // project path + agent id
	t.Cleanup(func() {
		if d.cmd != nil {
			for _, s := range started {
				p, id, _ := strings.Cut(s, "|")
				d.do("POST", p+"/agents/"+id+"/stop", nil)
			}
		}
		d.signal(syscall.SIGTERM)
		if t.Failed() {
			if logData, err := os.ReadFile(d.logPath); err == nil {
				t.Logf("daemon log tail:\n%s", tail(string(logData), 60))
			}
		}
	})
	d.start()
	pid := d.cmd.Process.Pid
	if out, err := exec.Command(bin, "project", "add", repos[1], "--name", "beta", "--data-dir", data).CombinedOutput(); err != nil {
		t.Fatalf("project add: %v %s", err, out)
	}
	var listed []struct{ ID, Name string }
	d.json("GET", "/api/projects", nil, http.StatusOK, &listed)
	if len(listed) != 2 {
		t.Fatalf("projects %+v", listed)
	}
	var paths []string
	for _, p := range listed {
		path := "/api/projects/" + p.ID
		d.json("PATCH", path, map[string]any{"settings": map[string]any{"max_agents": 8}}, http.StatusOK, nil)
		paths = append(paths, path)
	}
	start := func(path string, want int) {
		t.Helper()
		code, body := d.do("POST", path+"/agents", nil)
		if code != want {
			t.Fatalf("start in %s: %d %s, want %d", path, code, body, want)
		}
		if code == http.StatusCreated {
			id := regexp.MustCompile(`"id":"([0-9a-f]{12})"`).FindStringSubmatch(string(body))
			if id == nil {
				t.Fatalf("no agent id in %s", body)
			}
			started = append(started, path+"|"+id[1])
		}
	}
	// The daemon-wide cap of 4 holds across both projects despite their 8.
	for _, p := range paths {
		start(p, http.StatusCreated)
		start(p, http.StatusCreated)
	}
	for _, p := range paths {
		code, body := d.do("POST", p+"/agents", nil)
		if code != http.StatusConflict || !strings.Contains(string(body), "daemon-wide cap of 4") {
			t.Fatalf("a fifth agent: %d %s", code, body)
		}
	}

	// The browser raises it, from the project's settings warning.
	out, err := exec.Command(bin, "pair", "--data-dir", data).CombinedOutput()
	code := regexp.MustCompile(`#pair=([A-Z0-9-]+)`).FindStringSubmatch(string(out))
	if err != nil || code == nil {
		t.Fatalf("pair: %v %s", err, out)
	}
	browser := exec.Command("node", "e2e/raise-cap.ts", d.base, code[1], "8", "4", "8")
	browser.Dir = filepath.Join("..", "..", "web")
	if out, err := browser.CombinedOutput(); err != nil {
		t.Fatalf("the browser script: %v\n%s", err, out)
	} else {
		t.Logf("%s", strings.TrimSpace(string(out)))
	}

	// Six run across the two projects now, with the same daemon.
	start(paths[0], http.StatusCreated)
	start(paths[1], http.StatusCreated)
	running := 0
	for _, p := range paths {
		var st struct {
			Caps struct {
				DaemonMax int `json:"daemon_max"`
				Effective int `json:"effective"`
				Running   int `json:"running"`
			} `json:"caps"`
		}
		d.json("GET", p+"/status", nil, http.StatusOK, &st)
		if st.Caps.DaemonMax != 8 || st.Caps.Effective != 8 {
			t.Fatalf("%s caps %+v, want the daemon-wide cap of 8", p, st.Caps)
		}
		running += st.Caps.Running
	}
	if running != 6 {
		t.Fatalf("%d agents run, want 6 across the two projects", running)
	}
	if d.cmd.Process.Pid != pid {
		t.Fatal("the daemon was restarted")
	}
	if err := d.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the daemon is gone: %v", err)
	}
}
