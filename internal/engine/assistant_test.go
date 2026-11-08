package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// The assistant briefs from daemon facts and proposes memory that reaches
// other agents only once the user accepts it (DoD).
func TestAssistantBriefingAndMemory(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 5
	f.opts.deliverySettle = 50 * time.Millisecond
	f.opts.typingQuiet = 300 * time.Millisecond
	f.opts.enterDelay = 20 * time.Millisecond
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	tm := e.Team()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	arch, _ := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	if _, err := tm.MessageUser(ctx, arch.ID, mcp.MessageUserInput{Kind: "question", Body: "REST or gRPC?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.SubmitPlan(ctx, arch.ID, samplePlan()); err != nil {
		t.Fatal(err)
	}
	blocked := StatusBlocked
	other, _ := e.CreateTask(ctx, TaskInput{Title: "Old thing"})
	if _, err := e.UpdateTask(ctx, other.ID, TaskPatch{Status: &blocked}); err != nil {
		t.Fatal(err)
	}

	as, err := e.AskBriefing(ctx)
	if err != nil || as.Kind != KindAssistant || as.TaskID != "" {
		t.Fatalf("assistant %+v %v", as, err)
	}
	log.wait(t, startWait, agentState(as.ID, hooks.Idle))
	screen(t, e, as.ID, "call get_briefing and send the user your first briefing")
	if _, err := tm.GetBriefing(ctx, arch.ID); !isUser(err, ErrForbidden) {
		t.Fatalf("briefing for the architect: %v", err)
	}
	b, err := tm.GetBriefing(ctx, as.ID)
	if err != nil {
		t.Fatal(err)
	}
	waiting := strings.Join(b.WaitingOnYou, "\n")
	for _, want := range []string{"Approve or edit plan version 1", `"REST or gRPC?"`} {
		if !strings.Contains(waiting, want) {
			t.Errorf("waiting on the user lacks %q: %s", want, waiting)
		}
	}
	if !strings.Contains(strings.Join(b.Blocked, "\n"), `"Old thing" is marked blocked`) {
		t.Errorf("blocked %v", b.Blocked)
	}
	var g *mcp.GoalBrief
	for i := range b.Goals {
		if b.Goals[i].ID == goal.ID {
			g = &b.Goals[i]
		}
	}
	if g == nil || g.Plan != "version 1 waiting for approval" || len(b.Running) != 2 {
		t.Fatalf("briefing %+v", b)
	}

	// Memory: proposed by the assistant, invisible until the user accepts it.
	if _, err := tm.ProposeMemory(ctx, arch.ID, mcp.ProposeMemoryInput{Kind: "lesson", Title: "x", Body: "y"}); !isUser(err, ErrForbidden) {
		t.Fatalf("propose by the architect: %v", err)
	}
	if _, err := tm.ProposeMemory(ctx, as.ID, mcp.ProposeMemoryInput{Kind: "rumour", Title: "x", Body: "y"}); !isUser(err, ErrInvalid) {
		t.Fatalf("bad kind: %v", err)
	}
	p, err := tm.ProposeMemory(ctx, as.ID, mcp.ProposeMemoryInput{Kind: "convention", Title: "Errors are wrapped",
		Body: "Wrap errors with %w and the operation's name."})
	if err != nil || p.Status != MemoryProposed {
		t.Fatalf("propose %+v %v", p, err)
	}
	if s := e.memorySection(ctx, RoleWorker, nil, true); s != "" {
		t.Fatalf("a proposal reached the prompts: %q", s)
	}
	task, _ := e.CreateTask(ctx, TaskInput{Title: "Fix bug"})
	w1, _ := e.Start(ctx, StartRequest{TaskID: task.ID})
	log.wait(t, startWait, agentState(w1.ID, hooks.Idle))
	if strings.Contains(screen(t, e, w1.ID, "PROMPT:"), "Errors are wrapped") {
		t.Fatal("an unaccepted note reached an agent")
	}
	if _, err := e.UpdateMemory(ctx, p.ID, MemoryInput{Status: MemoryAccepted, Body: "Wrap errors with %w."}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UpdateMemory(ctx, p.ID, MemoryInput{Status: "proposed"}); err == nil {
		t.Fatal("un-accepting is not a status change")
	}
	own, err := e.AddMemory(ctx, MemoryInput{Kind: "decision", Title: "SQLite only", Body: "No other database."})
	if err != nil || own.Status != MemoryAccepted || own.Source != "" {
		t.Fatalf("user's own entry %+v %v", own, err)
	}
	task2, _ := e.CreateTask(ctx, TaskInput{Title: "Fix another bug"})
	w2, _ := e.Start(ctx, StartRequest{TaskID: task2.ID})
	log.wait(t, startWait, agentState(w2.ID, hooks.Idle))
	scr := screen(t, e, w2.ID, "Project memory (notes the user approved")
	if !strings.Contains(scr, "[convention] Errors are wrapped: Wrap errors with %w.") || !strings.Contains(scr, "SQLite only") {
		t.Fatalf("memory in the prompt:\n%s", scr)
	}
	list, _ := tm.ListMemory(ctx, as.ID)
	if len(list.Entries) != 2 {
		t.Fatalf("list_memory %+v", list)
	}
	if err := e.DeleteMemory(ctx, own.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.memorySection(ctx, RoleWorker, nil, true), "SQLite only") {
		t.Fatal("a deleted note stays in the prompts")
	}

	// A second briefing request goes to the running assistant as the user's message.
	typeLine(t, e, as.ID, "turn")
	again, err := e.AskBriefing(ctx)
	if err != nil || again.ID != as.ID {
		t.Fatalf("second briefing %+v %v", again, err)
	}
	screen(t, e, as.ID, "Please give me a new briefing.")
}

func TestMemorySectionIsCapped(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := e.AddMemory(ctx, MemoryInput{Kind: "lesson", Title: "Lesson", Body: strings.Repeat("x", 1500)}); err != nil {
			t.Fatal(err)
		}
	}
	s := e.memorySection(ctx, RoleWorker, nil, true)
	if len(s) > maxMemoryPrompt+100 || !strings.Contains(s, "more note(s) left out for length; list_memory shows every note") {
		t.Fatalf("section of %d bytes", len(s))
	}
}

// Relevant memory (ADR 0037): entries scoped to a role or to files reach only
// agents they apply to, before the unscoped ones; list_memory shows every
// accepted entry to every behaviour but reviewers, marked whether it applies.
func TestMemoryRelevance(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	add := func(title string, roles, paths []string) MemoryEntry {
		t.Helper()
		m, err := e.AddMemory(ctx, MemoryInput{Kind: MemoryConvention, Title: title, Body: title + " body.", Roles: &roles, Paths: &paths})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	add("General", nil, nil)
	add("For QA", []string{RoleQA}, nil)
	add("Store code", nil, []string{"internal/store/"})
	add("Workers on the web", []string{RoleWorker}, []string{"web/**"})
	if _, err := e.AddMemory(ctx, MemoryInput{Kind: MemoryLesson, Title: "x", Body: "y", Roles: &[]string{"no-such-role"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown role: %v", err)
	}
	if _, err := e.AddMemory(ctx, MemoryInput{Kind: MemoryLesson, Title: "x", Body: "y", Paths: &[]string{"../etc"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad path: %v", err)
	}

	s := e.memorySection(ctx, RoleWorker, []string{"internal/store/db.go"}, true)
	if !strings.Contains(s, "Store code") || !strings.Contains(s, "General") || strings.Contains(s, "For QA") ||
		strings.Contains(s, "Workers on the web") || strings.Index(s, "Store code") > strings.Index(s, "General") ||
		!strings.Contains(s, "2 note(s) for other roles or files not shown; list_memory shows every note") {
		t.Fatalf("store worker's section:\n%s", s)
	}
	if s := e.memorySection(ctx, RoleWorker, []string{"web/src/App.tsx"}, false); !strings.Contains(s, "Workers on the web") ||
		strings.Contains(s, "list_memory") {
		t.Fatalf("web worker's section:\n%s", s)
	}
	if s := e.memorySection(ctx, RoleQA, nil, true); !strings.Contains(s, "For QA") || strings.Contains(s, "Store code") {
		t.Fatalf("QA's section:\n%s", s)
	}

	// A sub-task's declared files pick its notes at start.
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Goal"})
	sub, err := e.createTask(ctx, TaskInput{Title: "Store work", Files: []string{"internal/store/queries.sql"}}, goal.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.Start(ctx, StartRequest{TaskID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 5*time.Second, agentState(w.ID, hooks.Idle))
	scr := screen(t, e, w.ID, "PROMPT:")
	if !strings.Contains(scr, "Store code body.") || strings.Contains(scr, "For QA body.") {
		t.Fatalf("sub-task prompt:\n%s", scr)
	}
	tm := team{e}
	list, err := tm.ListMemory(ctx, w.ID)
	if err != nil || len(list.Entries) != 4 {
		t.Fatalf("worker's list_memory %+v %v", list, err)
	}
	for _, m := range list.Entries {
		want := m.Title == "General" || m.Title == "Store code"
		if m.AppliesToYou != want || m.Status != MemoryAccepted {
			t.Errorf("%s: applies %v", m.Title, m.AppliesToYou)
		}
	}

	// Export: accepted entries as Markdown, with their scope.
	md, err := e.MemoryMarkdown(ctx)
	if err != nil || !strings.Contains(md, "## Conventions") || !strings.Contains(md, "### Store code") ||
		!strings.Contains(md, "_Applies to files: internal/store/._") || !strings.Contains(md, "roles: worker; files: web/**") {
		t.Fatalf("export:\n%s %v", md, err)
	}

	// PATCH keeps an absent scope and clears an empty one.
	m := add("Scoped", []string{RoleWorker}, nil)
	m2, err := e.UpdateMemory(ctx, m.ID, MemoryInput{Body: "Changed."})
	if err != nil || len(m2.Roles) != 1 {
		t.Fatalf("scope kept %+v %v", m2, err)
	}
	m3, err := e.UpdateMemory(ctx, m.ID, MemoryInput{Roles: &[]string{}})
	if err != nil || len(m3.Roles) != 0 {
		t.Fatalf("scope cleared %+v %v", m3, err)
	}
}
