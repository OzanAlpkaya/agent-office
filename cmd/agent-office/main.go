// Command agent-office runs a team of AI coding agents on a git repository.
//
//	agent-office serve --repo <path>   run the daemon and web UI
//	agent-office hook                  forward a hook payload (stdin) to the daemon; used by agents
//	agent-office mcp                   team MCP server for an agent: stdio <-> the daemon (ADR 0006)
//	agent-office sandbox-init -- cmd   in-container init of a sandboxed worker (ADR 0016)
//	agent-office worker-image          build the worker container image with podman
//	agent-office pack list|check       list stack packs, validate a pack directory (ADR 0023)
//	agent-office role list|check       list team roles, validate a role directory (ADR 0029)
//	agent-office version               print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/api"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/review"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
	pricing "github.com/OzanAlpkaya/agent-office/internal/usage"
	"github.com/OzanAlpkaya/agent-office/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "agent-office:", err)
			os.Exit(1)
		}
	case "hook":
		os.Exit(hook())
	case "mcp":
		os.Exit(mcpBridge())
	case "sandbox-init":
		os.Exit(sandboxInit(os.Args[2:]))
	case "worker-image":
		if err := workerImage(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "agent-office:", err)
			os.Exit(1)
		}
	case "pack":
		os.Exit(packCommand(os.Args[2:], os.Stdout, os.Stderr))
	case "role":
		os.Exit(roleCommand(os.Args[2:], os.Stdout, os.Stderr))
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: agent-office <command>

commands:
  serve     run the daemon and web UI (agent-office serve -h for flags)
  hook      forward a hook payload from stdin to the daemon (used by agents)
  mcp       team MCP server over stdio, bridged to the daemon (used by agents)
  sandbox-init -- <command>
            in-container init of a sandboxed worker (started by the daemon)
  worker-image [--pack <name>] [--tag]
            build the base worker image, and a stack pack's image with --pack, with podman
            (--tag: print the tag only)
  pack list               list the built-in and your stack packs
  pack check <dir>...     validate stack pack directories and print every problem
  role list               list the built-in and your team roles
  role check <dir>...     validate role directories and print every problem
  version   print the version
`)
}

// workerImage builds the base worker image and, with --pack, the pack's image
// on top of it (ADR 0023). --tag prints the tag only.
func workerImage(args []string) error {
	fs := flag.NewFlagSet("worker-image", flag.ContinueOnError)
	tagOnly := fs.Bool("tag", false, "print the image tag and exit")
	name := fs.String("pack", os.Getenv("AGENT_OFFICE_PACK"), "also build this stack pack's image [AGENT_OFFICE_PACK]")
	defDir, err := pack.DefaultDir()
	if err != nil {
		return err
	}
	dir := fs.String("packs-dir", defDir, "your stack packs ["+pack.DirEnv+"]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var p *pack.Pack
	var files map[string]sandbox.BuildFile
	tag := sandbox.BaseImage()
	if *name != "" {
		if p, err = pack.Find(*name, *dir); err != nil {
			return err
		}
		if files, err = p.BuildFiles(); err != nil {
			return err
		}
		if tag, err = p.Image(); err != nil {
			return err
		}
	}
	if *tagOnly {
		fmt.Println(tag)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pm := sandbox.Podman{}
	have, err := pm.ImageExists(ctx, sandbox.BaseImage())
	if err != nil {
		return err
	}
	if p == nil || !have { // without --pack, always (re)build: podman's cache makes it cheap
		if err := pm.BuildBaseImage(ctx, os.Stderr); err != nil {
			return err
		}
	}
	if files != nil {
		if err := pm.BuildPackImage(ctx, p.Name, files, p.Containerfile, os.Stderr); err != nil {
			return err
		}
	} else if p != nil {
		fmt.Fprintf(os.Stderr, "stack pack %s has no toolchain; its agents use the base image\n", p.Name)
	}
	fmt.Println(tag)
	return nil
}

// sandboxInitFailed is the exit code when sandbox-init itself fails, so it is
// told apart from the agent's own exit codes.
const sandboxInitFailed = 125

// sandboxInit runs inside a worker container: it bridges the loopback proxy
// addresses to the mounted sockets and runs the agent CLI as its child.
func sandboxInit(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	// The tty delivers SIGINT/SIGQUIT to the agent itself; init must survive
	// them to keep the bridges up. SIGTERM/SIGHUP are passed on.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	code, err := sandbox.RunInit(ctx, sandbox.InitFromEnv(args))
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-office:", err)
		return sandboxInitFailed
	}
	return code
}

// hookTimeout must stay below the hook timeout in the generated settings
// (10 s), so the forwarder always exits on its own terms.
const hookTimeout = 8 * time.Second

// hook forwards one hook payload. It always exits 0: a broken daemon must not
// block or alter the agent; the failure is reported on stderr.
func hook() int {
	sock := os.Getenv(agent.SocketEnv)
	if sock == "" {
		fmt.Fprintln(os.Stderr, "agent-office hook:", agent.SocketEnv, "is not set")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	if err := hooks.Forward(ctx, sock, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agent-office hook:", err)
	}
	return 0
}

// mcpBridge is the stdio MCP server Claude Code starts for an agent: it
// pipes stdio to the agent's MCP socket in the daemon (ADR 0006). Unlike
// hook, a failure exits non-zero, so Claude Code shows the server as failed.
func mcpBridge() int {
	sock := os.Getenv(mcp.SocketEnv)
	if sock == "" {
		fmt.Fprintln(os.Stderr, "agent-office mcp:", mcp.SocketEnv, "is not set")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := mcp.Bridge(ctx, sock, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "agent-office mcp:", err)
		return 1
	}
	return 0
}

type serveConfig struct {
	addr                string
	repo                string
	dataDir             string
	maxAgents           int
	model               string
	claudeBin           string
	inheritUserSettings bool
	dev                 bool
	logLevel            string
	sandbox             string   // modePodman | modeHost
	egressAllow         []string // added to sandbox.DefaultEgressAllow
	sandboxMemory       string
	sandboxPids         int
	github              bool    // push and open pull requests (ADR 0020)
	githubRemote        string  // remote of the main repository to push to
	reviewer            string  // internal | coderabbit | none (ADR 0021)
	reviewRounds        int     // automatic follow-ups per pull request (ADR 0022)
	pack                string  // stack pack name (ADR 0023); empty: none
	packsDir            string  // user stack packs
	secretsDir          string  // values of the pack's env vars, one file per name
	pricesFile          string  // user price table (ADR 0026)
	pricesRequired      bool    // named explicitly: a missing file is an error
	budgetSessionUSD    float64 // per agent session (ADR 0027); 0: off
	budgetGoalUSD       float64 // per goal; 0: off
	eventRetentionDays  int     // ADR 0028; 0: keep events
	logFormat           string  // text | json
	rolesDir            string  // user roles (ADR 0029)
	roleModels          map[string]string
	qa                  bool // a goal's report needs a passing QA run (ADR 0033)
	restoreAgents       bool // resume the agents the previous run's stop interrupted (ADR 0035)
}

func parseServe(args []string) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var c serveConfig
	fs.StringVar(&c.addr, "addr", envOr("AGENT_OFFICE_ADDR", "127.0.0.1:7680"), "listen address; must be loopback [AGENT_OFFICE_ADDR]")
	fs.StringVar(&c.repo, "repo", os.Getenv("AGENT_OFFICE_REPO"), "repository the agent works in [AGENT_OFFICE_REPO]")
	fs.StringVar(&c.dataDir, "data-dir", envOr("AGENT_OFFICE_DATA_DIR", defaultDataDir()),
		"database and worktrees [AGENT_OFFICE_DATA_DIR]")
	maxAgents, err := envInt("AGENT_OFFICE_MAX_AGENTS", 4)
	if err != nil {
		return c, err
	}
	fs.IntVar(&c.maxAgents, "max-agents", maxAgents, "agents running at the same time [AGENT_OFFICE_MAX_AGENTS]")
	fs.StringVar(&c.model, "model", os.Getenv("AGENT_OFFICE_MODEL"), "model for agents, e.g. haiku; empty = CLI default [AGENT_OFFICE_MODEL]")
	fs.StringVar(&c.claudeBin, "claude-bin", envOr("AGENT_OFFICE_CLAUDE_BIN", "claude"), "Claude Code binary [AGENT_OFFICE_CLAUDE_BIN]")
	inherit, err := envBool("AGENT_OFFICE_INHERIT_USER_SETTINGS", true)
	if err != nil {
		return c, err
	}
	fs.BoolVar(&c.inheritUserSettings, "inherit-user-settings", inherit,
		"host mode: agents load your own Claude Code settings and hooks [AGENT_OFFICE_INHERIT_USER_SETTINGS]")
	fs.StringVar(&c.sandbox, "sandbox", envOr("AGENT_OFFICE_SANDBOX", modePodman),
		"podman: agents run in rootless Podman workers; host: unsandboxed, permission prompts on [AGENT_OFFICE_SANDBOX]")
	egress := fs.String("egress-allow", os.Getenv("AGENT_OFFICE_EGRESS_ALLOW"),
		"extra host names sandboxed agents may reach, comma-separated; .example.com for subdomains [AGENT_OFFICE_EGRESS_ALLOW]")
	fs.StringVar(&c.sandboxMemory, "sandbox-memory", envOr("AGENT_OFFICE_SANDBOX_MEMORY", sandbox.DefaultMemory),
		"memory limit per worker, e.g. 2g [AGENT_OFFICE_SANDBOX_MEMORY]")
	pids, err := envInt("AGENT_OFFICE_SANDBOX_PIDS", sandbox.DefaultPids)
	if err != nil {
		return c, err
	}
	fs.IntVar(&c.sandboxPids, "sandbox-pids", pids, "process limit per worker [AGENT_OFFICE_SANDBOX_PIDS]")
	gh, err := envBool("AGENT_OFFICE_GITHUB", false)
	if err != nil {
		return c, err
	}
	fs.BoolVar(&c.github, "github", gh, "push finished work and open pull requests on GitHub [AGENT_OFFICE_GITHUB]")
	fs.StringVar(&c.githubRemote, "github-remote", envOr("AGENT_OFFICE_GITHUB_REMOTE", "origin"),
		"remote of --repo to push to; its URL names the GitHub repository [AGENT_OFFICE_GITHUB_REMOTE]")
	fs.StringVar(&c.reviewer, "reviewer", envOr("AGENT_OFFICE_REVIEWER", review.NameInternal),
		"who reviews pull requests: internal (a reviewer agent), coderabbit or none [AGENT_OFFICE_REVIEWER]")
	rounds, err := envInt("AGENT_OFFICE_REVIEW_ROUNDS", 2)
	if err != nil {
		return c, err
	}
	fs.IntVar(&c.reviewRounds, "review-rounds", rounds,
		"review feedback rounds sent to the worker automatically per pull request [AGENT_OFFICE_REVIEW_ROUNDS]")
	fs.StringVar(&c.pack, "pack", os.Getenv("AGENT_OFFICE_PACK"),
		"stack pack for sandboxed agents: image, egress, secrets, skills, MCP servers (built in: go, node; "+
			"`agent-office pack list`); empty: the base image [AGENT_OFFICE_PACK]")
	packsDir, err := pack.DefaultDir()
	if err != nil {
		return c, err
	}
	fs.StringVar(&c.packsDir, "packs-dir", packsDir, "your stack packs, one directory each ["+pack.DirEnv+"]")
	secretsDir, err := pack.DefaultSecretsDir()
	if err != nil {
		return c, err
	}
	fs.StringVar(&c.secretsDir, "secrets-dir", secretsDir,
		"values of the pack's env vars, one user-only file per name ["+pack.SecretsDirEnv+"]")
	fs.BoolVar(&c.dev, "dev", false, "allow the Vite dev server origin (localhost:5173)")
	fs.StringVar(&c.logLevel, "log-level", envOr("AGENT_OFFICE_LOG_LEVEL", "info"), "debug | info | warn [AGENT_OFFICE_LOG_LEVEL]")
	fs.StringVar(&c.logFormat, "log-format", envOr("AGENT_OFFICE_LOG_FORMAT", "text"),
		"text | json; every line about an agent carries its session_id [AGENT_OFFICE_LOG_FORMAT]")
	pricesFile, err := pricing.DefaultPricesFile()
	if err != nil {
		return c, err
	}
	c.pricesRequired = os.Getenv(pricing.PricesFileEnv) != ""
	fs.StringVar(&c.pricesFile, "prices", pricesFile,
		"your price table for cost estimates, merged over the built-in one; optional at the default path ["+pricing.PricesFileEnv+"]")
	if c.budgetSessionUSD, err = envFloat("AGENT_OFFICE_BUDGET_SESSION_USD", 0); err != nil {
		return c, err
	}
	fs.Float64Var(&c.budgetSessionUSD, "budget-session-usd", c.budgetSessionUSD,
		"sandboxed agents: refuse API requests once one session's estimated cost reaches this; 0 = off [AGENT_OFFICE_BUDGET_SESSION_USD]")
	if c.budgetGoalUSD, err = envFloat("AGENT_OFFICE_BUDGET_GOAL_USD", 0); err != nil {
		return c, err
	}
	fs.Float64Var(&c.budgetGoalUSD, "budget-goal-usd", c.budgetGoalUSD,
		"sandboxed agents: refuse API requests once a goal's estimated cost (lead, workers, reviewers) reaches this; 0 = off [AGENT_OFFICE_BUDGET_GOAL_USD]")
	rolesDir, err := role.DefaultDir()
	if err != nil {
		return c, err
	}
	fs.StringVar(&c.rolesDir, "roles-dir", rolesDir, "your team roles, one directory each; one named like a built-in replaces it ["+
		role.DirEnv+"]")
	roleModels := fs.String("role-model", os.Getenv("AGENT_OFFICE_ROLE_MODELS"),
		"models per role, e.g. architect=opus,worker=sonnet; others use the role's model or --model [AGENT_OFFICE_ROLE_MODELS]")
	qa, err := envBool("AGENT_OFFICE_QA", true)
	if err != nil {
		return c, err
	}
	fs.BoolVar(&c.qa, "qa", qa, "a goal is reported only after QA passed on its sub-tasks' current work [AGENT_OFFICE_QA]")
	restore, err := envBool("AGENT_OFFICE_RESTORE_AGENTS", true)
	if err != nil {
		return c, err
	}
	fs.BoolVar(&c.restoreAgents, "restore-agents", restore,
		"at start, resume the conversations of the agents the previous run's stop interrupted [AGENT_OFFICE_RESTORE_AGENTS]")
	retention, err := envInt("AGENT_OFFICE_EVENT_RETENTION_DAYS", 30)
	if err != nil {
		return c, err
	}
	fs.IntVar(&c.eventRetentionDays, "event-retention-days", retention,
		"delete agent activity events older than this; 0 = keep [AGENT_OFFICE_EVENT_RETENTION_DAYS]")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prices" {
			c.pricesRequired = true
		}
	})
	if c.repo == "" {
		return c, errors.New("--repo is required")
	}
	if c.roleModels, err = role.ParseModels(*roleModels); err != nil {
		return c, err
	}
	if c.rolesDir, err = filepath.Abs(c.rolesDir); err != nil {
		return c, err
	}
	abs, err := filepath.Abs(c.repo)
	if err != nil {
		return c, err
	}
	c.repo = abs
	if c.dataDir, err = filepath.Abs(c.dataDir); err != nil {
		return c, err
	}
	if c.sandbox != modePodman && c.sandbox != modeHost {
		return c, fmt.Errorf("--sandbox must be %s or %s, got %q", modePodman, modeHost, c.sandbox)
	}
	for _, h := range strings.Split(*egress, ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.egressAllow = append(c.egressAllow, h)
		}
	}
	if _, err := sandbox.ParseAllowlist(c.egressAllow); err != nil {
		return c, fmt.Errorf("--egress-allow: %w", err)
	}
	if c.sandboxPids < 16 || c.sandboxPids > 65536 {
		return c, fmt.Errorf("--sandbox-pids must be between 16 and 65536, got %d", c.sandboxPids)
	}
	if c.github && c.sandbox == modeHost {
		// Host-mode agents run as you: they could read the GitHub token file or
		// change .git hooks that the daemon's push would then run (ADR 0020).
		return c, errors.New("--github needs sandboxed agents (--sandbox podman): host-mode agents run as you and " +
			"could read the GitHub token or change the git hooks the daemon's push runs")
	}
	if c.pack != "" && c.sandbox == modeHost {
		return c, errors.New("--pack needs sandboxed agents (--sandbox podman): a stack pack configures the worker " +
			"container (image, egress, secrets), which host-mode agents do not have")
	}
	for _, d := range []*string{&c.packsDir, &c.secretsDir} {
		if *d, err = filepath.Abs(*d); err != nil {
			return c, err
		}
	}
	switch c.reviewer {
	case review.NameInternal, review.NameCodeRabbit, engine.ReviewerNone:
	default:
		return c, fmt.Errorf("--reviewer must be %s, %s or %s, got %q", review.NameInternal, review.NameCodeRabbit,
			engine.ReviewerNone, c.reviewer)
	}
	if c.reviewRounds < 0 || c.reviewRounds > engine.MaxReviewRounds {
		return c, fmt.Errorf("--review-rounds must be between 0 and %d, got %d", engine.MaxReviewRounds, c.reviewRounds)
	}
	if c.logFormat != "text" && c.logFormat != "json" {
		return c, fmt.Errorf("--log-format must be text or json, got %q", c.logFormat)
	}
	for name, v := range map[string]float64{"--budget-session-usd": c.budgetSessionUSD, "--budget-goal-usd": c.budgetGoalUSD} {
		if v < 0 || v > 1e6 || math.IsNaN(v) {
			return c, fmt.Errorf("%s must be between 0 and 1000000, got %v", name, v)
		}
		if v > 0 && math.Round(v*1e6) == 0 {
			return c, fmt.Errorf("%s %v is below $0.000001 and would turn the budget off; use 0 for off", name, v)
		}
	}
	if c.eventRetentionDays < 0 || c.eventRetentionDays > 3650 {
		return c, fmt.Errorf("--event-retention-days must be between 0 and 3650, got %d", c.eventRetentionDays)
	}
	if c.pricesFile != "" {
		if c.pricesFile, err = filepath.Abs(c.pricesFile); err != nil {
			return c, err
		}
	}
	if c.maxAgents < 1 || c.maxAgents > 32 {
		return c, fmt.Errorf("--max-agents must be between 1 and 32, got %d", c.maxAgents)
	}
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return c, fmt.Errorf("--addr: %w", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return c, fmt.Errorf("--addr must be a loopback address, got %q (remote access is a later phase)", c.addr)
	}
	return c, nil
}

func serve(args []string) error {
	cfg, err := parseServe(args)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		return fmt.Errorf("--log-level: %w", err)
	}
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	if cfg.logFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	log := slog.New(handler)
	prices, err := pricing.Load(cfg.pricesFile, cfg.pricesRequired)
	if err != nil {
		return fmt.Errorf("price table: %w", err)
	}
	// Money is integer from here on (ADR 0026): the flags' dollars become micro-USD.
	budgets := engine.Budgets{SessionMicroUSD: int64(math.Round(cfg.budgetSessionUSD * 1e6)),
		GoalMicroUSD: int64(math.Round(cfg.budgetGoalUSD * 1e6))}
	roles, err := role.Load(cfg.rolesDir)
	if err != nil {
		return fmt.Errorf("roles: %w", err)
	}
	if roles, err = roles.WithModels(cfg.roleModels); err != nil {
		return err
	}
	for _, r := range roles.List() {
		if r.Model == "" {
			continue
		}
		if err := checkBudgetModel(budgets, r.Model, prices); err != nil {
			return fmt.Errorf("role %s: %w", r.Name, err)
		}
	}
	if err := checkBudgetModel(budgets, cfg.model, prices); err != nil {
		return err
	}

	claudeBin, err := exec.LookPath(cfg.claudeBin)
	if err != nil {
		return fmt.Errorf("claude binary: %w", err)
	}
	ci, err := resolveClaude(claudeBin)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	runtimeDir, err := runtimeDir()
	if err != nil {
		return err
	}

	var sb *engine.SandboxOptions
	var packWarnings []string
	if cfg.sandbox == modePodman {
		if sb, packWarnings, err = sandboxOptions(context.Background(), cfg, self, ci, log); err != nil {
			return err
		}
	} else {
		log.Warn("host mode: agents run unsandboxed on this machine, with permission prompts on")
	}
	warnings := append(versionWarnings(ci, cfg.sandbox), packWarnings...)
	if (budgets.SessionMicroUSD > 0 || budgets.GoalMicroUSD > 0) && cfg.sandbox == modeHost {
		warnings = append(warnings, "budgets apply to sandboxed agents only: host-mode agents reach Anthropic directly "+
			"and are not limited (ADR 0027)")
	}
	for _, w := range warnings {
		log.Warn(w)
	}

	g := git.Git{}
	gitVersion, err := g.Version(context.Background())
	if err != nil {
		return fmt.Errorf("git is required: %w", err)
	}
	var ghOpts *engine.GitHubOptions
	if cfg.github {
		if ghOpts, err = githubOptions(context.Background(), cfg, g, log); err != nil {
			return err
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(cfg.dataDir, "agent-office.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if m := st.Migration; m.From != m.To {
		log.Info("database migrated", "from", m.From, "to", m.To, "backup", m.Backup)
	}

	eng, err := engine.New(context.Background(), engine.Options{
		Repo: cfg.repo, WorktreeRoot: filepath.Join(cfg.dataDir, "worktrees"), Store: st, Git: g,
		MaxAgents: cfg.maxAgents, Model: cfg.model, InheritUserSettings: cfg.inheritUserSettings,
		ClaudeBin: claudeBin, HookCommand: agent.ShellQuote(self) + " hook",
		RuntimeDir: runtimeDir, BaseEnv: os.Environ(),
		TermSize: terminal.Size{Cols: 120, Rows: 36}, Logger: log,
		Sandbox: sb, Warnings: warnings, AgentOfficeBin: self, Version: version, GitHub: ghOpts,
		Roles: roles, QA: cfg.qa, RestoreAgents: cfg.restoreAgents,
		Prices: prices, Budgets: budgets, EventRetention: time.Duration(cfg.eventRetentionDays) * 24 * time.Hour,
	})
	if err != nil {
		return err
	}

	ui, built := web.FS()
	var devOrigins []string
	if cfg.dev {
		devOrigins = []string{"localhost:5173", "127.0.0.1:5173"}
	}
	_, port, _ := net.SplitHostPort(cfg.addr)
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.New(eng, ui, api.Options{Port: port, DevOrigins: devOrigins, Version: version, Logger: log}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info("agent-office starting", "version", version, "addr", "http://"+cfg.addr, "repo", eng.Project().RepoPath,
		"data_dir", cfg.dataDir, "max_agents", cfg.maxAgents, "git_version", gitVersion,
		"claude", ci.path, "claude_version", ci.version, "sandbox", cfg.sandbox, "model", cfg.model,
		"inherit_user_settings", cfg.inheritUserSettings, "github", cfg.github, "ui_built", built, "dev", cfg.dev,
		"prices_as_of", prices.AsOf, "prices_file", prices.File, "budget_session_usd", cfg.budgetSessionUSD,
		"budget_goal_usd", cfg.budgetGoalUSD, "event_retention_days", cfg.eventRetentionDays)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		eng.Shutdown()
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	eng.Shutdown()
	return nil
}

// runtimeDir returns a private directory for sockets and settings files.
func runtimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join(os.TempDir(), "agent-office-"+strconv.Itoa(os.Getuid()))
	}
	dir := filepath.Join(base, "agent-office")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("runtime dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("runtime dir: %w", err)
	}
	return dir, nil
}

// claudeVersion logs which CLI version hook payloads were produced by.
func claudeVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// defaultDataDir is $XDG_DATA_HOME/agent-office (default ~/.local/share/agent-office).
func defaultDataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "agent-office")
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

// checkBudgetModel refuses to start with a budget when --model names a model
// the price table does not know, since its spend could not be measured (ADR
// 0027). Aliases such as "haiku" resolve inside Claude Code and are checked
// per request instead (an unpriced model is refused then).
func checkBudgetModel(b engine.Budgets, model string, p *pricing.Prices) error {
	if b.SessionMicroUSD <= 0 && b.GoalMicroUSD <= 0 || !strings.HasPrefix(model, "claude-") {
		return nil
	}
	if _, ok := p.Lookup(model); !ok {
		return fmt.Errorf("--model %s has no price in the price table, so a budget cannot be enforced; "+
			"add it to %s or turn the budgets off", model, pricing.PricesFileEnv)
	}
	return nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
