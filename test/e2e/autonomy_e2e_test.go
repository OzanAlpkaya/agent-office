//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

type accessRequest struct {
	ID             int64  `json:"id"`
	AgentID        string `json:"agent_id"`
	Value          string `json:"value"`
	Status         string `json:"status"`
	DecidedBy      string `json:"decided_by"`
	DeciderAgentID string `json:"decider_agent_id"`
	DecisionReason string `json:"decision_reason"`
	Scope          string `json:"scope"`
}

type userMessage struct {
	message
	FromUser bool `json:"from_user"`
}

// oomKill makes the kernel's out-of-memory killer end a sandboxed agent's
// CLI while it works, as in Ozan's PulseGo run: the CLI's processes are made
// the killer's first choice (raising oom_score_adj is allowed for the same
// user), then a memory hog fills the worker up to its limit.
func oomKill(t *testing.T, agentID string) {
	t.Helper()
	script := `for p in /proc/[0-9]*; do [ "$p" = /proc/$$ ] && continue; ` +
		`case "$(tr '\0' ' ' < $p/cmdline 2>/dev/null)" in *claude*) echo 1000 > $p/oom_score_adj 2>/dev/null;; esac; done; ` +
		`head -c 16g /dev/zero | tail`
	out, err := exec.Command("podman", "exec", sandbox.ContainerName(agentID), "sh", "-c", script).CombinedOutput()
	t.Logf("memory hog in %s: %v %s", agentID, err, strings.TrimSpace(string(out)))
}

// TestAutonomousTeamEndToEnd reproduces the incidents of Ozan's PulseGo
// Sprint 3 run with real Claude Code (Phase 16): a worker's network access
// request decided by the Tech Lead within the project's host policy (and
// still allowed after the worker's resume, request #4); a working worker
// OOM-killed by the kernel twice (resumed once by agent-office, then the
// lead's to deal with); a worker stopped by the user with a message waiting
// for it while the whole team sits idle until the stall watchdog wakes the
// lead. The goal must reach review without a human answering anything.
func TestAutonomousTeamEndToEnd(t *testing.T) {
	d, repo, listAgents := teamDaemon(t, "--max-agents", "3", "--qa=false", "--stall-after", "30s")
	var projects []struct {
		ID      string `json:"id"`
		Default bool   `json:"default"`
	}
	d.json("GET", "/api/projects", nil, http.StatusOK, &projects)
	pid := projects[slices.IndexFunc(projects, func(p struct {
		ID      string `json:"id"`
		Default bool   `json:"default"`
	}) bool {
		return p.Default
	})].ID
	d.json("PATCH", "/api/projects/"+pid, map[string]any{"settings": map[string]any{
		"access_decider": "lead_policy", "lead_access_hosts": []string{"example.com"}, "worker_memory": "1g"}}, http.StatusOK, nil)

	var goal taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E autonomy: a reachability note",
		"description": "Create exactly one sub-task with this description, word for word, and assign it right away:\n\n" +
			"1. Call the agent-office request_access tool for the host example.com (kind domain) with the reason " +
			"\"record whether example.com answers\", then end your turn and wait for the decision message.\n" +
			"2. Once it is approved, run: curl -sS -o /dev/null -w '%{http_code}' https://example.com/ and write the " +
			"printed status code into a file named reach.txt.\n" +
			"3. Write a file named notes.txt containing the single word ready.\n" +
			"4. Commit both files, then call report_done.\n\n" +
			"When the worker has reported, call report_done for the goal."}, http.StatusCreated, &goal)
	var lead teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": goal.ID, "role": "tech-lead"}, http.StatusCreated, &lead)

	// Incident 7: the request goes to the Tech Lead, which decides it with a
	// recorded reason; the user does nothing.
	var req accessRequest
	waitFor(t, "the lead's access decision", 6*time.Minute, func() bool {
		var list []accessRequest
		d.json("GET", "/api/access-requests", nil, http.StatusOK, &list)
		for _, r := range list {
			if r.Value == "example.com" && r.Status != "pending" {
				req = r
				return true
			}
		}
		return false
	})
	if req.Status != "approved" || req.DecidedBy != "lead" || req.DeciderAgentID != lead.ID || req.DecisionReason == "" {
		t.Fatalf("access decision %+v", req)
	}
	t.Logf("the lead approved #%d for %s: %q", req.ID, req.Scope, req.DecisionReason)
	worker := req.AgentID

	working := func(id string) teamAgent {
		return waitAgent(t, listAgents, id, 6*time.Minute, func(a teamAgent) bool {
			if a.State == "exited" && a.ExitReason == "reported_done" {
				t.Fatalf("worker %s finished before the incident could be reproduced", id)
			}
			return a.State == "working"
		})
	}
	sessionOf := func(id string) string { return findAgent(listAgents(), id).SessionID }

	// Incidents 1 and 2: a real OOM kill while it works; agent-office says why
	// and resumes it once.
	working(worker)
	first := sessionOf(worker)
	oomKill(t, worker)
	resumed := waitAgent(t, listAgents, worker, 3*time.Minute, func(a teamAgent) bool {
		return a.SessionID != first && a.ResumedFrom != "" && a.State != "exited"
	})
	t.Logf("resumed automatically: session %s from %s", resumed.SessionID, resumed.ResumedFrom)
	leadSays := func(sub string) bool {
		var msgs []message
		d.json("GET", "/api/agents/"+lead.ID+"/messages", nil, http.StatusOK, &msgs)
		return slices.ContainsFunc(msgs, func(m message) bool { return m.To == lead.ID && strings.Contains(m.Body, sub) })
	}
	waitFor(t, "the lead hears of the OOM kill", time.Minute, func() bool {
		return leadSays("Out of memory") && leadSays("resumed its conversation automatically")
	})
	// Request #4: the approval holds in the resumed session.
	waitAgent(t, listAgents, worker, time.Minute, func(a teamAgent) bool { return a.State == "working" || a.State == "idle" })
	if out, err := exec.Command("podman", "exec", sandbox.ContainerName(worker), "curl", "-sS", "-o", "/dev/null", "-w",
		"%{http_code}", "https://example.com/").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "200" {
		t.Errorf("example.com from the resumed session: %v %q", err, out)
	}

	// The second OOM kill is not retried: the lead gets the reason and acts.
	working(worker)
	oomKill(t, worker)
	dead := waitAgent(t, listAgents, worker, 2*time.Minute, func(a teamAgent) bool { return a.State == "exited" })
	if !dead.OOMKilled || dead.ExitSignal != "KILL" || !strings.Contains(dead.ExitDetail, "Out of memory") ||
		!strings.Contains(dead.ExitDetail, "1g memory limit") {
		t.Errorf("second exit %+v", dead.agentInfo)
	}
	waitFor(t, "the lead hears of the second failure", time.Minute, func() bool { return leadSays("died again") })
	time.Sleep(5 * time.Second)
	if a := findAgent(listAgents(), worker); a.SessionID != dead.SessionID {
		// The lead may have resumed it already (resume_agent): that is the lead's
		// decision, not an automatic resume.
		t.Logf("the lead acted quickly: %s is in session %s", worker, a.SessionID)
	}

	// The lead resumes the worker or gives the sub-task to a new one.
	var current teamAgent
	waitFor(t, "the lead brings a worker back", 6*time.Minute, func() bool {
		for _, a := range listAgents() {
			if a.Role == "worker" && a.State == "working" {
				current = a
				return true
			}
		}
		return false
	})
	t.Logf("worker %s works again (resumed from %q)", current.ID, current.ResumedFrom)

	// Incidents 4 and 5: the user stops it and leaves it a message; nobody
	// else moves, so the watchdog wakes the lead, which resumes it; the
	// message is delivered then.
	d.json("POST", "/api/agents/"+current.ID+"/stop", nil, http.StatusOK, nil)
	d.json("POST", "/api/agents/"+current.ID+"/message", map[string]string{
		"body": "Note from the user: keep reach.txt to the status code only."}, http.StatusCreated, nil)
	stopped := findAgent(listAgents(), current.ID)
	waitFor(t, "the watchdog wakes the lead", 4*time.Minute, func() bool { return leadSays("[agent-office] Status check") })
	waitFor(t, "the user's message reaches the resumed worker", 6*time.Minute, func() bool {
		var msgs []userMessage
		d.json("GET", "/api/agents/"+current.ID+"/messages", nil, http.StatusOK, &msgs)
		return slices.ContainsFunc(msgs, func(m userMessage) bool {
			return m.FromUser && strings.Contains(m.Body, "keep reach.txt") && m.DeliveredAt != ""
		})
	})
	if a := findAgent(listAgents(), current.ID); a.SessionID == stopped.SessionID {
		t.Errorf("the message was delivered without a new session: %+v", a)
	}

	// The goal reaches review without the user answering anything.
	deadline := time.Now().Add(12 * time.Minute)
	for {
		var tasks []teamTask
		d.json("GET", "/api/tasks", nil, http.StatusOK, &tasks)
		g := tasks[slices.IndexFunc(tasks, func(x teamTask) bool { return x.ID == goal.ID })]
		if g.Status == "review" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goal %s; agents %+v", g.Status, listAgents())
		}
		time.Sleep(5 * time.Second)
	}
	var inbox []struct {
		Kind       string `json:"kind"`
		AgentID    string `json:"agent_id"`
		AnsweredAt string `json:"answered_at"`
		Body       string `json:"body"`
	}
	d.json("GET", "/api/inbox", nil, http.StatusOK, &inbox)
	for _, it := range inbox {
		t.Logf("inbox: %s from %q: %s", it.Kind, it.AgentID, it.Body)
		if it.Kind == "question" || it.Kind == "blocked" {
			t.Errorf("the team asked the user: %s", it.Body)
		}
	}
	found := map[string]string{}
	for _, a := range listAgents() {
		if a.Role != "worker" || a.LastImport == nil || a.LastImport.Branch == "" {
			continue
		}
		for _, name := range []string{"notes.txt", "reach.txt"} {
			if out, err := exec.Command("git", "-C", repo, "show", a.LastImport.Branch+":"+name).Output(); err == nil {
				found[name] = strings.TrimSpace(string(out))
			}
		}
	}
	if found["notes.txt"] != "ready" || found["reach.txt"] != "200" {
		t.Errorf("files on the workers' branches: %v", found)
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		for _, k := range []string{"agent exited", "agent resumed automatically", "stall watchdog", "worker resumed by the lead",
			"sub-task reclaimed", "access request decided", "out-of-memory kill"} {
			if strings.Contains(line, k) {
				t.Log(line)
			}
		}
		if strings.Contains(line, "team tool failed") {
			t.Errorf("team tool failed: %s", line)
		}
	}
	d.json("PATCH", "/api/tasks/"+goal.ID, map[string]string{"status": "done"}, http.StatusOK, nil)
}

// waitFor polls ok every 2 s for up to d.
func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Second)
	}
}
