//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type wfTask struct {
	teamTask
	SpecKey   string   `json:"spec_key"`
	DependsOn []string `json:"depends_on"`
}

type wfSpec struct {
	ID      int64  `json:"id"`
	GoalID  string `json:"goal_id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Plan    struct {
		SubTasks []struct {
			Key       string   `json:"key"`
			DependsOn []string `json:"depends_on"`
		} `json:"sub_tasks"`
	} `json:"plan"`
}

type wfQARun struct {
	ID     int64  `json:"id"`
	GoalID string `json:"goal_id"`
	Status string `json:"status"`
	Branch string `json:"branch"`
	Merged []struct {
		TaskID string `json:"task_id"`
	} `json:"merged"`
}

// TestTeamWorkflowEndToEnd is Phase 12's chain with real Claude Code in
// sandbox mode: an architect submits a plan with a dependency; the plan is
// approved through the API (the user's step), committed as a spec and turned
// into sub-tasks; the Tech Lead assigns them in order, the dependent one
// starting from its dependency's work; QA merges both and passes; the goal is
// reported; the lead stays until the goal is marked done.
func TestTeamWorkflowEndToEnd(t *testing.T) {
	d, repo, listAgents := teamDaemon(t, "--max-agents", "4")

	var goal taskInfo
	d.json("POST", "/api/tasks", map[string]string{"title": "E2E workflow: two dependent notes",
		"description": "Do not ask the user anything: everything is decided here. Plan exactly two sub-tasks and " +
			"submit the plan right away:\n" +
			"- key \"alpha\": create the file wf-alpha.txt containing the single word alpha, and commit it. Files: wf-alpha.txt. " +
			"Acceptance: wf-alpha.txt contains alpha.\n" +
			"- key \"beta\", depends on alpha: create the file wf-beta.txt containing the single word beta, and commit it; " +
			"wf-alpha.txt must still be there. Files: wf-beta.txt. Acceptance: wf-beta.txt contains beta.\n" +
			"There are no tests to add for these files; QA checks the file contents."}, 201, &goal)
	var arch teamAgent
	d.json("POST", "/api/agents", map[string]string{"task_id": goal.ID, "role": "architect"}, 201, &arch)
	if arch.Kind != "architect" || arch.Role != "architect" {
		t.Fatalf("architect %+v", arch)
	}

	// 1. The architect's plan arrives.
	var spec wfSpec
	deadline := time.Now().Add(8 * time.Minute)
	for spec.ID == 0 {
		var specs []wfSpec
		d.json("GET", "/api/specs", nil, 200, &specs)
		for _, s := range specs {
			if s.GoalID == goal.ID && s.Status == "proposed" {
				spec = s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no plan from the architect; agents %+v", listAgents())
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("plan v%d: %+v", spec.Version, spec.Plan.SubTasks)
	if len(spec.Plan.SubTasks) != 2 {
		t.Fatalf("plan has %d sub-tasks", len(spec.Plan.SubTasks))
	}

	// 2. The user approves it: spec committed, sub-tasks created, Tech Lead started.
	var res struct {
		Spec      wfSpec     `json:"spec"`
		Created   []string   `json:"created"`
		Lead      *teamAgent `json:"lead"`
		LeadError string     `json:"lead_error"`
	}
	d.json("POST", "/api/specs/"+strconv.FormatInt(spec.ID, 10)+"/approve", nil, 200, &res)
	if res.Lead == nil || len(res.Created) != 2 {
		t.Fatalf("approval %+v", res)
	}
	if got := gitOut(t, repo, "show", res.Spec.Branch+":"+res.Spec.Path); !strings.Contains(got, "E2E workflow") {
		t.Fatalf("spec file on %s:\n%s", res.Spec.Branch, got)
	}
	waitAgent(t, listAgents, arch.ID, time.Minute, func(a teamAgent) bool { return a.State == "exited" })
	lead := res.Lead.ID

	// 3. The goal is reported (which needs QA to pass) and the lead is idle.
	deadline = time.Now().Add(25 * time.Minute)
	for {
		var tasks []wfTask
		d.json("GET", "/api/tasks", nil, 200, &tasks)
		g := tasks[slices.IndexFunc(tasks, func(x wfTask) bool { return x.ID == goal.ID })]
		la := findAgent(listAgents(), lead)
		if g.Status == "review" && la.State == "idle" {
			break
		}
		if la.State == "exited" {
			t.Fatalf("the Tech Lead exited (%s); goal %s", la.ExitReason, g.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: goal %s; tasks %+v; agents %+v", g.Status, tasks, listAgents())
		}
		time.Sleep(5 * time.Second)
	}

	// The dependent sub-task's branch contains its dependency's work.
	var tasks []wfTask
	d.json("GET", "/api/tasks", nil, 200, &tasks)
	var dependent wfTask
	for _, x := range tasks {
		if x.ParentTaskID == goal.ID && len(x.DependsOn) == 1 {
			dependent = x
		}
	}
	if dependent.ID == "" {
		t.Fatalf("no dependent sub-task: %+v", tasks)
	}
	var depBranch string
	for _, a := range listAgents() {
		if a.TaskID == dependent.ID && a.Kind == "worker" && a.LastImport != nil {
			depBranch = a.LastImport.Branch
		}
	}
	if got := strings.TrimSpace(gitOut(t, repo, "show", depBranch+":wf-alpha.txt")); got != "alpha" {
		t.Errorf("the dependent branch %s lacks its dependency's file: %q", depBranch, got)
	}
	// QA passed on both branches.
	var runs []wfQARun
	d.json("GET", "/api/qa-runs", nil, 200, &runs)
	if len(runs) == 0 || runs[0].Status != "passed" || len(runs[0].Merged) != 2 {
		t.Fatalf("QA runs %+v", runs)
	}
	for _, f := range []string{"wf-alpha.txt", "wf-beta.txt"} {
		if out, err := exec.Command("git", "-C", repo, "show", runs[0].Branch+":"+f).Output(); err != nil {
			t.Errorf("QA's integration branch %s lacks %s: %v", runs[0].Branch, f, err)
		} else {
			t.Logf("%s on the integration branch: %q", f, strings.TrimSpace(string(out)))
		}
	}
	logData, _ := os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		switch {
		case strings.Contains(line, "team tool failed"), strings.Contains(line, "api request refused"):
			t.Errorf("%s", line)
		case strings.Contains(line, "team tool refused"):
			t.Logf("refused team call (the model corrected itself): %s", line)
		case strings.Contains(line, "plan submitted"), strings.Contains(line, "plan approved"),
			strings.Contains(line, "sub-task starts from its dependencies"), strings.Contains(line, "QA started"),
			strings.Contains(line, "QA reported"), strings.Contains(line, "goal reported"), strings.Contains(line, "conflicts found"):
			t.Log(line)
		}
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the user's checkout changed: %q", st)
	}

	// 4. The user marks the goal done: the Tech Lead ends.
	d.json("PATCH", "/api/tasks/"+goal.ID, map[string]string{"status": "done"}, 200, nil)
	waitAgent(t, listAgents, lead, 30*time.Second, func(a teamAgent) bool { return a.State == "exited" })
}
