package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/office"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/review"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// PackNone is the pack setting of a project that uses no stack pack although
// the daemon has one (--pack).
const PackNone = "none"

// Limits of project settings.
const (
	MaxProjectAgents   = 32    // --max-agents has the same range
	MaxProjectSubTasks = 100   // max_sub_tasks's range is 1-100 (ADR 0062)
	DefaultMaxSubTasks = 20    // open sub-tasks per goal unless a project sets max_sub_tasks
	maxProjectEgress   = 50    // extra egress names per project
	maxProjectName     = 60    // display name
	maxBudgetMicroUSD  = 1e12  // $1,000,000, as the --budget-* flags
	maxProjectSettings = 20000 // bytes of JSON; the column's CHECK
)

var remoteRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ProjectSettings is what a project overrides of the daemon's flags (ADR
// 0046). A nil field, or an empty list or map, inherits the daemon's value;
// the daemon's own values are a ProjectSettings with every field set
// (Defaults). Money is integer micro-USD (ADR 0026).
type ProjectSettings struct {
	Pack                  *string           `json:"pack,omitempty"`                     // a stack pack name; PackNone: none (--pack)
	SecretsDir            *string           `json:"secrets_dir,omitempty"`              // absolute (--secrets-dir)
	EgressAllow           []string          `json:"egress_allow,omitempty"`             // added to the pack's names and --egress-allow
	Model                 *string           `json:"model,omitempty"`                    // "": the CLI default (--model)
	RoleModels            map[string]string `json:"role_models,omitempty"`              // merged over --role-model
	RoleCLIs              map[string]string `json:"role_clis,omitempty"`                // merged over --role-cli (ADR 0050)
	CodexModel            *string           `json:"codex_model,omitempty"`              // Codex agents' default model; "": Codex's (--codex-model)
	MaxAgents             *int              `json:"max_agents,omitempty"`               // at most the daemon's --max-agents
	MaxSubTasks           *int              `json:"max_sub_tasks,omitempty"`            // open sub-tasks per goal (ADR 0062); default 20
	GitHub                *bool             `json:"github,omitempty"`                   // --github
	GitHubRemote          *string           `json:"github_remote,omitempty"`            // --github-remote
	Reviewer              *string           `json:"reviewer,omitempty"`                 // --reviewer
	ReviewRounds          *int              `json:"review_rounds,omitempty"`            // --review-rounds
	BudgetSessionMicroUSD *int64            `json:"budget_session_micro_usd,omitempty"` // --budget-session-usd; 0: off
	BudgetGoalMicroUSD    *int64            `json:"budget_goal_micro_usd,omitempty"`    // --budget-goal-usd; 0: off
	AccessDecider         *string           `json:"access_decider,omitempty"`           // who decides access requests (ADR 0053); default user
	LeadAccessHosts       []string          `json:"lead_access_hosts,omitempty"`        // lead_policy: the hosts the lead may approve
	WorkerMemory          *string           `json:"worker_memory,omitempty"`            // sandboxed workers' memory limit, e.g. 4g (ADR 0051); "": the pack's or --sandbox-memory
	WorkerPids            *int              `json:"worker_pids,omitempty"`              // sandboxed workers' process limit (Phase 17); 0: the pack's or --sandbox-pids
	MergeTarget           *string           `json:"merge_target,omitempty"`             // the branch a goal's Merge button squash-merges into (ADR 0055)
	OfficeLayout          *string           `json:"office_layout,omitempty"`            // the office's floor plan as YAML (Phase 18, ADR 0057); "": the default plan
}

// Who decides access requests (ADR 0053).
const (
	DeciderUser       = "user"        // the user, in the UI (ADR 0019)
	DeciderLead       = "lead"        // the goal's Tech Lead, with a recorded reason; the user can decide first
	DeciderLeadPolicy = "lead_policy" // the Tech Lead, for hosts in lead_access_hosts only; the user for the rest
)

// DefaultMergeTarget is the branch a goal is merged into unless the project
// sets merge_target (ADR 0055).
const DefaultMergeTarget = "main"

// validBranchName accepts a plain local branch name: what git check-ref-format
// --branch takes, minus anything that could read as an option or a range.
func validBranchName(b string) bool {
	if b == "" || len(b) > 255 || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, ".") || strings.Contains(b, "..") ||
		strings.Contains(b, "//") || strings.Contains(b, "@{") || b == "@" || b == "HEAD" {
		return false
	}
	for _, r := range b {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

// ValidWorkerMemory reports whether m is a worker memory limit between 256m
// and 256g, written as podman takes it (512m, 4g).
func ValidWorkerMemory(m string) bool { return sandbox.ValidMemory(m) }

// ParseProjectSettings decodes stored or posted settings strictly: an
// unknown field is an error, so a typo never silently inherits.
func ParseProjectSettings(b []byte) (ProjectSettings, error) {
	var s ProjectSettings
	if len(bytes.TrimSpace(b)) == 0 {
		return s, nil
	}
	if len(b) > maxProjectSettings {
		return s, invalid("project settings are larger than %d bytes", maxProjectSettings)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, invalid("project settings: %v", err)
	}
	if dec.More() {
		return s, invalid("project settings: one JSON object expected")
	}
	return s, s.Validate()
}

// encode returns the settings as stored: only the fields that are set.
func (s ProjectSettings) encode() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if len(b) > maxProjectSettings {
		return "", invalid("project settings are larger than %d bytes", maxProjectSettings)
	}
	return string(b), nil
}

// Validate checks every field that is set and reports all problems at once,
// each with what is allowed. Whether a pack exists or its image is built is
// checked when the project opens.
func (s ProjectSettings) Validate() error {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	if p := s.Pack; p != nil && *p != PackNone && !pack.ValidName(*p) {
		add("pack %q: a stack pack name (lower-case letters, digits and '-') or %q", *p, PackNone)
	}
	if d := s.SecretsDir; d != nil && (!filepath.IsAbs(*d) || filepath.Clean(*d) != *d) {
		add("secrets_dir %q: an absolute, clean path", *d)
	}
	if len(s.EgressAllow) > maxProjectEgress {
		add("egress_allow: at most %d names", maxProjectEgress)
	} else if _, err := sandbox.ParseAllowlist(s.EgressAllow); err != nil {
		add("egress_allow: %v", err)
	}
	if m := s.Model; m != nil && *m != "" && !role.ValidModel(*m) {
		add("model %q: a model name or alias, e.g. haiku", *m)
	}
	for _, n := range slices.Sorted(maps.Keys(s.RoleModels)) {
		if !role.ValidModel(s.RoleModels[n]) {
			add("role_models: %q is not a model name for role %s", s.RoleModels[n], n)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(s.RoleCLIs)) {
		if c := s.RoleCLIs[n]; c != role.CLIClaude && c != role.CLICodex {
			add("role_clis: %q is not an agent CLI for role %s; use %s or %s", c, n, role.CLIClaude, role.CLICodex)
		}
	}
	if m := s.CodexModel; m != nil && *m != "" && !role.ValidModel(*m) {
		add("codex_model %q: a model name, e.g. gpt-6.1-sol", *m)
	}
	if n := s.MaxAgents; n != nil && (*n < 1 || *n > MaxProjectAgents) {
		add("max_agents %d: between 1 and %d", *n, MaxProjectAgents)
	}
	if n := s.MaxSubTasks; n != nil && (*n < 1 || *n > MaxProjectSubTasks) {
		add("max_sub_tasks %d: between 1 and %d", *n, MaxProjectSubTasks)
	}
	if r := s.GitHubRemote; r != nil && !remoteRe.MatchString(*r) {
		add("github_remote %q: a git remote name, e.g. origin", *r)
	}
	if r := s.Reviewer; r != nil && *r != review.NameInternal && *r != review.NameCodeRabbit && *r != ReviewerNone {
		add("reviewer %q: %s, %s or %s", *r, review.NameInternal, review.NameCodeRabbit, ReviewerNone)
	}
	if n := s.ReviewRounds; n != nil && (*n < 0 || *n > MaxReviewRounds) {
		add("review_rounds %d: between 0 and %d", *n, MaxReviewRounds)
	}
	if d := s.AccessDecider; d != nil && *d != DeciderUser && *d != DeciderLead && *d != DeciderLeadPolicy {
		add("access_decider %q: %s, %s or %s", *d, DeciderUser, DeciderLead, DeciderLeadPolicy)
	}
	if len(s.LeadAccessHosts) > maxProjectEgress {
		add("lead_access_hosts: at most %d names", maxProjectEgress)
	} else if _, err := sandbox.ParseAllowlist(s.LeadAccessHosts); err != nil {
		add("lead_access_hosts: %v", err)
	}
	if m := s.WorkerMemory; m != nil && *m != "" && !ValidWorkerMemory(*m) {
		add("worker_memory %q: a size between 256m and 256g, e.g. 4g", *m)
	}
	if n := s.WorkerPids; n != nil && *n != 0 && (*n < sandbox.MinPids || *n > sandbox.MaxPids) {
		add("worker_pids %d: between %d and %d (0: the pack's or the daemon's)", *n, sandbox.MinPids, sandbox.MaxPids)
	}
	if b := s.MergeTarget; b != nil && !validBranchName(*b) {
		add("merge_target %q: a branch name such as main", *b)
	}
	// Only the size here: a stored layout that does not validate must not stop
	// the project from opening; the office falls back to the default plan
	// (ADR 0057). A new layout is validated when it is saved (Hub.Update).
	if l := s.OfficeLayout; l != nil && len(*l) > office.MaxBytes {
		add("office_layout: %d bytes; at most %d", len(*l), office.MaxBytes)
	}
	for name, v := range map[string]*int64{"budget_session_micro_usd": s.BudgetSessionMicroUSD, "budget_goal_micro_usd": s.BudgetGoalMicroUSD} {
		if v != nil && (*v < 0 || *v > maxBudgetMicroUSD) {
			add("%s %d: between 0 (off) and %d", name, *v, int64(maxBudgetMicroUSD))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	sort.Strings(probs)
	return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(probs, "; "))
}

// Merge applies the overrides o over the daemon's values d, which must have
// every field set: the result is what a project actually uses. Extra egress
// names are added to the daemon's; role models are merged.
func (d ProjectSettings) Merge(o ProjectSettings) ProjectSettings {
	out := d
	pick := func(dst **string, v *string) {
		if v != nil {
			*dst = v
		}
	}
	pick(&out.Pack, o.Pack)
	pick(&out.CodexModel, o.CodexModel)
	pick(&out.SecretsDir, o.SecretsDir)
	pick(&out.Model, o.Model)
	pick(&out.GitHubRemote, o.GitHubRemote)
	pick(&out.Reviewer, o.Reviewer)
	pick(&out.AccessDecider, o.AccessDecider)
	pick(&out.WorkerMemory, o.WorkerMemory)
	pick(&out.MergeTarget, o.MergeTarget)
	pick(&out.OfficeLayout, o.OfficeLayout)
	if o.WorkerPids != nil {
		out.WorkerPids = o.WorkerPids
	}
	if o.LeadAccessHosts != nil {
		out.LeadAccessHosts = slices.Clone(o.LeadAccessHosts) // a policy, not a list to add to
	}
	if o.MaxAgents != nil {
		out.MaxAgents = o.MaxAgents
	}
	if o.MaxSubTasks != nil {
		out.MaxSubTasks = o.MaxSubTasks
	}
	if o.GitHub != nil {
		out.GitHub = o.GitHub
	}
	if o.ReviewRounds != nil {
		out.ReviewRounds = o.ReviewRounds
	}
	if o.BudgetSessionMicroUSD != nil {
		out.BudgetSessionMicroUSD = o.BudgetSessionMicroUSD
	}
	if o.BudgetGoalMicroUSD != nil {
		out.BudgetGoalMicroUSD = o.BudgetGoalMicroUSD
	}
	out.EgressAllow = slices.Compact(slices.Concat(d.EgressAllow, o.EgressAllow))
	out.RoleModels = maps.Clone(d.RoleModels)
	if out.RoleModels == nil {
		out.RoleModels = map[string]string{}
	}
	maps.Copy(out.RoleModels, o.RoleModels)
	out.RoleCLIs = maps.Clone(d.RoleCLIs)
	if out.RoleCLIs == nil {
		out.RoleCLIs = map[string]string{}
	}
	maps.Copy(out.RoleCLIs, o.RoleCLIs)
	return out
}

// complete reports which fields of the daemon's values are missing.
func (d ProjectSettings) complete() error {
	var missing []string
	for name, set := range map[string]bool{"pack": d.Pack != nil, "secrets_dir": d.SecretsDir != nil, "model": d.Model != nil,
		"codex_model": d.CodexModel != nil,
		"max_agents":  d.MaxAgents != nil, "max_sub_tasks": d.MaxSubTasks != nil, "github": d.GitHub != nil, "github_remote": d.GitHubRemote != nil,
		"reviewer": d.Reviewer != nil, "review_rounds": d.ReviewRounds != nil,
		"budget_session_micro_usd": d.BudgetSessionMicroUSD != nil, "budget_goal_micro_usd": d.BudgetGoalMicroUSD != nil,
		"access_decider": d.AccessDecider != nil, "worker_memory": d.WorkerMemory != nil,
		"worker_pids": d.WorkerPids != nil, "merge_target": d.MergeTarget != nil} {
		if !set {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("engine: Defaults must set every field; missing " + strings.Join(missing, ", "))
	}
	return nil
}

// PackName is the effective pack: "" for none.
func (s ProjectSettings) PackName() string {
	if s.Pack == nil || *s.Pack == PackNone {
		return ""
	}
	return *s.Pack
}

// Ptr returns a pointer to v, for settings literals.
func Ptr[T any](v T) *T { return &v }

// NeutralDefaults are daemon values that change nothing: no pack, the CLI's
// default model, the widest cap, no GitHub, no budgets. Tests and embedders
// start from them; `serve` builds its defaults from its flags.
func NeutralDefaults() ProjectSettings {
	return ProjectSettings{Pack: Ptr(PackNone), SecretsDir: Ptr("/"), Model: Ptr(""), RoleModels: map[string]string{},
		RoleCLIs: map[string]string{}, CodexModel: Ptr(""),
		MaxAgents: Ptr(MaxProjectAgents), MaxSubTasks: Ptr(DefaultMaxSubTasks), GitHub: Ptr(false), GitHubRemote: Ptr("origin"), Reviewer: Ptr(review.NameInternal),
		ReviewRounds: Ptr(2), BudgetSessionMicroUSD: Ptr(int64(0)), BudgetGoalMicroUSD: Ptr(int64(0)),
		AccessDecider: Ptr(DeciderUser), WorkerMemory: Ptr(""), WorkerPids: Ptr(0), MergeTarget: Ptr(DefaultMergeTarget)}
}

// SandboxChanges names the settings that change what a project's agents can
// reach (pack, secrets, network, pushes, the branch their work is merged into
// and pushed to) between s and t. The API accepts
// them from this machine only (ADR 0046).
func (s ProjectSettings) SandboxChanges(t ProjectSettings) []string {
	var out []string
	same := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	for _, f := range []struct {
		name string
		a, b any
	}{{"pack", s.Pack, t.Pack}, {"secrets_dir", s.SecretsDir, t.SecretsDir}, {"egress_allow", s.EgressAllow, t.EgressAllow},
		{"github", s.GitHub, t.GitHub}, {"github_remote", s.GitHubRemote, t.GitHubRemote},
		{"access_decider", s.AccessDecider, t.AccessDecider}, {"lead_access_hosts", s.LeadAccessHosts, t.LeadAccessHosts},
		{"merge_target", s.MergeTarget, t.MergeTarget}} {
		if !same(f.a, f.b) {
			out = append(out, f.name)
		}
	}
	return out
}

// OfficeInfo is the layout a project's office uses (Phase 18, ADR 0057): the
// default plan, or the project's own layout. A stored layout that does not
// validate falls back to the default, with its problems for the UI's warning.
type OfficeInfo struct {
	Source   string         `json:"source"` // OfficeDefault | OfficeCustom
	Layout   *office.Layout `json:"layout,omitempty"`
	Problems []string       `json:"problems,omitempty"`
}

// Where an office's layout comes from.
const (
	OfficeDefault = "default"
	OfficeCustom  = "custom"
)

// officeOf parses a project's office_layout setting.
func officeOf(s ProjectSettings) OfficeInfo {
	if s.OfficeLayout == nil || strings.TrimSpace(*s.OfficeLayout) == "" {
		return OfficeInfo{Source: OfficeDefault}
	}
	l, err := office.Parse(*s.OfficeLayout)
	if err != nil {
		return OfficeInfo{Source: OfficeDefault, Problems: office.Problems(err)}
	}
	return OfficeInfo{Source: OfficeCustom, Layout: &l}
}

// withoutLayout is s without its office layout: what the project's engine
// runs with.
func (s ProjectSettings) withoutLayout() ProjectSettings {
	s.OfficeLayout = nil
	return s
}

// ReopenChanges names the settings that differ between s and t and change
// what a project's engine is built from (its pack, network, secrets, GitHub
// client and roles' CLIs): only those reopen the project, which needs its
// agents stopped. Every other setting is read when an agent starts or a tool
// runs and applies to the running engine at once (ADR 0061).
func (s ProjectSettings) ReopenChanges(t ProjectSettings) []string {
	same := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	var out []string
	for _, f := range []struct {
		name string
		a, b any
	}{{"pack", s.Pack, t.Pack}, {"secrets_dir", s.SecretsDir, t.SecretsDir}, {"egress_allow", s.EgressAllow, t.EgressAllow},
		{"github", s.GitHub, t.GitHub}, {"github_remote", s.GitHubRemote, t.GitHubRemote},
		{"reviewer", s.Reviewer, t.Reviewer}, {"review_rounds", s.ReviewRounds, t.ReviewRounds},
		{"role_clis", s.RoleCLIs, t.RoleCLIs}} {
		if !same(f.a, f.b) {
			out = append(out, f.name)
		}
	}
	return out
}
