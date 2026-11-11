package role

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

func TestBuiltinsAreValid(t *testing.T) {
	seen := map[Behaviour]bool{}
	for _, e := range Builtins() {
		if e.Err != nil {
			t.Fatalf("built-in %s: %v", e.Name, e.Err)
		}
		seen[e.Role.Behaviour] = true
		if e.Role.Source != SourceBuiltin {
			t.Errorf("%s: source %q", e.Name, e.Role.Source)
		}
	}
	for _, b := range Behaviours {
		if !seen[b] {
			t.Errorf("no built-in role for behaviour %s", b)
		}
	}
	s := MustBuiltins()
	for _, b := range Behaviours {
		if r := s.Default(b); r == nil || r.Behaviour != b {
			t.Errorf("default role for %s: %+v", b, r)
		}
	}
	if r, _ := s.Get(NameTechLead); !strings.Contains(r.Prompt, PlaceholderMaxSubTasks) ||
		!strings.Contains(r.Prompt, PlaceholderMaxWorkers) {
		t.Error("the tech-lead prompt lost its placeholders")
	}
	for _, n := range []string{NameArchitect, NameTechLead, NameAssistant} {
		if r, _ := s.Get(n); !r.ReadOnly {
			t.Errorf("%s is not read-only", n)
		}
	}
	if r, _ := s.Get("docs"); r.Behaviour != Worker || !slices.Contains(r.Deny, "WebFetch") {
		t.Errorf("docs role: %+v", r)
	}
}

func roleFS(yaml string) fstest.MapFS { return fstest.MapFS{File: {Data: []byte(yaml)}} }

const valid = `schema: 1
name: sec
description: Checks security.
behaviour: worker
model: sonnet
workspace: read-only
tools:
  team: [report_done, list_tasks]
  deny: [WebFetch, "Bash(curl:*)"]
prompt: |
  You check security.
`

func TestLoadValid(t *testing.T) {
	r, err := load(roleFS(valid), "sec", "x", "sec/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if r.Behaviour != Worker || r.Model != "sonnet" || !r.ReadOnly || r.ReportsTo != ToLead ||
		!slices.Equal(r.TeamTools, []string{mcp.ToolReportDone, mcp.ToolListTasks}) || r.Prompt != "You check security." {
		t.Fatalf("%+v", r)
	}
	if got := r.DenyRules(); !slices.Equal(got, []string{"WebFetch", "Bash(curl:*)", "Edit", "Write", "NotebookEdit"}) {
		t.Fatalf("deny rules %v", got)
	}
	// Defaults come from the behaviour.
	r, err = load(roleFS("schema: 1\nname: a\ndescription: d\nbehaviour: architect\nprompt: p\n"), "a", "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !r.ReadOnly || r.ReportsTo != ToUser || !slices.Equal(r.TeamTools, MaxTools(Architect)) {
		t.Fatalf("defaults %+v", r)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []struct{ yaml, want string }{
		{"", "the file is empty"},
		{"schema: 2\nname: x\n", "version 2 is not supported"},
		{"name: x\n", "schema: missing"},
		{"schema: 1\nname: Bad\n", `"Bad" must be lower-case`},
		{"schema: 1\nname: other\n", `does not match the role directory "x"`},
		{"schema: 1\nname: x\n", "description: missing"},
		{"schema: 1\nname: x\ndescription: \"a\\nb\"\n", "must be one line"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: boss\n", `"boss" is not a behaviour`},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nmodel: \"bad model\"\n", "is not a model name"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nreports_to: user\n", `"user" is not allowed for behaviour worker`},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nworkspace: sometimes\n", "must be read-only or read-write"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ntools: {team: [assign_task, report_done]}\n",
			`"assign_task" is not a team tool of behaviour worker`},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ntools: {team: [list_tasks]}\n", "needs report_done"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ntools: {team: [report_done, report_done]}\n", "listed twice"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ntools: {deny: [\"rm -rf\"]}\n", "is not a Claude Code permission rule"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\n", "prompt: missing"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nprompt: \"a\\u0007b\"\n", "control characters"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nprompt: p\ncolour: red\n", `unknown field "colour"`},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nprompt: p\n---\nschema: 1\n", "more than one YAML document"},
		{"schema: 1\nname: x\ndescription: d\nbehaviour: worker\nprompt: " + strings.Repeat("a", maxPromptChars+1) + "\n", "longer than"},
	}
	for _, c := range cases {
		_, err := load(roleFS(c.yaml), "x", "x", "x/role.yaml")
		var re *Error
		if !errors.As(err, &re) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: want %q, got %v", c.yaml, c.want, err)
		}
	}
	// Every problem is reported at once.
	_, err := load(roleFS("schema: 1\nname: x\nbehaviour: boss\n"), "x", "x", "x")
	var re *Error
	if !errors.As(err, &re) || len(re.Problems) != 3 {
		t.Fatalf("want 3 problems, got %v", err)
	}
}

func writeRole(t *testing.T, dir, name, body string) string {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, File), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUserRoles(t *testing.T) {
	dir := t.TempDir()
	writeRole(t, dir, "devops", "schema: 1\nname: devops\ndescription: CI.\nbehaviour: worker\nprompt: You own CI.\n")
	writeRole(t, dir, "tech-lead", "schema: 1\nname: tech-lead\ndescription: Mine.\nbehaviour: lead\nprompt: My lead.\n")
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.Get("devops"); !ok || r.Source != filepath.Join(dir, "devops") {
		t.Fatalf("devops: %+v", r)
	}
	if r := s.Default(Lead); r.Prompt != "My lead." || r.Source == SourceBuiltin {
		t.Fatalf("a user tech-lead replaces the built-in: %+v", r)
	}
	names := []string{}
	for _, r := range s.List() {
		names = append(names, r.Name)
	}
	n := 0
	for _, x := range names {
		if x == "tech-lead" {
			n++
		}
	}
	if !slices.IsSorted(names) || n != 1 {
		t.Fatalf("names %v", names)
	}
	all, _ := List(dir)
	for _, e := range all {
		if e.Name == "tech-lead" && e.Source != SourceBuiltin && !e.Replaces {
			t.Error("replacement not marked")
		}
	}

	// A replacement must keep the built-in's behaviour.
	writeRole(t, dir, "qa", "schema: 1\nname: qa\ndescription: x\nbehaviour: worker\nprompt: p\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "must keep its behaviour qa") {
		t.Fatalf("behaviour change: %v", err)
	}
	os.RemoveAll(filepath.Join(dir, "qa"))

	// An invalid user role fails the load; it is not skipped.
	writeRole(t, dir, "broken", "schema: 1\nname: broken\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("broken role: %v", err)
	}
	os.RemoveAll(filepath.Join(dir, "broken"))

	// Ownership and modes, like stack packs.
	d := writeRole(t, dir, "loose", "schema: 1\nname: loose\ndescription: x\nbehaviour: worker\nprompt: p\n")
	if err := os.Chmod(filepath.Join(d, File), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("group-writable: %v", err)
	}
	os.Chmod(filepath.Join(d, File), 0o644)
	if err := os.Symlink(filepath.Join(d, File), filepath.Join(d, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}

	// A missing directory means built-ins only.
	if _, err := Load(filepath.Join(dir, "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestModels(t *testing.T) {
	m, err := ParseModels(" architect=opus, worker = sonnet ,")
	if err != nil || m["architect"] != "opus" || m["worker"] != "sonnet" || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"architect", "=opus", "a=b,a=c"} {
		if _, err := ParseModels(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	s := MustBuiltins()
	s2, err := s.WithModels(map[string]string{"architect": "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Default(Architect).Model != "opus" || s.Default(Architect).Model != "" {
		t.Fatal("WithModels must copy")
	}
	if _, err := s.WithModels(map[string]string{"nobody": "opus"}); err == nil {
		t.Fatal("unknown role accepted")
	}
	if _, err := s.WithModels(map[string]string{"worker": "a b"}); err == nil {
		t.Fatal("bad model accepted")
	}
}

// The behaviour table is the ceiling: no behaviour gets a tool of another's
// authority (ADR 0029).
func TestBehaviourTools(t *testing.T) {
	only := map[string][]Behaviour{
		mcp.ToolCreateTask: {Lead}, mcp.ToolAssignTask: {Lead}, mcp.ToolStartQA: {Lead},
		mcp.ToolSubmitPlan: {Architect}, mcp.ToolReportQA: {QA}, mcp.ToolSubmitReview: {Reviewer},
		mcp.ToolReportNoChange: {Worker}, mcp.ToolGetBriefing: {Assistant}, mcp.ToolProposeMemory: {Assistant},
		mcp.ToolListMemory: {Architect, Lead, Worker, QA, Assistant}, // not reviewers (ADR 0037)
	}
	for tool, allowed := range only {
		for _, b := range Behaviours {
			if slices.Contains(MaxTools(b), tool) != slices.Contains(allowed, b) {
				t.Errorf("%s on %s", tool, b)
			}
		}
	}
	for _, b := range Behaviours {
		for _, need := range required(b) {
			if !slices.Contains(MaxTools(b), need) {
				t.Errorf("%s requires %s it cannot have", b, need)
			}
		}
	}
}

func TestCLIs(t *testing.T) {
	r, err := load(roleFS("schema: 1\nname: x\ndescription: d\nbehaviour: worker\ncli: codex\nprompt: p\n"), "x", "x", "x")
	if err != nil || r.CLI != CLICodex {
		t.Fatalf("codex role %+v %v", r, err)
	}
	if r, _ := load(roleFS(valid), "sec", "x", "sec/role.yaml"); r.CLI != CLIClaude {
		t.Fatalf("default CLI %q", r.CLI)
	}
	for yaml, want := range map[string]string{
		"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ncli: gemini\nprompt: p\n":                           `"gemini" is not an agent CLI`,
		"schema: 1\nname: x\ndescription: d\nbehaviour: worker\ncli: codex\ntools: {deny: [WebFetch]}\nprompt: p\n": "Claude Code permission rules",
	} {
		if _, err := load(roleFS(yaml), "x", "x", "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", yaml, err)
		}
	}
	m, err := ParseCLIs("worker=codex, reviewer=codex")
	if err != nil || m["worker"] != CLICodex || m["reviewer"] != CLICodex {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := ParseCLIs("worker=gemini"); err == nil || !strings.Contains(err.Error(), "--role-cli") {
		t.Fatalf("bad CLI: %v", err)
	}
	s := MustBuiltins()
	s2, err := s.WithCLIs(map[string]string{"worker": CLICodex, "reviewer": CLICodex})
	if err != nil {
		t.Fatal(err)
	}
	get := func(s *Set, n string) *Role { r, _ := s.Get(n); return r }
	if get(s2, "worker").CLI != CLICodex || get(s, "worker").CLI != CLIClaude || get(s2, "tech-lead").CLI != CLIClaude {
		t.Fatal("WithCLIs must copy and change only the named roles")
	}
	if _, err := s.WithCLIs(map[string]string{"nobody": CLICodex}); err == nil {
		t.Fatal("unknown role accepted")
	}
}
