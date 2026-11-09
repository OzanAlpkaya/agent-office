package engine

import (
	"context"
	"regexp"
	"testing"
)

func TestFirstNames(t *testing.T) {
	if len(firstNames) < 120 {
		t.Fatalf("%d names; the list should cover a busy office twice over", len(firstNames))
	}
	short := regexp.MustCompile(`^[A-Z][a-z]{1,7}$`) // ASCII, fits a nameplate
	seen := map[string]bool{}
	for _, n := range firstNames {
		if !short.MatchString(n) {
			t.Errorf("name %q: want 2-8 ASCII letters, capitalized", n)
		}
		if seen[n] {
			t.Errorf("name %q is listed twice", n)
		}
		seen[n] = true
	}
}

func TestPickName(t *testing.T) {
	none := func(string) bool { return false }
	a := pickName("a1b2c3d4e5f6", none)
	if a != pickName("a1b2c3d4e5f6", none) {
		t.Fatal("the same id and taken names give a different name")
	}
	// A taken name moves on to the next free one in list order.
	b := pickName("a1b2c3d4e5f6", func(n string) bool { return n == a })
	if b == a || b == "" {
		t.Fatalf("taken %q, got %q", a, b)
	}
	// Different ids spread over the list.
	got := map[string]bool{}
	for _, id := range []string{"000001", "000002", "000003", "000004", "000005", "000006", "000007", "000008"} {
		got[pickName(id, none)] = true
	}
	if len(got) < 6 {
		t.Fatalf("8 ids landed on %d names", len(got))
	}
	// Every name taken: numbered, skipping taken numbers.
	listed := map[string]bool{a + " 2": true}
	for _, n := range firstNames {
		listed[n] = true
	}
	all := func(n string) bool { return listed[n] }
	if n := pickName("a1b2c3d4e5f6", all); n != a+" 3" {
		t.Fatalf("all names taken: got %q, want %q", n, a+" 3")
	}
}

// TestAgentNames: names are unique among listed agents, survive a restart, and
// agents recorded before names existed get one (ADR 0043).
func TestAgentNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for range 3 {
		a, err := e1.Start(ctx, StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if a.Name == "" {
			t.Fatalf("agent %s has no name", a.ID)
		}
		for id, n := range names {
			if n == a.Name {
				t.Fatalf("agents %s and %s are both %q", id, a.ID, n)
			}
		}
		names[a.ID] = a.Name
	}
	e1.Shutdown()

	// One agent from before names existed.
	var unnamed string
	for id := range names {
		unnamed = id
		break
	}
	if _, err := f.store.DB.Exec("UPDATE agents SET name = '' WHERE id = ?", unnamed); err != nil {
		t.Fatal(err)
	}
	e2 := f.engine(t)
	seen := map[string]bool{}
	for id, want := range names {
		got, err := e2.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if id != unnamed && got.Name != want {
			t.Fatalf("after a restart %s is %q, was %q", id, got.Name, want)
		}
		if got.Name == "" || seen[got.Name] {
			t.Fatalf("after a restart %s is %q (taken: %v)", id, got.Name, seen)
		}
		seen[got.Name] = true
	}
	row, err := f.store.Q.ListVisibleAgents(ctx, e2.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range row {
		if r.Name == "" {
			t.Fatalf("agent %s was named but not stored", r.ID)
		}
	}
}

func TestNameFallsBackWhenAllAreListed(t *testing.T) {
	e := &Engine{agents: map[string]*agentProc{}, naming: map[string]bool{}}
	stamp := now()
	for i, n := range firstNames {
		a := &agentProc{info: AgentInfo{ID: randomHex(6), Name: n, ExitedAt: &stamp}}
		if i == 0 {
			a.info.ExitedAt = nil // the only running one
		}
		e.agents[a.info.ID] = a
	}
	// Every name is listed, all but the first by exited agents: a new agent
	// may share an exited agent's name, never the running agent's.
	for _, id := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		if n := e.nameLocked(id); n == firstNames[0] || n == "" {
			t.Fatalf("got %q", n)
		}
	}
	// A start in progress reserves its name like a running agent.
	e.naming[firstNames[1]] = true
	for _, id := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		if n := e.nameLocked(id); n == firstNames[0] || n == firstNames[1] {
			t.Fatalf("got %q", n)
		}
	}
}
