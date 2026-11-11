package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/api"
	"github.com/OzanAlpkaya/agent-office/internal/auth"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/store"
)

func gitRepo(t *testing.T) string {
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

func TestProjectCommand(t *testing.T) {
	rt, err := os.MkdirTemp("", "ao-rt-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	data := t.TempDir()
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := projectCommand(append(args, "--data-dir", data), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, _, e := run("list"); code != 1 || !strings.Contains(e, "no agent-office daemon is running") {
		t.Fatalf("no daemon: %d %q", code, e)
	}

	st, err := store.Open(context.Background(), filepath.Join(data, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agentRT := filepath.Join(rt, "agents-rt")
	hub, err := engine.NewHub(context.Background(), engine.HubOptions{Base: engine.Options{
		WorktreeRoot: filepath.Join(data, "wt"), Store: st, MaxAgents: 4, ClaudeBin: "/bin/true", HookCommand: "true",
		RuntimeDir: agentRT, StopTimeout: time.Second, Logger: log, AgentOfficeBin: "/bin/true",
	}, Defaults: engine.NeutralDefaults()})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Shutdown()
	m, err := auth.New(context.Background(), st.Q, auth.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := runtimeDir()
	l, err := listenControl(controlSocketPath(dir, data))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.NewControl(m, hub, api.ControlInfo{}, log)}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	if code, out, _ := run("list"); code != 0 || !strings.Contains(out, "No projects") {
		t.Fatalf("empty list: %d %q", code, out)
	}
	repo := gitRepo(t)
	if code, out, e := run("add", repo, "--name", "webapp", "--model", "sonnet", "--max-agents", "2",
		"--budget-goal-usd", "0.5", "--role-model", "worker=opus"); code != 0 || !strings.Contains(out, "Added project webapp") {
		t.Fatalf("add: %d %q %q", code, out, e)
	}
	ps := hub.Projects()
	if len(ps) != 1 {
		t.Fatalf("projects %+v", ps)
	}
	s := ps[0].Settings
	if *s.Model != "sonnet" || *s.MaxAgents != 2 || *s.BudgetGoalMicroUSD != 500_000 || s.RoleModels["worker"] != "opus" ||
		s.Pack != nil || s.GitHub != nil {
		t.Fatalf("settings from flags %+v", s)
	}
	if code, _, e := run("add", repo); code != 1 || !strings.Contains(e, "already a project") {
		t.Fatalf("add twice: %d %q", code, e)
	}
	if code, _, e := run("add", gitRepo(t), "--max-agents", "40"); code != 1 || !strings.Contains(e, "max_agents 40") {
		t.Fatalf("invalid setting: %d %q", code, e)
	}
	// set changes only the flags given; --inherit drops overrides.
	if code, out, e := run("set", "webapp", "--model", "opus", "--inherit", "max_agents"); code != 0 || !strings.Contains(out, "webapp") {
		t.Fatalf("set: %d %q %q", code, out, e)
	}
	s = hub.Projects()[0].Settings
	if *s.Model != "opus" || s.MaxAgents != nil || *s.BudgetGoalMicroUSD != 500_000 {
		t.Fatalf("settings after set %+v", s)
	}
	if code, out, _ := run("list"); code != 0 || !strings.Contains(out, "webapp") || !strings.Contains(out, "opus") ||
		!strings.Contains(out, repo) {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, e := run("remove", "nope"); code != 1 || !strings.Contains(e, "not found") {
		t.Fatalf("remove unknown: %d %q", code, e)
	}
	if code, out, e := run("remove", ps[0].ID); code != 0 || !strings.Contains(out, "Removed project") {
		t.Fatalf("remove: %d %q %q", code, out, e)
	}
	if len(hub.Projects()) != 0 {
		t.Fatal("project still listed")
	}
	if code, _, _ := run("frobnicate"); code != 2 {
		t.Fatal("unknown subcommand accepted")
	}
}

func TestDaemonDefaults(t *testing.T) {
	c, err := parseServe([]string{"--pack", "go", "--model", "haiku", "--budget-session-usd", "0.25", "--role-model", "qa=sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	d := daemonDefaults(c)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.PackName() != "go" || *d.Model != "haiku" || *d.BudgetSessionMicroUSD != 250_000 || d.RoleModels["qa"] != "sonnet" ||
		*d.MaxAgents != 4 || *d.GitHub {
		t.Fatalf("defaults %+v", d)
	}
	if c, _ := parseServe(nil); daemonDefaults(c).PackName() != "" || *daemonDefaults(c).Pack != engine.PackNone {
		t.Fatal("no --pack is the pack none")
	}
	// A project overrides one thing and inherits the rest.
	eff := d.Merge(engine.ProjectSettings{Pack: engine.Ptr("node"), EgressAllow: []string{"registry.example.com"}})
	if eff.PackName() != "node" || *eff.Model != "haiku" || len(eff.EgressAllow) != 1 {
		t.Fatalf("merged %+v", eff)
	}
}
