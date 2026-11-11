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
	"github.com/OzanAlpkaya/agent-office/internal/office"
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
	if m := read(hub); string(m["type"]) != `"presence"` {
		t.Fatalf("presence after the snapshot %v", m)
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

// A remote device may manage projects, but not add a repository or widen
// what agents reach (found in the Phase 14 security review).
func TestRemoteDevicesCannotWidenProjects(t *testing.T) {
	f := remoteFixture(t)
	viaServe := map[string]string{"Host": tailnetHost, "Tailscale-User-Login": "ozan@github", "Content-Type": "application/json",
		"Origin": "https://" + tailnetHost}
	repo := newGitRepo(t)
	if r := f.doBody(t, "POST", "/api/projects", viaServe, fmt.Sprintf(`{"repo": %q}`, repo)); r.StatusCode != http.StatusForbidden {
		t.Fatalf("remote add: %d", r.StatusCode)
	}
	for _, body := range []string{`{"settings": {"egress_allow": ["evil.example.com"]}}`, `{"settings": {"github": true}}`,
		`{"settings": {"pack": "node"}}`, `{"settings": {"secrets_dir": "/"}}`} {
		r := f.doBody(t, "PATCH", "/api/projects/"+f.pid, viaServe, body)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "need this machine") {
			t.Errorf("remote %s: %d %s", body, r.StatusCode, b)
		}
	}
	var p engine.ProjectInfo
	jsonBody(t, f.doBody(t, "PATCH", "/api/projects/"+f.pid, viaServe, `{"name": "renamed", "settings": {"model": "sonnet"}}`), http.StatusOK, &p)
	if p.Name != "renamed" || *p.Effective.Model != "sonnet" {
		t.Fatalf("remote rename and model: %+v", p)
	}
	// From this machine all of it works.
	local := map[string]string{"Content-Type": "application/json"}
	jsonBody(t, f.doBody(t, "POST", "/api/projects", local, fmt.Sprintf(`{"repo": %q}`, repo)), http.StatusCreated, nil)
}

// TestOfficeLayoutsAPI: the presets, the preview's check, and a project's
// layout through PATCH (Phase 18, ADR 0057).
func TestOfficeLayoutsAPI(t *testing.T) {
	f := newFixture(t)
	var presets []office.Preset
	jsonBody(t, f.do(t, "GET", "/api/office-layouts", nil), http.StatusOK, &presets)
	if len(presets) != 4 || presets[0].Name != "classic" || !strings.Contains(presets[0].YAML, "version: 1") {
		t.Fatalf("presets %+v", presets)
	}
	type check struct {
		Layout   *office.Layout `json:"layout"`
		Problems []string       `json:"problems"`
	}
	var ok check
	body, _ := json.Marshal(map[string]string{"yaml": presets[0].YAML})
	jsonBody(t, f.doBody(t, "POST", "/api/office-layouts/check", jsonCT, string(body)), http.StatusOK, &ok)
	if ok.Layout == nil || ok.Layout.Desks != 12 || len(ok.Problems) != 0 {
		t.Fatalf("check of a preset %+v", ok)
	}
	var bad check
	jsonBody(t, f.doBody(t, "POST", "/api/office-layouts/check", jsonCT, `{"yaml": "version: 1\nrooms: []\n"}`), http.StatusOK, &bad)
	if bad.Layout != nil || len(bad.Problems) != 4 {
		t.Fatalf("check of an empty layout %+v", bad)
	}
	if r := f.doBody(t, "POST", "/api/office-layouts/check", jsonCT, `{"yml": ""}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", r.StatusCode)
	}
	// Saved through the project's settings; an invalid one is refused with its problems.
	settings, _ := json.Marshal(map[string]any{"settings": map[string]string{"office_layout": presets[1].YAML}})
	var p engine.ProjectInfo
	jsonBody(t, f.doBody(t, "PATCH", "/api/projects/"+f.pid, jsonCT, string(settings)), http.StatusOK, &p)
	if p.Office.Source != engine.OfficeCustom || p.Office.Layout == nil || p.Office.Layout.Desks != 6 {
		t.Fatalf("project office %+v", p.Office)
	}
	r := f.doBody(t, "PATCH", "/api/projects/"+f.pid, jsonCT, `{"settings": {"office_layout": "version: 1\nrooms: []\n"}}`)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "room lead is missing") {
		t.Fatalf("invalid layout: %d %s", r.StatusCode, b)
	}
}

// TestPresence: each UI page reports whether it is visible on the hub
// stream; the stream carries the devices with a visible page (Phase 18, ADR
// 0058). A hidden or closed page stops counting.
func TestPresence(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, f.ws+"/api/events", f.dialOpts())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	// next reads until a presence message and returns its devices.
	next := func(c *websocket.Conn) []string {
		t.Helper()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Type     string           `json:"type"`
				Presence *engine.Presence `json:"presence"`
			}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if m.Type == "presence" {
				return m.Presence.Devices
			}
		}
	}
	send := func(c *websocket.Conn, msg string) {
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	a := dial()
	if d := next(a); len(d) != 0 {
		t.Fatalf("nobody yet: %q", d)
	}
	send(a, `{"type":"presence","visible":true}`)
	d := next(a)
	if len(d) != 1 || d[0] == "" {
		t.Fatalf("a visible page: %q", d)
	}
	device := d[0]
	b := dial()
	if got := next(b); len(got) != 1 || got[0] != device {
		t.Fatalf("a new page sees the presence: %q", got)
	}
	send(b, `{"type":"presence","visible":true}`) // the same device: no change, no message
	send(a, `{"type":"presence","visible":false}`) // b still shows it: no change either
	send(b, `not json`)                            // ignored
	send(b, `{"type":"presence","visible":false}`) // now nobody
	if got := next(a); len(got) != 0 {
		t.Fatalf("every page hidden: %q", got)
	}
	send(b, `{"type":"presence","visible":true}`)
	if got := next(a); len(got) != 1 {
		t.Fatalf("visible again: %q", got)
	}
	b.Close(websocket.StatusNormalClosure, "")
	if got := next(a); len(got) != 0 {
		t.Fatalf("the visible page closed: %q", got)
	}
}

