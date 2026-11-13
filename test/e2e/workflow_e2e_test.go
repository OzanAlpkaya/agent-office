//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
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
// reported; the user merges it into main and pushes it to a local bare
// remote. Phase 19 (items 5 and 6): the user asks for a follow-up round that
// changes a file the squash merge brought into main, with the project's limit
// of open sub-tasks at 2 (the reported ones do not count); the new sub-task
// starts from main, QA tests it from main, and the second merge has no
// conflicts. Marking the goal done closes its sub-tasks; its agents are
// removed.
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

	// 4. Phase 17 (ADR 0055): the user merges the goal's tested work into main
	// and pushes it, on this throwaway clone with a local bare remote (origin
	// is replaced first, so nothing reaches the playground's own remote).
	if b := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(b) != "main" {
		t.Fatalf("the clone is on %q; the merge target is main", b)
	}
	bare := t.TempDir()
	gitOut(t, bare, "init", "-q", "--bare", "-b", "main")
	gitOut(t, repo, "remote", "set-url", "origin", bare)
	gitOut(t, repo, "remote", "set-url", "--push", "origin", bare)
	gitOut(t, repo, "push", "-q", "origin", "main")
	var rec struct {
		Report *struct {
			Summary       string `json:"summary"`
			CommitMessage string `json:"commit_message"`
		} `json:"report"`
		Agents []struct {
			ID      string `json:"id"`
			Removed bool   `json:"removed"`
		} `json:"agents"`
	}
	d.json("GET", "/api/tasks/"+goal.ID+"/record", nil, 200, &rec)
	if rec.Report == nil || rec.Report.Summary == "" {
		t.Fatalf("no Tech Lead report in the record: %+v", rec)
	}
	t.Logf("the Tech Lead's commit message: %q", rec.Report.CommitMessage)
	var p struct {
		Ready      bool   `json:"ready"`
		Reason     string `json:"reason"`
		Branch     string `json:"branch"`
		SourceHead string `json:"source_head"`
		Target     string `json:"target"`
		TargetHead string `json:"target_head"`
		Message    string `json:"message"`
		RemoteURL  string `json:"remote_url"`
	}
	d.json("GET", "/api/tasks/"+goal.ID+"/merge", nil, 200, &p)
	if !p.Ready || p.Branch != runs[0].Branch || p.Target != "main" || p.Message == "" || p.RemoteURL != bare {
		t.Fatalf("merge preview %+v", p)
	}
	if rec.Report.CommitMessage != "" && p.Message != rec.Report.CommitMessage {
		t.Errorf("the preview's message is not the Tech Lead's: %q", p.Message)
	}
	before := strings.TrimSpace(gitOut(t, repo, "rev-parse", "main"))
	var m struct {
		Commit   string `json:"commit"`
		PushedAt string `json:"pushed_at"`
	}
	d.json("POST", "/api/tasks/"+goal.ID+"/merge", map[string]string{"target_head": p.TargetHead,
		"source_head": p.SourceHead, "message": p.Message}, 200, &m)
	if head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "main")); head != m.Commit || head == before {
		t.Fatalf("main at %s after the merge %s (before %s)", head, m.Commit, before)
	}
	for f, want := range map[string]string{"wf-alpha.txt": "alpha", "wf-beta.txt": "beta"} {
		if b, err := os.ReadFile(filepath.Join(repo, f)); err != nil || strings.TrimSpace(string(b)) != want {
			t.Errorf("%s in the checked-out main after the merge: %q %v", f, b, err)
		}
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the checkout is not clean after the merge: %q", st)
	}
	d.json("POST", "/api/tasks/"+goal.ID+"/merge", map[string]string{"target_head": m.Commit,
		"source_head": p.SourceHead, "message": "again"}, 409, nil)

	// 4b. Phase 19: a follow-up round after the squash merge (ADR 0062, 0063).
	// The open sub-task limit is lowered to 2 while the lead runs: the goal
	// already has 2 sub-tasks, reported, which do not count.
	pid, _ := defaults.Load(d.base)
	d.json("PATCH", "/api/projects/"+pid.(string), map[string]any{"settings": map[string]any{"max_sub_tasks": 2}}, 200, nil)
	firstMerge := m.Commit
	d.json("POST", "/api/agents/"+lead+"/message", map[string]string{"body": "Follow-up for this goal, decided, do not ask: " +
		"create exactly one new sub-task \"gamma\": change wf-beta.txt so that it has two lines, beta and then gamma, and " +
		"commit it. Files: wf-beta.txt. Acceptance: wf-beta.txt is beta, then gamma. This change is wanted: it replaces what " +
		"the beta sub-task asked of wf-beta.txt, so file nothing about that. Assign it, run QA when it is reported, " +
		"then report the goal again with report_done and a commit_message."}, 201, nil)
	deadline = time.Now().Add(25 * time.Minute)
	var gammaTask wfTask
	for {
		d.json("GET", "/api/tasks", nil, 200, &tasks)
		g := tasks[slices.IndexFunc(tasks, func(x wfTask) bool { return x.ID == goal.ID })]
		for _, x := range tasks {
			// The follow-up is the sub-task not from the plan (the lead may make it depend on beta).
			if x.ParentTaskID == goal.ID && x.SpecKey == "" && strings.Contains(strings.ToLower(x.Title), "gamma") {
				gammaTask = x
			}
		}
		d.json("GET", "/api/qa-runs", nil, 200, &runs)
		la := findAgent(listAgents(), lead)
		if gammaTask.ID != "" && g.Status == "review" && la.State == "idle" && len(runs) >= 2 && runs[0].Status == "passed" {
			break
		}
		if la.State == "exited" {
			t.Fatalf("the Tech Lead exited (%s) during the follow-up; goal %s", la.ExitReason, g.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout in the follow-up: goal %s; tasks %+v; QA %+v; agents %+v", g.Status, tasks, runs, listAgents())
		}
		time.Sleep(5 * time.Second)
	}
	var gammaBranch string
	for _, a := range listAgents() {
		if a.TaskID == gammaTask.ID && a.Kind == "worker" && a.LastImport != nil {
			gammaBranch = a.LastImport.Branch
		}
	}
	if gammaBranch == "" {
		t.Fatalf("no imported branch for the follow-up sub-task %s", gammaTask.ID)
	}
	if err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", firstMerge, gammaBranch).Run(); err != nil {
		t.Errorf("the follow-up branch %s does not start from main after the merge (%s)", gammaBranch, firstMerge)
	}
	if len(runs[0].Merged) != 1 || runs[0].Merged[0].TaskID != gammaTask.ID {
		t.Errorf("the follow-up QA run merged %+v; want the follow-up sub-task only", runs[0].Merged)
	}
	d.json("GET", "/api/tasks/"+goal.ID+"/merge", nil, 200, &p)
	if !p.Ready || p.Branch != runs[0].Branch {
		t.Fatalf("the follow-up's merge preview: %+v", p)
	}
	d.json("POST", "/api/tasks/"+goal.ID+"/merge", map[string]string{"target_head": p.TargetHead,
		"source_head": p.SourceHead, "message": p.Message}, 200, &m)
	if m.Commit == firstMerge || strings.TrimSpace(gitOut(t, repo, "rev-parse", "main")) != m.Commit {
		t.Fatalf("the second merge: %+v", m)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "wf-beta.txt")); err != nil ||
		strings.Join(strings.Fields(string(b)), " ") != "beta gamma" {
		t.Errorf("wf-beta.txt in main after the follow-up's merge: %q %v", b, err)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the checkout is not clean after the second merge: %q", st)
	}
	// Item 3: an Architect started again during the follow-up ends once nothing waits for it; one that
	// asked the user a question waits for the answer.
	var inbox []struct {
		AgentID    string `json:"agent_id"`
		Kind       string `json:"kind"`
		AnsweredAt string `json:"answered_at"`
		Body       string `json:"body"`
	}
	d.json("GET", "/api/inbox", nil, 200, &inbox)
	asked := slices.ContainsFunc(inbox, func(i struct {
		AgentID    string `json:"agent_id"`
		Kind       string `json:"kind"`
		AnsweredAt string `json:"answered_at"`
		Body       string `json:"body"`
	}) bool {
		return i.AgentID == arch.ID && i.Kind == "question" && i.AnsweredAt == ""
	})
	if a := findAgent(listAgents(), arch.ID); a.State != "exited" && !asked {
		time.Sleep(10 * time.Second) // its end check runs a few seconds after its turn
		if a = findAgent(listAgents(), arch.ID); a.State != "exited" {
			t.Errorf("the Architect is %s after the follow-up, with no question to the user", a.State)
		}
	}
	logData, _ = os.ReadFile(d.logPath)
	for _, line := range strings.Split(string(logData), "\n") {
		if strings.Contains(line, "architect") && (strings.Contains(line, "relaunched") || strings.Contains(line, "ends") ||
			strings.Contains(line, "message_user") || strings.Contains(line, "agent exited")) {
			t.Log(line)
		}
	}

	// 5. The user marks the goal done: its sub-tasks are done, the Tech Lead
	// ends; the push and the cleanup work on the done goal.
	d.json("PATCH", "/api/tasks/"+goal.ID, map[string]string{"status": "done"}, 200, nil)
	waitAgent(t, listAgents, lead, 30*time.Second, func(a teamAgent) bool { return a.State == "exited" })
	d.json("GET", "/api/tasks", nil, 200, &tasks)
	for _, x := range tasks {
		if x.ParentTaskID == goal.ID && x.Status != "done" {
			t.Errorf("sub-task %s is %s after the goal was marked done", x.ID, x.Status)
		}
	}
	d.json("POST", "/api/tasks/"+goal.ID+"/push", nil, 200, &m)
	if m.PushedAt == "" || strings.TrimSpace(gitOut(t, bare, "rev-parse", "main")) != m.Commit {
		t.Fatalf("push: %+v; the bare remote's main is %s", m, gitOut(t, bare, "rev-parse", "main"))
	}
	var cleanup struct {
		Removed []struct {
			AgentID string `json:"agent_id"`
		} `json:"removed"`
		Kept []struct {
			AgentID string `json:"agent_id"`
			Reason  string `json:"reason"`
		} `json:"kept"`
	}
	d.json("POST", "/api/tasks/"+goal.ID+"/remove-agents", nil, 200, &cleanup)
	t.Logf("cleanup: removed %d, kept %+v", len(cleanup.Removed), cleanup.Kept)
	if len(cleanup.Removed) == 0 {
		t.Errorf("no agent removed: %+v", cleanup)
	}
	d.json("GET", "/api/tasks/"+goal.ID+"/record", nil, 200, &rec)
	removed := 0
	for _, a := range rec.Agents {
		if a.Removed {
			removed++
		}
	}
	if removed != len(cleanup.Removed) {
		t.Errorf("the record lists %d removed agents, the cleanup removed %d", removed, len(cleanup.Removed))
	}
}
