package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// A lead without an architect plans dependencies and files with create_task;
// assign_task enforces them (DoD): overlapping running sub-tasks are refused,
// and a sub-task with two dependencies starts from a merge of their work.
func TestDependenciesAndFileOwnership(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	e, tm, lead := tf.e, tf.team, tf.lead.ID
	gitIn(t, tf.f.repo, "config", "user.name", "Ozan")
	gitIn(t, tf.f.repo, "config", "user.email", "o@example.com")

	if _, err := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "x", DependsOn: []string{"nope"}}); !isUser(err, ErrInvalid) {
		t.Fatalf("unknown dependency: %v", err)
	}
	if _, err := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "x", Files: []string{"/etc/passwd"}}); !isUser(err, ErrInvalid) {
		t.Fatalf("absolute file: %v", err)
	}
	if _, err := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "x", Role: "qa"}); !isUser(err, ErrInvalid) {
		t.Fatalf("a non-worker role: %v", err)
	}
	a, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "Store", Files: []string{"internal/store/"}})
	b, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "Store index", Files: []string{"internal/store/index.go"}})
	c, _ := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "API", Files: []string{"internal/api/"}})
	d, err := tm.CreateTask(ctx, lead, mcp.CreateTaskInput{Title: "Wire up", DependsOn: []string{a.ID, c.ID},
		Files: []string{"cmd/"}, Acceptance: []string{"it builds"}})
	if err != nil || len(d.DependsOn) != 2 {
		t.Fatalf("dependent task %+v %v", d, err)
	}

	start := func(id string) AgentInfo {
		t.Helper()
		out, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: id})
		if err != nil {
			t.Fatal(err)
		}
		tf.log.wait(t, startWait, agentState(out.AgentID, hooks.Idle))
		w, _ := e.Get(out.AgentID)
		return w
	}
	wa := start(a.ID)
	// b's files are inside a's directory, and neither depends on the other.
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: b.ID}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "internal/store/index.go") {
		t.Fatalf("overlapping files: %v", err)
	}
	wc := start(c.ID) // independent files run in parallel
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: d.ID}); !isUser(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "waits for") {
		t.Fatalf("dependencies not reported: %v", err)
	}
	report := func(w AgentInfo, file, text string) {
		t.Helper()
		typeLine(t, e, w.ID, "write "+file+" "+text)
		typeLine(t, e, w.ID, "commitall")
		screen(t, e, w.ID, "committed all")
		if _, err := tm.ReportDone(ctx, w.ID, mcp.ReportDoneInput{Summary: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	report(wa, "internal/store/tags.go", "package store")
	report(wc, "internal/api/tags.go", "package api")
	wd := start(d.ID)
	for _, f := range []string{"internal/store/tags.go", "internal/api/tags.go"} {
		if _, err := os.Stat(filepath.Join(wd.WorktreePath, f)); err != nil {
			t.Errorf("the dependent worker lacks %s: %v", f, err)
		}
	}
	if parents := gitIn(t, tf.f.repo, "log", "-1", "--format=%P", wd.Branch); len(strings.Fields(parents)) != 2 {
		t.Fatalf("base of two dependencies is a merge: %q", parents)
	}
	// b no longer overlaps a running sub-task: a has reported.
	typeLine(t, e, wa.ID, "stop")
	tf.log.wait(t, startWait, agentState(wa.ID, hooks.Exited))
	if _, err := tm.AssignTask(ctx, lead, mcp.AssignTaskInput{TaskID: b.ID}); err != nil {
		t.Fatalf("after the overlapping sub-task ended: %v", err)
	}
}
