package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	pricing "github.com/OzanAlpkaya/agent-office/internal/usage"
)

// daemonDefaults are the serve flags as project settings: what every project
// uses unless it overrides it (ADR 0046).
func daemonDefaults(cfg serveConfig) engine.ProjectSettings {
	p := cfg.pack
	if p == "" {
		p = engine.PackNone
	}
	models := cfg.roleModels
	if models == nil {
		models = map[string]string{}
	}
	return engine.ProjectSettings{
		Pack: engine.Ptr(p), SecretsDir: engine.Ptr(cfg.secretsDir), EgressAllow: slices.Clone(cfg.egressAllow),
		Model: engine.Ptr(cfg.model), RoleModels: models, MaxAgents: engine.Ptr(cfg.maxAgents),
		GitHub: engine.Ptr(cfg.github), GitHubRemote: engine.Ptr(cfg.githubRemote), Reviewer: engine.Ptr(cfg.reviewer),
		ReviewRounds:          engine.Ptr(cfg.reviewRounds),
		BudgetSessionMicroUSD: engine.Ptr(int64(math.Round(cfg.budgetSessionUSD * 1e6))),
		BudgetGoalMicroUSD:    engine.Ptr(int64(math.Round(cfg.budgetGoalUSD * 1e6))),
	}
}

// configureProject resolves what a project's effective settings need from
// the outside world when it opens: its stack pack (image, allowlist,
// secrets), its GitHub client, and the checks `serve` used to do once.
func configureProject(cfg serveConfig, g git.Git, prices *pricing.Prices, log *slog.Logger) func(context.Context, engine.Project,
	engine.ProjectSettings, *engine.Options) error {
	return func(ctx context.Context, p engine.Project, eff engine.ProjectSettings, o *engine.Options) error {
		plog := log.With("project", p.Slug)
		roles := o.Roles
		if roles == nil {
			roles = role.MustBuiltins()
		}
		if err := checkBudgetModels(eff, roles, prices); err != nil {
			return err
		}
		if o.Sandbox == nil {
			if eff.PackName() != "" {
				return fmt.Errorf("%w: the stack pack %s needs sandboxed agents (--sandbox podman): a pack configures the "+
					"worker container, which host-mode agents do not have", engine.ErrInvalid, eff.PackName())
			}
			if *eff.GitHub {
				return fmt.Errorf("%w: GitHub needs sandboxed agents (--sandbox podman): host-mode agents run as you and "+
					"could read the GitHub token or change the git hooks the daemon's push runs", engine.ErrInvalid)
			}
			if o.Budgets.SessionMicroUSD > 0 || o.Budgets.GoalMicroUSD > 0 {
				o.Warnings = append(o.Warnings, "budgets apply to sandboxed agents only: host-mode agents reach Anthropic "+
					"directly and are not limited (ADR 0027)")
			}
			o.Warnings = append(o.Warnings, "Host mode: agents run as you, with permission prompts on. A command you allow can "+
				"read any project's files and reach other projects' agent sockets; isolation between projects holds for "+
				"sandboxed agents (--sandbox podman, ADR 0046).")
		} else {
			w, err := applyPack(ctx, o.Sandbox, packSpec{name: eff.PackName(), label: "pack", packsDir: cfg.packsDir,
				secretsDir: *eff.SecretsDir, egress: eff.EgressAllow, repo: p.RepoPath})
			if err != nil {
				return err
			}
			o.Warnings = append(o.Warnings, w...)
			plog.Info("stack pack", "pack", eff.PackName(), "image", o.Sandbox.Image, "egress_allow", strings.Join(o.Sandbox.AllowNames, ","))
		}
		if *eff.GitHub {
			gh, err := githubOptions(ctx, githubSpec{repo: p.RepoPath, remote: *eff.GitHubRemote, reviewer: *eff.Reviewer,
				rounds: *eff.ReviewRounds}, g, plog)
			if err != nil {
				return err
			}
			o.GitHub = gh
		}
		return nil
	}
}

// projectCommand is `agent-office project`: the running daemon's projects,
// changed over its control socket.
func projectCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-office project list | add <repo> [flags] | set <project> [flags] | remove <project> | retry <project>")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", envOr("AGENT_OFFICE_DATA_DIR", defaultDataDir()), "data dir of the daemon [AGENT_OFFICE_DATA_DIR]")
	var sf *settingsFlags
	name := ""
	switch sub {
	case "add", "set":
		fs.StringVar(&name, "name", "", "the project's name in the UI (default: the repository's directory name)")
		sf = addSettingsFlags(fs, sub == "set")
	case "list", "remove", "retry":
	default:
		fmt.Fprintf(stderr, "agent-office project: unknown command %q\n", sub)
		return 2
	}
	// The argument may come before or after the flags.
	var arg string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		arg, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if arg == "" && fs.NArg() > 0 {
		arg = fs.Arg(0)
	}
	if (sub == "list") != (arg == "") || fs.NArg() > 1 {
		fmt.Fprintf(stderr, "agent-office project %s: wrong arguments (agent-office project %s -h)\n", sub, sub)
		return 2
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "agent-office project:", err)
		return 1
	}
	rt, err := runtimeDir()
	if err != nil {
		fmt.Fprintln(stderr, "agent-office project:", err)
		return 1
	}
	c := controlClient(controlSocketPath(rt, dir))
	fail := func(err error) int {
		fmt.Fprintf(stderr, "agent-office project %s: %s\n", sub, explainControl(err, dir))
		return 1
	}
	path := "/projects/" + url.PathEscape(arg)
	switch sub {
	case "list":
		var ps []engine.ProjectInfo
		if err := controlDo(c, http.MethodGet, "/projects", nil, &ps, http.StatusOK); err != nil {
			return fail(err)
		}
		printProjects(stdout, ps)
	case "add":
		s, err := sf.settings(engine.ProjectSettings{})
		if err != nil {
			return fail(err)
		}
		repo, err := filepath.Abs(arg)
		if err != nil {
			return fail(err)
		}
		var p engine.ProjectInfo
		body := map[string]any{"repo": repo, "name": name, "settings": s}
		if err := controlDo(c, http.MethodPost, "/projects", body, &p, http.StatusCreated); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Added project %s (%s): %s\n", p.Name, p.ID, p.RepoPath)
	case "set":
		var cur engine.ProjectInfo
		if err := controlDo(c, http.MethodGet, path, nil, &cur, http.StatusOK); err != nil {
			return fail(err)
		}
		s, err := sf.settings(cur.Settings)
		if err != nil {
			return fail(err)
		}
		body := map[string]any{"settings": s}
		if name != "" {
			body["name"] = name
		}
		var p engine.ProjectInfo
		if err := controlDo(c, http.MethodPatch, path, body, &p, http.StatusOK); err != nil {
			return fail(err)
		}
		printProjects(stdout, []engine.ProjectInfo{p})
	case "remove":
		if err := controlDo(c, http.MethodDelete, path, nil, nil, http.StatusNoContent); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Removed project %s; its history, branches and worktrees are kept. Add the repository again to bring it back.\n", arg)
	case "retry":
		var p engine.ProjectInfo
		if err := controlDo(c, http.MethodPost, path+"/retry", nil, &p, http.StatusOK); err != nil {
			return fail(err)
		}
		printProjects(stdout, []engine.ProjectInfo{p})
	}
	return 0
}

func printProjects(w io.Writer, ps []engine.ProjectInfo) {
	if len(ps) == 0 {
		fmt.Fprintln(w, "No projects. Add one with `agent-office project add <repo>`.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tPACK\tMODEL\tMAX AGENTS\tREPOSITORY")
	for _, p := range ps {
		name := p.Name
		if p.Default {
			name += " (default)"
		}
		model := *p.Effective.Model
		if model == "" {
			model = "CLI default"
		}
		pk := p.Effective.PackName()
		if pk == "" {
			pk = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", p.ID, name, p.State, pk, model, *p.Effective.MaxAgents, p.RepoPath)
	}
	_ = tw.Flush()
	for _, p := range ps {
		if p.Error != "" {
			fmt.Fprintf(w, "\n%s: %s\n", p.Name, p.Error)
		}
	}
}

// settingsFlags are the project settings as flags of `project add` and
// `project set`: a flag that is not given leaves the setting as it is
// (inherited from the daemon, for add).
type settingsFlags struct {
	fs                                                       *flag.FlagSet
	pack, secretsDir, egress, model, roleModels, remote, rev *string
	maxAgents, rounds                                        *int
	github                                                   *bool
	budgetSession, budgetGoal                                *float64
	inherit                                                  *string
}

func addSettingsFlags(fs *flag.FlagSet, set bool) *settingsFlags {
	f := &settingsFlags{fs: fs}
	f.pack = fs.String("pack", "", "stack pack (`agent-office pack list`), or none")
	f.secretsDir = fs.String("secrets-dir", "", "where this project's pack secrets live (default: the daemon's --secrets-dir)")
	f.egress = fs.String("egress-allow", "", "extra host names its sandboxed agents may reach, comma-separated")
	f.model = fs.String("model", "", "default model for its agents, e.g. haiku")
	f.roleModels = fs.String("role-model", "", "models per role, e.g. architect=opus,worker=sonnet")
	f.maxAgents = fs.Int("max-agents", 0, "its agents at the same time (at most the daemon's --max-agents)")
	f.github = fs.Bool("github", false, "push finished work and open pull requests (sandboxed agents only)")
	f.remote = fs.String("github-remote", "", "remote to push to")
	f.rev = fs.String("reviewer", "", "internal, coderabbit or none")
	f.rounds = fs.Int("review-rounds", 0, "review feedback rounds sent to the worker automatically per pull request")
	f.budgetSession = fs.Float64("budget-session-usd", 0, "refuse API requests once a session's estimated cost reaches this; 0 = off")
	f.budgetGoal = fs.Float64("budget-goal-usd", 0, "refuse API requests once a goal's estimated cost reaches this; 0 = off")
	if set {
		f.inherit = fs.String("inherit", "", "settings to take from the daemon again, comma-separated, e.g. pack,model "+
			"(names as in the API: pack, secrets_dir, egress_allow, model, role_models, max_agents, github, github_remote, "+
			"reviewer, review_rounds, budget_session_micro_usd, budget_goal_micro_usd)")
	}
	return f
}

// settings applies the flags given on the command line over cur.
func (f *settingsFlags) settings(cur engine.ProjectSettings) (engine.ProjectSettings, error) {
	s := cur
	var err error
	f.fs.Visit(func(fl *flag.Flag) {
		if err != nil {
			return
		}
		switch fl.Name {
		case "pack":
			s.Pack = engine.Ptr(*f.pack)
		case "secrets-dir":
			var d string
			if d, err = filepath.Abs(*f.secretsDir); err == nil {
				s.SecretsDir = &d
			}
		case "egress-allow":
			s.EgressAllow = nil
			for _, h := range strings.Split(*f.egress, ",") {
				if h = strings.TrimSpace(h); h != "" {
					s.EgressAllow = append(s.EgressAllow, h)
				}
			}
		case "model":
			s.Model = engine.Ptr(*f.model)
		case "role-model":
			s.RoleModels, err = role.ParseModels(*f.roleModels)
		case "max-agents":
			s.MaxAgents = engine.Ptr(*f.maxAgents)
		case "github":
			s.GitHub = engine.Ptr(*f.github)
		case "github-remote":
			s.GitHubRemote = engine.Ptr(*f.remote)
		case "reviewer":
			s.Reviewer = engine.Ptr(*f.rev)
		case "review-rounds":
			s.ReviewRounds = engine.Ptr(*f.rounds)
		case "budget-session-usd":
			s.BudgetSessionMicroUSD, err = microUSD("--budget-session-usd", *f.budgetSession)
		case "budget-goal-usd":
			s.BudgetGoalMicroUSD, err = microUSD("--budget-goal-usd", *f.budgetGoal)
		}
	})
	if err != nil {
		return s, err
	}
	if f.inherit != nil {
		given := map[string]bool{}
		f.fs.Visit(func(fl *flag.Flag) { given[strings.ReplaceAll(fl.Name, "-", "_")] = true })
		for _, n := range strings.Split(*f.inherit, ",") {
			n = strings.TrimSpace(n)
			flagName := strings.TrimSuffix(strings.TrimSuffix(n, "_micro_usd"), "_usd")
			if given[flagName] || given[flagName+"_usd"] || (n == "role_models" && given["role_model"]) {
				return s, fmt.Errorf("--inherit %s and a flag that sets it: give one of them", n)
			}
			switch n {
			case "":
			case "pack":
				s.Pack = nil
			case "secrets_dir":
				s.SecretsDir = nil
			case "egress_allow":
				s.EgressAllow = nil
			case "model":
				s.Model = nil
			case "role_models":
				s.RoleModels = nil
			case "max_agents":
				s.MaxAgents = nil
			case "github":
				s.GitHub = nil
			case "github_remote":
				s.GitHubRemote = nil
			case "reviewer":
				s.Reviewer = nil
			case "review_rounds":
				s.ReviewRounds = nil
			case "budget_session_micro_usd", "budget_session_usd":
				s.BudgetSessionMicroUSD = nil
			case "budget_goal_micro_usd", "budget_goal_usd":
				s.BudgetGoalMicroUSD = nil
			default:
				return s, fmt.Errorf("--inherit: unknown setting %q", n)
			}
		}
	}
	return s, s.Validate()
}

func microUSD(flagName string, v float64) (*int64, error) {
	if v < 0 || v > 1e6 || math.IsNaN(v) {
		return nil, fmt.Errorf("%s must be between 0 and 1000000, got %v", flagName, v)
	}
	if v > 0 && math.Round(v*1e6) == 0 {
		return nil, errors.New(flagName + " is below $0.000001 and would turn the budget off; use 0 for off")
	}
	return engine.Ptr(int64(math.Round(v * 1e6))), nil
}
