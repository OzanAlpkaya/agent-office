package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
)

func newGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func jsonBody(t *testing.T, r *http.Response, want int, v any) {
	t.Helper()
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", r.StatusCode, want, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
}

var jsonCT = map[string]string{"Content-Type": "application/json"}

// addProject adds a second project over the API and returns it.
func (f *fixture) addProject(t *testing.T, body string) engine.ProjectInfo {
	t.Helper()
	var p engine.ProjectInfo
	jsonBody(t, f.doBody(t, "POST", "/api/projects", jsonCT, body), http.StatusCreated, &p)
	return p
}

func TestProjectsAPI(t *testing.T) {
	f := newFixture(t)
	var list []engine.ProjectInfo
	jsonBody(t, f.do(t, "GET", "/api/projects", nil), http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != f.pid || !list[0].Default || list[0].State != engine.ProjectOpen {
		t.Fatalf("projects %+v", list)
	}
	repo := newGitRepo(t)
	b := f.addProject(t, fmt.Sprintf(`{"repo": %q, "name": "webapp", "settings": {"model": "sonnet", "max_agents": 1}}`, repo))
	if b.Name != "webapp" || *b.Effective.Model != "sonnet" || *b.Settings.MaxAgents != 1 || b.Default {
		t.Fatalf("added %+v", b)
	}
	// Refusals: the same repository, an unknown setting, a bad value, no repository.
	for body, want := range map[string]int{
		fmt.Sprintf(`{"repo": %q}`, repo):                                 http.StatusConflict,
		fmt.Sprintf(`{"repo": %q, "settings": {"modle": "x"}}`, repo):     http.StatusBadRequest,
		fmt.Sprintf(`{"repo": %q, "settings": {"max_agents": 99}}`, repo): http.StatusBadRequest,
		fmt.Sprintf(`{"repo": %q}`, t.TempDir()):                          http.StatusBadRequest,
		`{"repo": "relative/path"}`:                                       http.StatusBadRequest,
	} {
		if r := f.doBody(t, "POST", "/api/projects", jsonCT, body); r.StatusCode != want {
			t.Errorf("add %s: %d, want %d", body, r.StatusCode, want)
		}
	}
	var got engine.ProjectInfo
	jsonBody(t, f.doBody(t, "PATCH", "/api/projects/"+b.ID, jsonCT, `{"name": "Web app", "settings": {"model": "opus"}}`),
		http.StatusOK, &got)
	if got.Name != "Web app" || *got.Effective.Model != "opus" || got.Settings.MaxAgents != nil {
		t.Fatalf("patched %+v", got)
	}
	var st struct {
		Sandbox   string `json:"sandbox"`
		MaxAgents int    `json:"max_agents"`
		Default   string `json:"default_project"`
		Version   string `json:"version"`
	}
	jsonBody(t, f.do(t, "GET", "/api/status", nil), http.StatusOK, &st)
	if st.Sandbox != engine.SandboxHost || st.MaxAgents != 1 || st.Default != f.pid || st.Version != "test" {
		t.Fatalf("daemon status %+v", st)
	}
	var u engine.DaemonUsage
	jsonBody(t, f.do(t, "GET", "/api/usage", nil), http.StatusOK, &u)
	if len(u.Projects) != 2 || u.Projects[1].ProjectID != b.ID {
		t.Fatalf("usage %+v", u)
	}
	if r := f.do(t, "GET", "/api/projects/nope/tasks", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d", r.StatusCode)
	}
	if r := f.do(t, "DELETE", "/api/projects/"+b.ID, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	if r := f.do(t, "GET", "/api/projects/"+b.ID+"/tasks", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a removed project's routes: %d", r.StatusCode)
	}
}

func TestRemoveProjectWithRunningAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	r := f.do(t, "DELETE", "/api/projects/"+f.pid, nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("remove with a running agent: %d", r.StatusCode)
	}
}

// TestProjectRoutesAreIsolated: one project's ids under another project's
// prefix reach nothing (ADR 0046).
func TestProjectRoutesAreIsolated(t *testing.T) {
	f := newFixture(t)
	b := f.addProject(t, fmt.Sprintf(`{"repo": %q}`, newGitRepo(t)))
	bp := "/api/projects/" + b.ID
	var task engine.Task
	jsonBody(t, f.doBody(t, "POST", bp+"/tasks", jsonCT, `{"title": "B's task"}`), http.StatusCreated, &task)
	var mem engine.MemoryEntry
	jsonBody(t, f.doBody(t, "POST", bp+"/memory", jsonCT, `{"kind": "convention", "title": "B's note", "body": "only B"}`),
		http.StatusCreated, &mem)
	var agent engine.AgentInfo
	jsonBody(t, f.do(t, "POST", bp+"/agents", nil), http.StatusCreated, &agent)

	var tasks []engine.Task
	jsonBody(t, f.do(t, "GET", f.pp+"/tasks", nil), http.StatusOK, &tasks)
	var agents []engine.AgentInfo
	jsonBody(t, f.do(t, "GET", f.pp+"/agents", nil), http.StatusOK, &agents)
	var memory []engine.MemoryEntry
	jsonBody(t, f.do(t, "GET", f.pp+"/memory", nil), http.StatusOK, &memory)
	if len(tasks) != 0 || len(agents) != 0 || len(memory) != 0 {
		t.Fatalf("A lists B's data: %d tasks, %d agents, %d memory entries", len(tasks), len(agents), len(memory))
	}
	mid := fmt.Sprint(mem.ID)
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/tasks/" + task.ID, `{"title": "taken"}`},
		{"DELETE", "/tasks/" + task.ID, ""},
		{"POST", "/agents", `{"task_id": "` + task.ID + `"}`},
		{"POST", "/agents/" + agent.ID + "/stop", ""},
		{"POST", "/agents/" + agent.ID + "/message", `{"body": "hi"}`},
		{"GET", "/agents/" + agent.ID + "/messages", ""},
		{"GET", "/agents/" + agent.ID + "/sessions", ""},
		{"GET", "/agents/" + agent.ID + "/sessions/" + agent.SessionID + "/terminal", ""},
		{"DELETE", "/agents/" + agent.ID, ""},
		{"PATCH", "/memory/" + mid, `{"title": "taken"}`},
		{"POST", "/memory/" + mid + "/accept", ""},
		{"DELETE", "/memory/" + mid, ""},
	} {
		hdr := map[string]string(nil)
		if c.body != "" {
			hdr = jsonCT
		}
		r := f.doBody(t, c.method, f.pp+c.path, hdr, c.body)
		if r.StatusCode != http.StatusNotFound {
			b, _ := io.ReadAll(r.Body)
			t.Errorf("%s %s under A: %d %s, want 404", c.method, c.path, r.StatusCode, b)
		}
	}
	// B's agent's terminal is not reachable under A either.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, f.ws+f.pp+"/agents/"+agent.ID+"/terminal", f.dialOpts()); err == nil ||
		resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B's terminal under A: err=%v resp=%v", err, resp)
	}
	// Nothing of B changed.
	var still engine.Task
	jsonBody(t, f.doBody(t, "PATCH", bp+"/tasks/"+task.ID, jsonCT, `{}`), http.StatusOK, &still)
	if still.Title != "B's task" {
		t.Fatalf("B's task %+v", still)
	}
}

// TestProjectEventsCarryOnlyTheirProject: each project's events stream
// carries its own updates, each tagged with its id; the daemon's stream
// carries the project list.
func TestProjectEventsCarryOnlyTheirProject(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub, _, err := websocket.Dial(ctx, f.ws+"/api/events", f.dialOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.CloseNow()
	read := func(c *websocket.Conn) map[string]json.RawMessage {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := read(hub); string(m["type"]) != `"projects"` || !strings.Contains(string(m["projects"]), f.pid) {
		t.Fatalf("daemon snapshot %v", m)
	}
	b := f.addProject(t, fmt.Sprintf(`{"repo": %q}`, newGitRepo(t)))
	if m := read(hub); string(m["type"]) != `"project"` || !strings.Contains(string(m["project"]), b.ID) {
		t.Fatalf("daemon update %v", m)
	}
	a, _, err := websocket.Dial(ctx, f.ws+f.pp+"/events", f.dialOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer a.CloseNow()
	if m := read(a); string(m["type"]) != `"snapshot"` || string(m["project_id"]) != `"`+f.pid+`"` {
		t.Fatalf("A's snapshot %v", m)
	}
	f.doBody(t, "POST", "/api/projects/"+b.ID+"/tasks", jsonCT, `{"title": "in B"}`).Body.Close()
	f.doBody(t, "POST", f.pp+"/tasks", jsonCT, `{"title": "in A"}`).Body.Close()
	m := read(a)
	if string(m["type"]) != `"task"` || string(m["project_id"]) != `"`+f.pid+`"` || !strings.Contains(string(m["task"]), "in A") {
		t.Fatalf("A's stream got %v", m)
	}
}
