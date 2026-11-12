package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

func samplePlan() Plan {
	return Plan{Summary: "Tags for links.", Design: "A tags table and a filter.",
		SubTasks: []mcp.PlanSubTask{
			{Key: "schema", Title: "Tags table", Description: "Add the table.", Files: []string{"internal/store/"},
				Acceptance: []string{"a link can have tags"}},
			{Key: "api", Title: "Tag filter", Description: "Filter by tag.", DependsOn: []string{"schema"},
				Files: []string{"internal/httpapi/handler.go"}, Acceptance: []string{"GET /links?tag=x filters"}},
			{Key: "docs", Title: "Document tags", Role: "docs", DependsOn: []string{"api"}, Files: []string{"README.md"}},
		}, Risks: "Migrations."}
}

func TestValidatePlan(t *testing.T) {
	e := engineWithOptions(t, Options{Roles: role.MustBuiltins()})
	p := samplePlan()
	p.SubTasks[0].Files = append(p.SubTasks[0].Files, " internal/store/ ", "")
	if err := e.validatePlan(&p); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.SubTasks[0].Files, []string{"internal/store/"}) {
		t.Fatalf("files normalized: %v", p.SubTasks[0].Files)
	}
	bad := map[string]func(p *Plan){
		"summary":               func(p *Plan) { p.Summary = " " },
		"1-20 sub-tasks":        func(p *Plan) { p.SubTasks = nil },
		"used twice":            func(p *Plan) { p.SubTasks[1].Key = "schema" },
		"lower-case":            func(p *Plan) { p.SubTasks[0].Key = "Schema!" },
		"not a key of the plan": func(p *Plan) { p.SubTasks[1].DependsOn = []string{"nope"} },
		"depends on itself":     func(p *Plan) { p.SubTasks[0].DependsOn = []string{"schema"} },
		"cycle":                 func(p *Plan) { p.SubTasks[0].DependsOn = []string{"docs"} },
		"is not valid":          func(p *Plan) { p.SubTasks[0].Files = []string{"../etc/passwd"} },
		"not a worker role":     func(p *Plan) { p.SubTasks[2].Role = "tech-lead" },
		"title":                 func(p *Plan) { p.SubTasks[0].Title = "" },
	}
	for want, mutate := range bad {
		p := samplePlan()
		mutate(&p)
		if err := e.validatePlan(&p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	if c := findCycle(map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}); len(c) != 4 {
		t.Errorf("cycle %v", c)
	}
	for _, f := range []string{"a.go", "dir/", "internal/*.go", "web/src/**"} {
		if !validFilePattern(f) {
			t.Errorf("%q refused", f)
		}
	}
	for _, f := range []string{"/abs", "..", "../x", "a/../b", "a\\b", "[", "./"} {
		if validFilePattern(f) {
			t.Errorf("%q accepted", f)
		}
	}
}

func TestPatternsOverlap(t *testing.T) {
	cases := []struct {
		p, q string
		want bool
	}{
		{"a.go", "a.go", true},
		{"internal/store/", "internal/store/store.go", true},
		{"internal/store/store.go", "internal/store/", true},
		{"internal/store", "internal/store/x.go", false}, // a path, not a directory
		{"internal/*.go", "internal/a.go", true},
		{"internal/*.go", "web/a.go", false},
		{"internal/api/*.go", "internal/", true},
		{"internal/api/*", "internal/store/*", false},
		{"internal/*", "internal/api/*", true},
		{"README.md", "docs/README.md", false},
		{"internal/**/x.go", "internal/a/b/x.go", true}, // path.Match has no "**"
		{"web/**", "internal/a.go", false},
	}
	for _, c := range cases {
		if got := patternsOverlap(c.p, c.q); got != c.want {
			t.Errorf("%s ~ %s = %v", c.p, c.q, got)
		}
	}
}

// git runs git in dir and returns its trimmed output.
func gitOutIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitIn(t, dir, args...)
}

// The architect's plan is approved in the UI's API: the spec is committed on
// its own branch, the sub-tasks are created with their dependencies, the
// architect ends and a Tech Lead starts on the plan (DoD).
func TestArchitectPlanToTechLead(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 5
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	tm := e.Team()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags", Description: "tags for links"})
	arch, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	head := gitOutIn(t, f.repo, "rev-parse", "HEAD")

	// Only the architect submits plans, and they are validated.
	bad := samplePlan()
	bad.SubTasks[0].DependsOn = []string{"docs"}
	if _, err := tm.SubmitPlan(ctx, arch.ID, bad); !isUser(err, ErrInvalid) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cyclic plan: %v", err)
	}
	out, err := tm.SubmitPlan(ctx, arch.ID, samplePlan())
	if err != nil || out.Version != 1 || out.Status != SpecProposed || !strings.Contains(out.Next, "not approved") ||
		!strings.Contains(out.Next, "press Approve") {
		t.Fatalf("submit: %+v %v", out, err)
	}
	// Its prompt says that only the UI approves (Ozan's test: "approved" typed
	// into the terminal made an earlier architect claim the plan was approved).
	screen(t, e, arch.ID, "Approval happens only with the Approve button in the agent-office UI")
	items, _ := e.Inbox(ctx)
	if len(items) != 1 || items[0].Kind != InboxPlan || items[0].AgentID != "" || items[0].GoalID != goal.ID {
		t.Fatalf("plan notice %+v", items)
	}
	// The user edits it: a new version; the first is superseded.
	edited := samplePlan()
	edited.SubTasks[0].Title = "Tags table (edited)"
	v2, err := e.EditSpec(ctx, out.SpecID, edited)
	if err != nil || v2.Version != 2 || v2.Author != "" {
		t.Fatalf("edit: %+v %v", v2, err)
	}
	if _, err := e.ApproveSpec(ctx, out.SpecID); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("approving a superseded version: %v", err)
	}
	res, err := e.ApproveSpec(ctx, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 3 || res.Lead == nil || res.Lead.Kind != KindLead || res.Spec.Status != SpecApproved {
		t.Fatalf("approve: %+v", res)
	}
	if _, err := e.ApproveSpec(ctx, v2.ID); err == nil {
		t.Fatal("approved twice")
	}

	// The spec is a commit on the spec branch, on top of HEAD; the user's
	// checkout is untouched.
	file, branch := "docs/specs/ship-tags.md", res.Spec.Branch
	if res.Spec.Path != file || !strings.HasPrefix(branch, "agent-office/spec-ship-tags-") {
		t.Fatalf("location %s %s", res.Spec.Path, branch)
	}
	if got := gitOutIn(t, f.repo, "show", branch+":"+file); !strings.Contains(got, "# Ship tags") ||
		!strings.Contains(got, "Tags table (edited)") || !strings.Contains(got, "**Depends on:** `schema`") {
		t.Fatalf("spec file:\n%s", got)
	}
	if p := gitOutIn(t, f.repo, "rev-parse", branch+"^"); p != head {
		t.Fatalf("spec parent %s, HEAD %s", p, head)
	}
	if gitOutIn(t, f.repo, "rev-parse", "HEAD") != head || gitOutIn(t, f.repo, "status", "--porcelain") != "" {
		t.Fatal("the user's checkout changed")
	}

	// Sub-tasks with their dependencies as task ids.
	byKey := map[string]Task{}
	tasks, _ := e.ListTasks(ctx)
	for _, x := range tasks {
		if x.ParentTaskID == goal.ID {
			byKey[x.SpecKey] = x
		}
	}
	if len(byKey) != 3 || !slices.Equal(byKey["api"].DependsOn, []string{byKey["schema"].ID}) || byKey["docs"].Role != "docs" ||
		!slices.Equal(byKey["schema"].Acceptance, []string{"a link can have tags"}) {
		t.Fatalf("sub-tasks %+v", byKey)
	}
	// The architect is done; the Tech Lead knows the plan.
	log.wait(t, 5*time.Second, agentState(arch.ID, hooks.Exited))
	if a, _ := e.Get(arch.ID); a.ExitReason != ExitPlanApproved {
		t.Fatalf("architect exit %q", a.ExitReason)
	}
	lead := res.Lead.ID
	log.wait(t, startWait, agentState(lead, hooks.Idle))
	screen(t, e, lead, "An approved plan exists (version 2")

	// The first sub-task's worker starts from the spec; the dependent one waits.
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: byKey["api"].ID}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "waits for") {
		t.Fatalf("dependent before its dependency: %v", err)
	}
	a1, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: byKey["schema"].ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a1.AgentID, hooks.Idle))
	w1, _ := e.Get(a1.AgentID)
	if _, err := os.Stat(filepath.Join(w1.WorktreePath, file)); err != nil {
		t.Fatalf("the spec is in the worker's tree: %v", err)
	}
	typeLine(t, e, w1.ID, "write internal/store/tags.sql CREATE TABLE tags;")
	typeLine(t, e, w1.ID, "commitall")
	screen(t, e, w1.ID, "committed all")
	if _, err := tm.ReportDone(ctx, w1.ID, mcp.ReportDoneInput{Summary: "table"}); err != nil {
		t.Fatal(err)
	}
	// The lead is told what the report made possible.
	if m := messagesTo(t, e, lead); len(m) == 0 || !strings.Contains(m[0].Body, "Now ready to assign") ||
		!strings.Contains(m[0].Body, byKey["api"].ID) {
		t.Fatalf("report to the lead: %+v", m)
	}
	// Now the dependent sub-task starts, from its dependency's work.
	a2, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: byKey["api"].ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a2.AgentID, hooks.Idle))
	w2, _ := e.Get(a2.AgentID)
	if b, err := os.ReadFile(filepath.Join(w2.WorktreePath, "internal/store/tags.sql")); err != nil ||
		!strings.Contains(string(b), "CREATE TABLE") {
		t.Fatalf("the dependent worker starts from its dependency: %v", err)
	}
}

// When the plan breaks, the Tech Lead escalates to the architect, which is
// started again with the approved plan; a revision approved by the user
// changes only sub-tasks nobody worked on yet.
func TestEscalationRevisesThePlan(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 5
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	tm := e.Team()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	arch, _ := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	out, err := tm.SubmitPlan(ctx, arch.ID, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ApproveSpec(ctx, out.SpecID)
	if err != nil || res.Lead == nil {
		t.Fatalf("approve %+v %v", res, err)
	}
	lead := res.Lead.ID
	log.wait(t, 5*time.Second, agentState(arch.ID, hooks.Exited))
	log.wait(t, startWait, agentState(lead, hooks.Idle))
	ids := map[string]string{}
	tasks, _ := e.ListTasks(ctx)
	for _, x := range tasks {
		ids[x.SpecKey] = x.ID
	}
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: ids["schema"]}); err != nil {
		t.Fatal(err)
	}

	// The lead escalates: the exited architect starts again, same agent.
	mark := log.mark()
	sent, err := tm.SendMessage(ctx, lead, mcp.SendMessageInput{To: "architect", Body: "handler.go does not exist"})
	if err != nil || sent.To != arch.ID {
		t.Fatalf("escalation: %+v %v", sent, err)
	}
	log.waitFrom(t, startWait, mark, agentState(arch.ID, hooks.Idle))
	screen(t, e, arch.ID, "The user approved version 1 of your plan earlier")
	typeLine(t, e, arch.ID, "turn") // its first turn ends: the lead's message follows on its Stop
	screen(t, e, arch.ID, "handler.go does not exist")

	// A revision: drop "docs", rename "api", add "cli". "schema" has a worker.
	rev := samplePlan()
	rev.SubTasks = rev.SubTasks[:2]
	rev.SubTasks[0].Title = "Tags table v2"
	rev.SubTasks[1].Title = "Tag filter v2"
	rev.SubTasks = append(rev.SubTasks, mcp.PlanSubTask{Key: "cli", Title: "Tag flag", DependsOn: []string{"api"}})
	out2, err := tm.SubmitPlan(ctx, arch.ID, rev)
	if err != nil || out2.Version != 2 {
		t.Fatalf("revision %+v %v", out2, err)
	}
	res2, err := e.ApproveSpec(ctx, out2.SpecID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res2.Deleted, []string{ids["docs"]}) || !slices.Equal(res2.Kept, []string{ids["schema"]}) ||
		!slices.Equal(res2.Updated, []string{ids["api"]}) || len(res2.Created) != 1 || res2.Lead == nil || res2.Lead.ID != lead {
		t.Fatalf("revision result %+v (ids %v)", res2, ids)
	}
	if s, _ := e.getTask(ctx, ids["schema"]); s.Title != "Tags table" {
		t.Fatalf("a started sub-task changed: %q", s.Title)
	}
	if s, _ := e.getTask(ctx, ids["api"]); s.Title != "Tag filter v2" {
		t.Fatalf("an untouched sub-task kept its old title: %q", s.Title)
	}
	typeLine(t, e, lead, "turn") // the lead's first turn ends; agent-office's message follows
	screen(t, e, lead, "approved version 2 of the plan")
	// The spec branch has both versions, the second on top of the first.
	if p := gitOutIn(t, f.repo, "rev-parse", res2.Spec.Branch+"^"); p != res.Spec.Commit {
		t.Fatalf("version 2's parent %s, version 1 %s", p, res.Spec.Commit)
	}
}

// A revision that drops a sub-task already worked on, with nobody running on
// it, closes it as superseded by the new version (ADR 0062).
func TestRevisionSupersedesDroppedWork(t *testing.T) {
	f := newFixture(t)
	f.opts.MaxAgents = 5
	gitIn(t, f.repo, "config", "user.name", "Ozan")
	gitIn(t, f.repo, "config", "user.email", "o@example.com")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	arch, _ := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	s1, err := e.saveSpec(ctx, mustTask(t, e, goal.ID), arch.ID, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApproveSpec(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	tasks, _ := e.ListTasks(ctx)
	for _, x := range tasks {
		ids[x.SpecKey] = x.ID
	}
	// "docs" was worked on (in progress), its worker is gone.
	if _, err := e.UpdateTask(ctx, ids["docs"], TaskPatch{Status: Ptr(StatusInProgress)}); err != nil {
		t.Fatal(err)
	}
	rev := samplePlan()
	rev.SubTasks = rev.SubTasks[:2]
	s2, err := e.EditSpec(ctx, s1.ID, rev)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ApproveSpec(ctx, s2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Superseded, []string{ids["docs"]}) || len(res.Deleted) != 0 {
		t.Fatalf("revision result %+v", res)
	}
	d, _ := e.getTask(ctx, ids["docs"])
	if d.Status != StatusDone || d.SupersededBy != "spec:2" {
		t.Fatalf("dropped sub-task %+v", taskFromRow(d))
	}
}

func mustTask(t *testing.T, e *Engine, id string) db.Task {
	t.Helper()
	r, err := e.getTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
