package engine

import (
	"errors"
	"strings"
	"testing"
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
