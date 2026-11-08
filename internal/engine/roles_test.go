package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
)

// userRoles writes role directories into a fresh roles dir and loads them.
func userRoles(t *testing.T, roles map[string]string) *role.Set {
	t.Helper()
	dir := t.TempDir()
	for name, body := range roles {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, role.File), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := role.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// agentSettings reads the settings file a host agent was started with.
func agentSettings(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.opts.RuntimeDir, "agents", id+".settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func denyOf(doc map[string]any) []string {
	var out []string
	if p, ok := doc["permissions"].(map[string]any); ok {
		if d, ok := p["deny"].([]any); ok {
			for _, x := range d {
				out = append(out, x.(string))
			}
		}
	}
	return out
}

// mcpTools lists the team tools an agent's MCP socket offers.
func mcpTools(t *testing.T, f *fixture, id string) []string {
	t.Helper()
	ctx := context.Background()
	c, err := net.Dial("unix", filepath.Join(f.opts.RuntimeDir, "agents", id+".mcp.sock"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx,
		&sdk.IOTransport{Reader: c, Writer: c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

// A new role is data: a user's "devops" worker runs with its own model,
// prompt, denied tools and team tools, without code changes (DoD).
func TestRolesAreData(t *testing.T) {
	f := newFixture(t, "AO_FAKE_PROMPT=1")
	f.opts.MaxAgents = 4
	f.opts.Model = "haiku"
	roles := userRoles(t, map[string]string{"devops": `schema: 1
name: devops
description: Owns CI.
behaviour: worker
model: sonnet
tools:
  team: [list_tasks, report_done]
  deny: [WebFetch]
prompt: You are the devops engineer; you own CI.
`})
	roles, err := roles.WithModels(map[string]string{"architect": "opus"})
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Roles = roles
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	task, _ := e.CreateTask(ctx, TaskInput{Title: "Fix CI"})
	a, err := e.Start(ctx, StartRequest{TaskID: task.ID, Role: "devops"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindWorker || a.Role != "devops" || a.Model != "sonnet" {
		t.Fatalf("agent %+v", a)
	}
	log.wait(t, startWait, agentState(a.ID, hooks.Idle))
	screen(t, e, a.ID, "PROMPT:You are the devops engineer; you own CI.")
	screen(t, e, a.ID, "Task: Fix CI")
	if d := denyOf(agentSettings(t, f, a.ID)); !slices.Equal(d, []string{"WebFetch"}) {
		t.Fatalf("deny rules %v", d)
	}
	if got := mcpTools(t, f, a.ID); !slices.Equal(got, []string{mcp.ToolListTasks, mcp.ToolReportDone}) {
		t.Fatalf("team tools %v", got)
	}
	rows, _ := e.q.ListVisibleAgents(ctx, e.project.ID)
	if len(rows) != 1 || rows[0].Kind != KindWorker || rows[0].Role != "devops" || rows[0].Model != "sonnet" {
		t.Fatalf("stored %+v", rows)
	}

	// A read-only role (the architect) gets the editing tools denied, and its
	// own model from --role-model; it needs a goal.
	if _, err := e.Start(ctx, StartRequest{Role: RoleArchitect}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("architect without a goal: %v", err)
	}
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Big goal"})
	arch, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	if arch.Model != "opus" || arch.Kind != KindArchitect {
		t.Fatalf("architect %+v", arch)
	}
	log.wait(t, startWait, agentState(arch.ID, hooks.Idle))
	if d := denyOf(agentSettings(t, f, arch.ID)); !slices.Equal(d, role.ReadOnlyDeny) {
		t.Fatalf("architect deny rules %v", d)
	}
	if _, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second architect: %v", err)
	}

	// Kind rules hold whatever the name: reviewers and QA are started by
	// agent-office only; the assistant has no task; unknown roles are refused.
	for _, c := range []StartRequest{{TaskID: task.ID, Role: RoleReviewer}, {TaskID: goal.ID, Role: RoleQA},
		{TaskID: task.ID, Role: RoleAssistant}, {Role: "boss"}} {
		if _, err := e.Start(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	// The old name of the Tech Lead still works.
	lead, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: "lead"})
	if err != nil || lead.Role != RoleTechLead || lead.Kind != KindLead || lead.Model != "haiku" {
		t.Fatalf("lead %+v %v", lead, err)
	}
	if !strings.Contains(strings.Join(mcpTools(t, f, lead.ID), ","), mcp.ToolAssignTask) {
		t.Fatal("the lead lacks assign_task")
	}
}
