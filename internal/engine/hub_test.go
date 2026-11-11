package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
)

// testDefaults are daemon flags as project settings, every field set.
func testDefaults() ProjectSettings {
	d := NeutralDefaults()
	d.Model = Ptr("haiku")
	return d
}

// hubFixture is a hub with two projects, A (the fixture's repository,
// registered like `serve --repo`) and B (added like `project add`).
type hubFixture struct {
	f    *fixture
	h    *Hub
	a, b ProjectInfo
	ea   *Engine
	eb   *Engine
}

func newHubFixture(t *testing.T, maxAgents int, bSettings ProjectSettings, extraEnv ...string) *hubFixture {
	t.Helper()
	f := newFixture(t, extraEnv...)
	f.opts.MaxAgents = maxAgents
	hf := &hubFixture{f: f}
	hf.h = hf.openHub(t)
	ctx := context.Background()
	var err error
	if hf.a, err = hf.h.Register(ctx, f.repo); err != nil {
		t.Fatal(err)
	}
	if hf.b, err = hf.h.Add(ctx, AddProject{Repo: newRepo(t), Name: "webapp", Settings: bSettings}); err != nil {
		t.Fatal(err)
	}
	hf.ea, hf.eb = hf.engines(t)
	return hf
}

func (hf *hubFixture) openHub(t *testing.T) *Hub {
	t.Helper()
	h, err := NewHub(context.Background(), HubOptions{Base: hf.f.opts, Defaults: testDefaults()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Shutdown)
	return h
}

func (hf *hubFixture) engines(t *testing.T) (*Engine, *Engine) {
	t.Helper()
	ea, err := hf.h.Engine(hf.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := hf.h.Engine(hf.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ea, eb
}

func TestHubRegistersAndListsProjects(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{Model: Ptr("sonnet")})
	ps := hf.h.Projects()
	if len(ps) != 2 || ps[0].ID != hf.a.ID || ps[1].ID != hf.b.ID {
		t.Fatalf("projects %+v", ps)
	}
	if !ps[0].Default || ps[1].Default || ps[1].Name != "webapp" || ps[0].State != ProjectOpen {
		t.Fatalf("default, name or state: %+v", ps)
	}
	if *ps[0].Effective.Model != "haiku" || *ps[1].Effective.Model != "sonnet" || ps[0].Settings.Model != nil {
		t.Fatalf("effective models %+v / %+v", ps[0].Effective, ps[1].Effective)
	}
	if *ps[1].Effective.MaxAgents != 4 {
		t.Fatalf("a project's cap is at most the daemon's: %d", *ps[1].Effective.MaxAgents)
	}
	// Registering again changes nothing; adding the same repository again is refused.
	if again, err := hf.h.Register(context.Background(), hf.f.repo); err != nil || again.ID != hf.a.ID {
		t.Fatalf("register twice: %+v %v", again, err)
	}
	if _, err := hf.h.Add(context.Background(), AddProject{Repo: hf.f.repo}); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("add twice: %v", err)
	}
	if p, err := hf.h.Find("WEBAPP"); err != nil || p.ID != hf.b.ID {
		t.Fatalf("find by name: %+v %v", p, err)
	}
	if hf.ea == hf.eb || hf.ea.Project().ID == hf.eb.Project().ID {
		t.Fatal("projects share an engine")
	}
}

func TestHubModelsPerProject(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{Model: Ptr("sonnet"), RoleModels: map[string]string{RoleTechLead: "opus"}})
	ctx := context.Background()
	wa, err := hf.ea.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wb, err := hf.eb.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	goal, _ := hf.eb.CreateTask(ctx, TaskInput{Title: "Goal"})
	lb, err := hf.eb.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead})
	if err != nil {
		t.Fatal(err)
	}
	if wa.Model != "haiku" || wb.Model != "sonnet" || lb.Model != "opus" {
		t.Fatalf("models: A worker %q, B worker %q, B lead %q", wa.Model, wb.Model, lb.Model)
	}
}

func TestHubCapAcrossProjects(t *testing.T) {
	hf := newHubFixture(t, 3, ProjectSettings{})
	ctx := context.Background()
	a1, err := hf.ea.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.ea.Start(ctx, StartRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := hf.eb.Start(ctx, StartRequest{}); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]*Engine{"A": hf.ea, "B": hf.eb} {
		_, err := e.Start(ctx, StartRequest{})
		if !errors.Is(err, ErrTooManyAgents) || !strings.Contains(err.Error(), "daemon-wide cap of 3") {
			t.Fatalf("project %s past the daemon-wide cap: %v", name, err)
		}
	}
	if st := hf.h.Status(); st.Running != 3 || st.MaxAgents != 3 {
		t.Fatalf("status %+v", st)
	}
	log := record(t, hf.ea)
	if err := hf.ea.Stop(a1.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(a1.ID, hooks.Exited))
	if _, err := hf.eb.Start(ctx, StartRequest{}); err != nil {
		t.Fatalf("a slot freed in A is usable in B: %v", err)
	}
}

func TestHubCapHoldsUnderConcurrentStarts(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{})
	ctx := context.Background()
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		e := hf.ea
		if i%2 == 1 {
			e = hf.eb
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Start(ctx, StartRequest{})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrTooManyAgents):
				refused.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 4 || refused.Load() != 8 {
		t.Fatalf("%d started, %d refused; want 4 and 8", ok.Load(), refused.Load())
	}
	if n := len(running(hf.ea)) + len(running(hf.eb)); n != 4 {
		t.Fatalf("%d agents run", n)
	}
}

func running(e *Engine) []AgentInfo {
	var out []AgentInfo
	for _, a := range e.List() {
		if a.ExitedAt == nil {
			out = append(out, a)
		}
	}
	return out
}

func TestHubCapCountsResumes(t *testing.T) {
	hf := newHubFixture(t, 2, ProjectSettings{})
	ctx := context.Background()
	log := record(t, hf.ea)
	task, _ := hf.ea.CreateTask(ctx, TaskInput{Title: "Talk"}) // a task: its prompt is a conversation to resume
	w, err := hf.ea.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(w.ID, hooks.Idle))
	if err := hf.ea.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	for range 2 {
		if _, err := hf.eb.Start(ctx, StartRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hf.ea.Resume(ctx, w.ID, ""); !errors.Is(err, ErrTooManyAgents) {
		t.Fatalf("resume past the daemon-wide cap: %v", err)
	}
}

func TestHubProjectCap(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{MaxAgents: Ptr(1)})
	ctx := context.Background()
	if _, err := hf.eb.Start(ctx, StartRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := hf.eb.Start(ctx, StartRequest{}); !errors.Is(err, ErrTooManyAgents) || !strings.Contains(err.Error(), "this project's cap of 1") {
		t.Fatalf("past the project's cap: %v", err)
	}
	for range 3 {
		if _, err := hf.ea.Start(ctx, StartRequest{}); err != nil {
			t.Fatalf("A has its own room under the daemon's cap: %v", err)
		}
	}
}

func TestHubOneGoalPerProject(t *testing.T) {
	hf := newHubFixture(t, 6, ProjectSettings{})
	ctx := context.Background()
	for _, e := range []*Engine{hf.ea, hf.eb} {
		goal, _ := e.CreateTask(ctx, TaskInput{Title: "Goal"})
		if _, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead}); err != nil {
			t.Fatalf("a Tech Lead per project: %v", err)
		}
		other, _ := e.CreateTask(ctx, TaskInput{Title: "Second goal"})
		if _, err := e.Start(ctx, StartRequest{TaskID: other.ID, Role: RoleTechLead}); !errors.Is(err, ErrLeadRunning) {
			t.Fatalf("a second Tech Lead in one project: %v", err)
		}
	}
}

func TestHubRemoveAndAddAgain(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{Model: Ptr("sonnet")})
	ctx := context.Background()
	task, _ := hf.eb.CreateTask(ctx, TaskInput{Title: "Kept"})
	w, err := hf.eb.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	updates, cancel := hf.h.Subscribe()
	defer cancel()
	if err := hf.h.Remove(ctx, hf.b.ID); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("remove with a running agent: %v", err)
	}
	log := record(t, hf.eb)
	if err := hf.eb.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	if err := hf.h.Remove(ctx, hf.b.ID); err != nil {
		t.Fatal(err)
	}
	if u := <-updates; u.Type != "project_removed" || u.ID != hf.b.ID {
		t.Fatalf("update %+v", u)
	}
	if _, err := hf.h.Engine(hf.b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("engine of a removed project: %v", err)
	}
	if _, err := hf.eb.Start(ctx, StartRequest{}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("a removed project's engine starts agents: %v", err)
	}
	if ps := hf.h.Projects(); len(ps) != 1 || ps[0].ID != hf.a.ID {
		t.Fatalf("projects after remove %+v", ps)
	}
	// Adding the repository again brings the project back with its history and settings.
	back, err := hf.h.Add(ctx, AddProject{Repo: hf.b.RepoPath})
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != hf.b.ID || back.Name != "webapp" || *back.Effective.Model != "sonnet" {
		t.Fatalf("restored project %+v", back)
	}
	eb, err := hf.h.Engine(back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tasks, _ := eb.ListTasks(ctx); len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("tasks after re-adding %+v", tasks)
	}
	if a, err := eb.Get(w.ID); err != nil || a.ExitReason != ExitStopped {
		t.Fatalf("agent history after re-adding %+v %v", a, err)
	}
}

func TestHubUpdateSettings(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{})
	ctx := context.Background()
	w, err := hf.eb.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.h.Update(ctx, hf.b.ID, UpdateProject{Settings: &ProjectSettings{Model: Ptr("opus")}}); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("settings change with a running agent: %v", err)
	}
	// A rename needs no reopen.
	if p, err := hf.h.Update(ctx, hf.b.ID, UpdateProject{Name: Ptr("Web app")}); err != nil || p.Name != "Web app" {
		t.Fatalf("rename: %+v %v", p, err)
	}
	if _, err := hf.h.Update(ctx, hf.b.ID, UpdateProject{Settings: &ProjectSettings{MaxAgents: Ptr(0)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid settings: %v", err)
	}
	log := record(t, hf.eb)
	if err := hf.eb.Stop(w.ID); err != nil {
		t.Fatal(err)
	}
	log.wait(t, startWait, agentState(w.ID, hooks.Exited))
	p, err := hf.h.Update(ctx, hf.b.ID, UpdateProject{Settings: &ProjectSettings{Model: Ptr("opus")}})
	if err != nil || *p.Effective.Model != "opus" || p.Name != "Web app" {
		t.Fatalf("settings change: %+v %v", p, err)
	}
	eb, _ := hf.h.Engine(hf.b.ID)
	if eb == hf.eb {
		t.Fatal("new settings did not reopen the project")
	}
	if a, err := eb.Start(ctx, StartRequest{}); err != nil || a.Model != "opus" {
		t.Fatalf("start after the change: %+v %v", a, err)
	}
	// The settings are stored: a new hub (a restart) opens the project with them.
	hf.h.Shutdown()
	h2 := hf.openHub(t)
	ps := h2.Projects()
	if len(ps) != 2 || *ps[1].Effective.Model != "opus" || ps[1].Name != "Web app" {
		t.Fatalf("after a restart %+v", ps)
	}
}

func TestHubKeepsAFailingProject(t *testing.T) {
	f := newFixture(t)
	hf := &hubFixture{f: f}
	h := hf.openHub(t)
	ctx := context.Background()
	gone := newRepo(t)
	b, err := h.Add(ctx, AddProject{Repo: gone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Register(ctx, f.repo); err != nil {
		t.Fatal(err)
	}
	h.Shutdown()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	h2 := hf.openHub(t)
	ps := h2.Projects()
	if len(ps) != 2 || ps[0].ID != b.ID || ps[0].State != ProjectError || ps[0].Error == "" || ps[1].State != ProjectOpen {
		t.Fatalf("projects %+v", ps)
	}
	if _, err := h2.Engine(b.ID); !errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("engine of a failed project: %v", err)
	}
	if p, _ := h2.Retry(ctx, b.ID); p.State != ProjectError {
		t.Fatalf("retry of a project whose repository is gone: %+v", p)
	}
	if err := h2.Remove(ctx, b.ID); err != nil {
		t.Fatalf("remove a failed project: %v", err)
	}
	// Adding a repository that cannot open registers nothing.
	if _, err := h2.Add(ctx, AddProject{Repo: t.TempDir()}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add a directory that is no repository: %v", err)
	}
	if ps := h2.Projects(); len(ps) != 1 {
		t.Fatalf("projects %+v", ps)
	}
}

func TestHubRestoresEveryProject(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{})
	hf.f.opts.RestoreAgents = true
	hf.h.Shutdown()
	hf.h = hf.openHub(t)
	hf.ea, hf.eb = hf.engines(t)
	ctx := context.Background()
	var ws []AgentInfo
	for _, e := range []*Engine{hf.ea, hf.eb} {
		log := record(t, e)
		task, _ := e.CreateTask(ctx, TaskInput{Title: "Restore me"})
		w, err := e.Start(ctx, StartRequest{TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		log.wait(t, startWait, agentState(w.ID, hooks.Idle))
		ws = append(ws, w)
	}
	hf.h.Shutdown() // a daemon stop: both projects keep what a restore needs (ADR 0035)
	h2 := hf.openHub(t)
	for i, id := range []string{hf.a.ID, hf.b.ID} {
		e, err := h2.Engine(id)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the restore of project "+string(rune('A'+i)), func() bool {
			a, _ := e.Get(ws[i].ID)
			return a.State == string(hooks.Idle) && a.ResumedFrom == ws[i].SessionID
		})
	}
	if st := h2.Status(); st.Running != 2 {
		t.Fatalf("running after the restore %+v", st)
	}
}

func TestHubCrashMarksOnlyItsProjects(t *testing.T) {
	hf := newHubFixture(t, 4, ProjectSettings{})
	ctx := context.Background()
	a, err := hf.ea.Start(ctx, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// A project added while A runs reconciles itself only: A's agent keeps running.
	c, err := hf.h.Add(ctx, AddProject{Repo: newRepo(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.h.Engine(c.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := hf.f.store.DB.QueryRow(`SELECT state FROM agents WHERE id = ?`, a.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if got, _ := hf.ea.Get(a.ID); got.ExitedAt != nil || !got.HasTerminal || state == string(hooks.Exited) {
		t.Fatalf("adding a project ended another project's agent: %+v, stored state %s", got, state)
	}
}
