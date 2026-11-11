package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

func TestProjectSettingsParseAndValidate(t *testing.T) {
	s, err := ParseProjectSettings([]byte(`{"pack": "node", "model": "sonnet", "max_agents": 2, "role_models": {"qa": "opus"},
		"egress_allow": ["registry.npmjs.org"], "github": true, "budget_goal_micro_usd": 500000}`))
	if err != nil {
		t.Fatal(err)
	}
	if *s.Pack != "node" || *s.MaxAgents != 2 || !*s.GitHub || s.RoleModels["qa"] != "opus" || s.Reviewer != nil {
		t.Fatalf("parsed %+v", s)
	}
	enc, _ := s.encode()
	if strings.Contains(enc, "reviewer") || strings.Contains(enc, "secrets_dir") {
		t.Fatalf("only set fields are stored: %s", enc)
	}
	if e, _ := (ProjectSettings{}).encode(); e != "{}" {
		t.Fatalf("empty settings stored as %s", e)
	}
	for in, want := range map[string]string{
		`{"modle": "haiku"}`:                     "unknown field",
		`{"pack": "Go!"}`:                        `pack "Go!"`,
		`{"secrets_dir": "relative"}`:            "secrets_dir",
		`{"egress_allow": ["*.example.com"]}`:    "egress_allow",
		`{"model": "has space"}`:                 "model",
		`{"max_agents": 0}`:                      "max_agents 0",
		`{"max_agents": 33}`:                     "max_agents 33",
		`{"reviewer": "gpt"}`:                    `reviewer "gpt"`,
		`{"review_rounds": 9}`:                   "review_rounds 9",
		`{"budget_session_micro_usd": -1}`:       "budget_session_micro_usd -1",
		`{"github_remote": "has space"}`:         "github_remote",
		`{"role_models": {"qa": ""}}`:            "role_models",
		`{"access_decider": "anyone"}`:           `access_decider "anyone"`,
		`{"lead_access_hosts": ["*.x.com"]}`:     "lead_access_hosts",
		`{"worker_memory": "100m"}`:              `worker_memory "100m"`,
		`{"worker_memory": "4 GB"}`:              "worker_memory",
		`{} {}`:                                  "one JSON object",
		`{"max_agents": 0, "reviewer": "gpt"}`:   "between 1 and 32; reviewer \"gpt\"", // every problem at once
		`{"pack": "none", "model": ""}` + "\x00": "invalid",
	} {
		_, err := ParseProjectSettings([]byte(in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	if _, err := ParseProjectSettings([]byte(`{"pack": "none", "model": ""}`)); err != nil {
		t.Fatalf("pack none and the CLI's default model: %v", err)
	}
	// ADR 0053, 0051.
	s, err = ParseProjectSettings([]byte(`{"access_decider": "lead_policy", "lead_access_hosts": ["storage.googleapis.com", ".example.org"],
		"worker_memory": "4g"}`))
	if err != nil || *s.AccessDecider != DeciderLeadPolicy || len(s.LeadAccessHosts) != 2 || *s.WorkerMemory != "4g" {
		t.Fatalf("access and memory settings: %+v %v", s, err)
	}
	eff := NeutralDefaults().Merge(s)
	if *eff.AccessDecider != DeciderLeadPolicy || *eff.WorkerMemory != "4g" || len(eff.LeadAccessHosts) != 2 {
		t.Fatalf("merged %+v", eff)
	}
	// Letting the lead decide widens the sandbox: from this machine only (ADR 0046).
	if ch := NeutralDefaults().SandboxChanges(eff); !slices.Contains(ch, "access_decider") || !slices.Contains(ch, "lead_access_hosts") {
		t.Fatalf("sandbox changes %v", ch)
	}
}

func TestProjectSettingsMerge(t *testing.T) {
	d := NeutralDefaults()
	d.Pack, d.EgressAllow, d.RoleModels = Ptr("go"), []string{"extra.example.com"}, map[string]string{"qa": "sonnet"}
	eff := d.Merge(ProjectSettings{Pack: Ptr(PackNone), EgressAllow: []string{"registry.npmjs.org"},
		RoleModels: map[string]string{"worker": "opus"}, MaxAgents: Ptr(2)})
	if eff.PackName() != "" || *eff.MaxAgents != 2 || *eff.Model != "" || *eff.Reviewer != d.ReviewerOrEmpty() {
		t.Fatalf("merged %+v", eff)
	}
	if strings.Join(eff.EgressAllow, ",") != "extra.example.com,registry.npmjs.org" {
		t.Fatalf("egress %v", eff.EgressAllow)
	}
	if eff.RoleModels["qa"] != "sonnet" || eff.RoleModels["worker"] != "opus" {
		t.Fatalf("role models %v", eff.RoleModels)
	}
	if len(d.RoleModels) != 1 {
		t.Fatalf("merge changed the daemon's role models: %v", d.RoleModels)
	}
	if err := (ProjectSettings{}).complete(); err == nil || !strings.Contains(err.Error(), "pack") {
		t.Fatalf("incomplete defaults: %v", err)
	}
}

// ReviewerOrEmpty is the reviewer, for comparisons in tests.
func (s ProjectSettings) ReviewerOrEmpty() string {
	if s.Reviewer == nil {
		return ""
	}
	return *s.Reviewer
}

func TestProjectSettingsCLIs(t *testing.T) {
	if _, err := ParseProjectSettings([]byte(`{"role_clis": {"worker": "codex"}, "codex_model": "gpt-6.1-sol"}`)); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		`{"role_clis": {"worker": "gemini"}}`: `role_clis: "gemini" is not an agent CLI`,
		`{"codex_model": "has space"}`:        "codex_model",
	} {
		if _, err := ParseProjectSettings([]byte(in)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", in, err)
		}
	}
	d := NeutralDefaults()
	d.RoleCLIs = map[string]string{"reviewer": "codex"}
	eff := d.Merge(ProjectSettings{RoleCLIs: map[string]string{"worker": "codex"}, CodexModel: Ptr("gpt-6.1-sol")})
	if eff.RoleCLIs["reviewer"] != "codex" || eff.RoleCLIs["worker"] != "codex" || *eff.CodexModel != "gpt-6.1-sol" {
		t.Fatalf("merged %+v", eff)
	}
}

// Codex's session id is learned only from a SessionStart of a startup or a
// resume, and every later hook must carry it (ADR 0049).
func TestCodexSessionLearning(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	ctx := context.Background()
	if _, err := f.store.DB.Exec(`INSERT INTO agents (id, project_id, inherit_user_settings, state, started_at, sandbox, cli)
		VALUES ('cx1', ?, 0, 'starting', ?, 'podman', 'codex')`, e.project.ID, now()); err != nil {
		t.Fatal(err)
	}
	if err := e.q.CreateSession(ctx, db.CreateSessionParams{ID: "s-cx1", AgentID: "cx1", StartedAt: now()}); err != nil {
		t.Fatal(err)
	}
	a := &agentProc{info: AgentInfo{ID: "cx1", SessionID: "s-cx1", CLI: role.CLICodex}}
	log := e.log
	ev := func(kind agent.EventKind, sid, detail string) *agent.Event {
		return &agent.Event{Kind: kind, SessionID: sid, Detail: detail}
	}
	for _, bad := range []*agent.Event{ev(agent.TurnStarted, "codex-1", ""), ev(agent.SessionStarted, "codex-1", "clear")} {
		if err := e.checkSessionLocked(ctx, a, bad, log); !errors.Is(err, ErrSession) || a.cliSession != "" {
			t.Fatalf("%+v named the session: %v %q", bad, err, a.cliSession)
		}
	}
	first := ev(agent.SessionStarted, "codex-1", "startup")
	if err := e.checkSessionLocked(ctx, a, first, log); err != nil || a.cliSession != "codex-1" || first.SessionID != "s-cx1" {
		t.Fatalf("startup: %v %q %+v", err, a.cliSession, first)
	}
	if s, _ := e.q.GetSession(ctx, "s-cx1"); s.CliSessionID != "codex-1" {
		t.Fatalf("recorded %q", s.CliSessionID)
	}
	if err := e.checkSessionLocked(ctx, a, ev(agent.TurnStarted, "codex-2", ""), log); !errors.Is(err, ErrSession) {
		t.Fatalf("another session's hook: %v", err)
	}
	if err := e.checkSessionLocked(ctx, a, ev(agent.TurnStarted, "codex-1", ""), log); err != nil {
		t.Fatalf("its own hook: %v", err)
	}
}
