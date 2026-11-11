package engine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

// Errors returned by the engine.
var (
	ErrNotFound       = errors.New("not found")
	ErrTooManyAgents  = errors.New("the maximum number of running agents is reached")
	ErrStillRunning   = errors.New("agent is still running; stop it first")
	ErrSession        = errors.New("hook session_id does not match the agent's session")
	ErrNoTerminal     = errors.New("terminal not available (the daemon restarted since this agent ran)")
	ErrWorktreeDirty  = errors.New("worktree has uncommitted changes; commit them or discard the worktree")
	ErrInvalid        = errors.New("invalid input")
	ErrTaskHasRunning = errors.New("task has a running agent; stop it first")
	ErrForbidden      = errors.New("forbidden")
	ErrLimit          = errors.New("limit reached")
	ErrLeadRunning    = errors.New("a lead agent is already running; stop it first")
	ErrDecided        = errors.New("access request was already decided")
	ErrFlagged        = errors.New("the branch has flagged commits; accept them before pushing")
	ErrNoGitHub       = errors.New("GitHub is not enabled; start the daemon with --github")
	ErrUpstream       = errors.New("git or GitHub failed") // push or GitHub API errors (ADR 0020)
	ErrShuttingDown   = errors.New("the daemon is shutting down")
)

// Exit reasons recorded for agents.
const (
	ExitProcess       = "process_exit"
	ExitStopped       = "stopped"
	ExitShutdown      = "daemon_shutdown"
	ExitDaemonRestart = "daemon_restart"
	ExitStartFailed   = "start_failed"
	ExitReported      = "reported_done"    // the agent called report_done and its turn ended (ADR 0017)
	ExitReviewed      = "review_submitted" // a reviewer called submit_review and its turn ended (ADR 0021)
	ExitFollowUp      = "follow_up"        // ended to be relaunched with review feedback (ADR 0022)
	ExitGoalDone      = "goal_done"        // the user marked the agent's goal done (Phase 12)
	ExitPlanApproved  = "plan_approved"    // an architect whose plan the user approved (ADR 0031)
	ExitResumeFailed  = "resume_failed"    // a resumed CLI exited before its first hook: no conversation to load (ADR 0035)
)

// Worktree statuses (ADR 0010).
const (
	WorktreeActive  = "active"
	WorktreeKept    = "kept"
	WorktreeRemoved = "removed"
)

// Sandbox modes (agents.sandbox) and workspace kinds (worktrees.kind), ADR 0014.
const (
	SandboxHost   = "host"   // the agent runs on the host with permission prompts on
	SandboxPodman = "podman" // the agent runs in a rootless Podman worker
	KindWorktree  = "worktree"
	KindClone     = "clone"
)

// Agent kinds (agents.kind): the behaviour the engine implements for an
// agent, which fixes its authority (ADR 0017, 0029). Authorization checks use
// the kind, never the role's name.
const (
	KindArchitect = string(role.Architect) // agrees the plan with the user (ADR 0031)
	KindLead      = string(role.Lead)      // the Tech Lead of a goal
	KindWorker    = string(role.Worker)
	KindQA        = string(role.QA)        // integrates and tests a goal (ADR 0033)
	KindReviewer  = string(role.Reviewer)  // reviews one pull request (ADR 0021)
	KindAssistant = string(role.Assistant) // briefs the user (ADR 0034)
)

// Names of the built-in roles (agents.role): data, see internal/role.
const (
	RoleArchitect = role.NameArchitect
	RoleTechLead  = role.NameTechLead
	RoleWorker    = role.NameWorker
	RoleQA        = role.NameQA
	RoleReviewer  = role.NameReviewer
	RoleAssistant = role.NameAssistant
	roleLeadAlias = "lead" // the Tech Lead's name before Phase 12, still accepted
)

// Options configures the engine. All paths must be absolute.
type Options struct {
	Repo                string        // any path inside the repository; set by the hub for its projects
	WorktreeRoot        string        // e.g. $XDG_DATA_HOME/agent-office/worktrees
	Store               *store.Store  // required
	Git                 git.Git       // git CLI wrapper
	MaxAgents           int           // this project's running agents at once; default 4 (ADR 0047)
	Model               string        // optional default model; a role's own model wins (ADR 0029)
	CodexModel          string        // Codex agents' default model; "": Codex's own (ADR 0050)
	Roles               *role.Set     // team roles (ADR 0029); nil: the built-in roles
	QA                  bool          // a goal's report needs a passing QA run (ADR 0033)
	InheritUserSettings bool          // host mode: load the user's own Claude Code settings
	ClaudeBin           string        // claude binary
	HookCommand         string        // shell command Claude Code runs for hooks
	RuntimeDir          string        // private dir for sockets and settings files
	BaseEnv             []string      // environment the agent env is derived from
	TermSize            terminal.Size // initial terminal size
	StopTimeout         time.Duration // SIGTERM -> SIGKILL grace period
	Logger              *slog.Logger
	Sandbox             *SandboxOptions // nil: host mode (permission prompts on)
	Warnings            []string        // shown in the UI (e.g. an untested Claude Code version)
	AgentOfficeBin      string          // this binary (absolute); host agents run `<bin> mcp` (ADR 0006)
	Version             string          // reported by the team MCP server
	GitHub              *GitHubOptions  // nil: no pushes or pull requests (ADR 0020)
	Prices              *usage.Prices   // cost estimates (ADR 0026); nil: the built-in table
	Budgets             Budgets         // sandboxed agents' spend limits (ADR 0027); zero: off
	EventRetention      time.Duration   // reduced events older than this are deleted (ADR 0028); 0: kept
	RestoreAgents       bool            // resume the agents the previous run's stop interrupted (ADR 0035)
	HistoryDir          string          // saved terminals of every session (ADR 0036); "": not saved
	AccessDecider       string          // who decides access requests (ADR 0053): DeciderUser (default), DeciderLead, DeciderLeadPolicy
	LeadAccessHosts     []string        // DeciderLeadPolicy: the hosts the lead may approve (allowlist entries)
	StallAfter          time.Duration   // the stall watchdog's wait (ADR 0052); 0: the default, negative: off

	// Message delivery timing (ADR 0018); zero values get the defaults. Tests
	// shorten them.
	deliverySettle time.Duration // idle this long before a message is typed
	typingQuiet    time.Duration // no user input for this long before typing
	enterDelay     time.Duration // between typing a message and pressing Enter
	usageThrottle  time.Duration // at most one live usage update per session this often

	// Set by the hub (ADR 0046); a standalone engine registers Repo itself and
	// has a pool of its own.
	project *db.Project
	slots   *slotPool // the daemon-wide cap
}

// Status is the daemon-wide state the UI shows next to the board.
type Status struct {
	Sandbox  string        `json:"sandbox"` // SandboxHost | SandboxPodman
	Image    string        `json:"image,omitempty"`
	Egress   []string      `json:"egress,omitempty"`        // sandboxed agents' allowlist
	Memory   string        `json:"worker_memory,omitempty"` // sandboxed workers' memory limit (ADR 0051)
	Pack     *PackStatus   `json:"pack,omitempty"`          // stack pack (ADR 0023)
	Warnings []string      `json:"warnings"`
	GitHub   *GitHubStatus `json:"github,omitempty"`
	Prices   PriceInfo     `json:"prices"`  // cost estimates (ADR 0026)
	Budgets  Budgets       `json:"budgets"` // enforced for sandboxed agents only (ADR 0027)
	QA       bool          `json:"qa"`      // goals need a passing QA run (ADR 0033)
}

// PackStatus is what the daemon's stack pack gives sandboxed agents. Secrets
// are names and whether their file exists, never values.
type PackStatus struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"` // "builtin" or the pack directory
	Egress      []string          `json:"egress"`
	Secrets     []pack.SecretInfo `json:"secrets"`
	Skills      []string          `json:"skills"`
	MCPServers  []string          `json:"mcp_servers"`
}

// Project is the repository the engine works on.
type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RepoPath string `json:"repo_path"`
	Slug     string `json:"slug"`
}

// AgentInfo is the public, JSON-serializable view of an agent.
type AgentInfo struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"` // a first name for people (ADR 0043); the id stays the key
	SessionID           string      `json:"session_id,omitempty"`
	TaskID              string      `json:"task_id,omitempty"`
	State               string      `json:"state"`
	Activity            string      `json:"activity,omitempty"`
	Model               string      `json:"model,omitempty"`
	InheritUserSettings bool        `json:"inherit_user_settings"`
	Sandbox             string      `json:"sandbox"` // SandboxHost | SandboxPodman
	CLI                 string      `json:"cli"`     // the agent CLI: CLIClaude | CLICodex (ADR 0050)
	Kind                string      `json:"kind"`    // the behaviour: KindLead, KindWorker, ...
	Role                string      `json:"role"`    // the role's name, e.g. tech-lead, worker, docs
	Branch              string      `json:"branch"`
	WorktreePath        string      `json:"worktree_path"`
	WorkspaceKind       string      `json:"workspace_kind"` // KindWorktree | KindClone
	WorktreeStatus      string      `json:"worktree_status"`
	HasTerminal         bool        `json:"has_terminal"`
	StartedAt           time.Time   `json:"started_at"`
	ExitedAt            *time.Time  `json:"exited_at,omitempty"`
	ExitCode            *int        `json:"exit_code,omitempty"`
	ExitReason          string      `json:"exit_reason,omitempty"`
	ExitSignal          string      `json:"exit_signal,omitempty"`  // the signal that ended it, e.g. KILL (ADR 0051)
	OOMKilled           bool        `json:"oom_killed,omitempty"`   // the out-of-memory killer ended it
	ExitDetail          string      `json:"exit_detail,omitempty"`  // why it ended, in plain words
	LastImport          *ImportInfo `json:"last_import,omitempty"`  // sandboxed agents (ADR 0014)
	Resumable           bool        `json:"resumable"`              // exited and can continue its conversation (ADR 0035)
	ResumedFrom         string      `json:"resumed_from,omitempty"` // the session this one continues
}

// Update is one change broadcast to subscribers.
type Update struct {
	// "agent" | "agent_removed" | "task" | "task_deleted" | "sandbox_event" | "message" | "access_request" |
	// "pull_request" | "usage" | "inbox" | "spec" | "qa_run" | "memory" | "memory_deleted"
	Type          string         `json:"type"`
	ProjectID     string         `json:"project_id"` // set by the engine: every update belongs to one project (ADR 0046)
	Agent         *AgentInfo     `json:"agent,omitempty"`
	Task          *Task          `json:"task,omitempty"`
	ID            string         `json:"id,omitempty"`
	SandboxEvent  *SandboxEvent  `json:"sandbox_event,omitempty"`
	Message       *Message       `json:"message,omitempty"`
	AccessRequest *AccessRequest `json:"access_request,omitempty"`
	PullRequest   *PullRequest   `json:"pull_request,omitempty"`
	Usage         *SessionUsage  `json:"usage,omitempty"`  // a running session's usage (ADR 0026)
	Inbox         *InboxItem     `json:"inbox,omitempty"`  // a message to the user (ADR 0030)
	Spec          *Spec          `json:"spec,omitempty"`   // a version of a goal's plan (ADR 0031)
	QARun         *QARun         `json:"qa_run,omitempty"` // a QA run (ADR 0033)
	Memory        *MemoryEntry   `json:"memory,omitempty"` // a project memory entry (ADR 0034)
}

type agentProc struct {
	info       AgentInfo
	status     hooks.Status
	baseCommit string
	term       *terminal.Session // nil for agents from a previous daemon run
	listener   *hooks.Listener
	settings   string
	exited     chan struct{}  // closed by watch after the exit is recorded
	afterExit  chan struct{}  // closed by watch after the exit import (sandbox); nil for agents of a previous run
	exitReason string         // set before stopping on purpose
	busy       bool           // a remove/discard is in progress
	sandbox    *sandboxRes    // nil in host mode and once the worker is gone
	events     eventLimit     // sandbox event rate limit
	importMu   sync.Mutex     // serializes imports and removal of a sandboxed agent
	mcp        *mcp.Listener  // team MCP socket; nil once the agent exited
	mcpConfig  string         // host mode: the --mcp-config file
	reviewID   int64          // reviewers: the review they were started for (ADR 0021)
	followup   *followupRound // workers relaunched for a review, until they report (ADR 0022); guarded by Engine.mu
	since      time.Time      // this session's start: older messages are not delivered to it (ADR 0022)
	heldSince  time.Time      // when it stopped taking messages (its report, or its exit): later ones wait for its next session (ADR 0051); guarded by Engine.mu
	usage      *sessionUsage  // this session's token usage (ADR 0026); nil for agents of a previous run
	log        *slog.Logger   // this session's logger: agent, session and task ids (ADR 0028)
	resumeFrom string         // the session this one continues (ADR 0035); "" for a fresh one
	cliSession string         // Codex: its own session id, learned from the first hook (ADR 0049); guarded by Engine.mu

	sessionStarted bool          // the main agent's SessionStart arrived; guarded by Engine.mu
	prompted       bool          // the session got a prompt (sessions.prompted, ADR 0035); guarded by Engine.mu
	historyDone    chan struct{} // closed after the session's terminal was saved for the last time (ADR 0036); nil for agents of a previous run

	// Message delivery (ADR 0018) and report_done (ADR 0017); guarded by Engine.mu.
	hasPrompt     bool        // started with an initial prompt
	turns         int         // turns finished (Stop or StopFailure of the main agent)
	lastInput     time.Time   // last terminal input from the user
	deliverTimer  *time.Timer // pending idle delivery
	delivering    bool        // a message is being typed into the PTY
	stopAfterTurn bool        // report_done: end the session when the turn ends
	reporting     bool        // a report_done call is running
}

// Engine owns agent and task lifecycles. All state changes go through it.
type Engine struct {
	opts    Options
	log     *slog.Logger
	adapter agent.ClaudeCode
	git     git.Git
	q       *db.Queries
	st      *store.Store
	project Project
	slots   *slotPool // the daemon-wide agent cap (ADR 0047)
	hubbed  bool      // opened by a hub, which does the daemon-wide restart work (ADR 0046)

	mu           sync.Mutex
	agents       map[string]*agentProc
	order        []string
	starting     int             // slots reserved by Start while git runs outside the lock
	startingKind map[string]bool // kinds of one-at-a-time agents being started (ADR 0017, 0029)
	naming       map[string]bool // names reserved by Start while git runs outside the lock (ADR 0043)
	assigning    map[string]bool // task ids an assign_task is starting a worker for
	subs         map[chan Update]struct{}
	wg           sync.WaitGroup

	gh       *ghState // GitHub integration; nil when off (ADR 0020)
	stopPoll context.CancelFunc
	stopBG   context.CancelFunc // retention loop (ADR 0028)
	ids      *sessionIDs        // running agents' sessions, for log lines (ADR 0028)
	closing  bool               // Shutdown began: no agent starts any more; guarded by mu

	leadHosts      sandbox.Allowlist      // Options.LeadAccessHosts, parsed (ADR 0053)
	stalls         map[string]*stallState // the stall watchdog's goals (ADR 0052); guarded by mu
	restartImports sync.WaitGroup         // sandbox imports after a restart; a restore waits for them
	noRestore      map[string]string      // agent id -> why a restore leaves it exited (set before the restore runs)
}

// New validates opts, registers the repository as a project, reconciles state
// left by a previous run and loads visible agents.
func New(ctx context.Context, opts Options) (*Engine, error) {
	if opts.project != nil {
		opts.Repo = opts.project.RepoPath
	}
	for name, p := range map[string]string{"Repo": opts.Repo, "RuntimeDir": opts.RuntimeDir, "WorktreeRoot": opts.WorktreeRoot} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("engine: %s must be an absolute path, got %q", name, p)
		}
	}
	if opts.HistoryDir != "" && !filepath.IsAbs(opts.HistoryDir) {
		return nil, fmt.Errorf("engine: HistoryDir must be an absolute path, got %q", opts.HistoryDir)
	}
	if opts.Store == nil || opts.ClaudeBin == "" || opts.HookCommand == "" {
		return nil, errors.New("engine: Store, ClaudeBin and HookCommand are required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ids := &sessionIDs{}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 5 * time.Second
	}
	if opts.MaxAgents <= 0 {
		opts.MaxAgents = 4
	}
	if !filepath.IsAbs(opts.AgentOfficeBin) {
		return nil, fmt.Errorf("engine: AgentOfficeBin must be an absolute path, got %q", opts.AgentOfficeBin)
	}
	if opts.deliverySettle <= 0 {
		opts.deliverySettle = 1500 * time.Millisecond
	}
	if opts.typingQuiet <= 0 {
		opts.typingQuiet = 5 * time.Second
	}
	if opts.enterDelay <= 0 {
		opts.enterDelay = 800 * time.Millisecond
	}
	if opts.usageThrottle <= 0 {
		opts.usageThrottle = 2 * time.Second
	}
	if opts.Prices == nil {
		opts.Prices = usage.Builtin()
	}
	if opts.Roles == nil {
		opts.Roles = role.MustBuiltins()
	}
	if b := opts.Budgets; b.SessionMicroUSD < 0 || b.GoalMicroUSD < 0 {
		return nil, fmt.Errorf("engine: budgets must not be negative, got %+v", b)
	}
	if opts.Sandbox != nil {
		if err := opts.Sandbox.validate(); err != nil {
			return nil, err
		}
	}
	switch opts.AccessDecider {
	case "", DeciderUser, DeciderLead, DeciderLeadPolicy:
	default:
		return nil, fmt.Errorf("engine: AccessDecider %q", opts.AccessDecider)
	}
	leadHosts, err := sandbox.ParseAllowlist(opts.LeadAccessHosts)
	if err != nil {
		return nil, fmt.Errorf("engine: LeadAccessHosts: %w", err)
	}
	for _, d := range []string{opts.RuntimeDir, opts.WorktreeRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
	}
	row := opts.project
	if row == nil { // standalone: register the repository like `serve --repo` (ADR 0046)
		r, _, err := registerProject(ctx, opts.Store.Q, opts.Git, opts.Repo, "", "")
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		row = &r
	} else if _, err := opts.Git.HeadCommit(ctx, row.RepoPath); err != nil {
		return nil, fmt.Errorf("engine: %s: %w", row.RepoPath, err)
	}
	slots := opts.slots
	if slots == nil {
		slots = newSlotPool(opts.MaxAgents)
	}
	opts.Logger = slog.New(newSessionHandler(opts.Logger.Handler(), ids)).With("project", row.Slug)
	e := &Engine{
		opts:         opts,
		log:          opts.Logger,
		ids:          ids,
		adapter:      agent.ClaudeCode{Bin: opts.ClaudeBin},
		git:          opts.Git,
		q:            opts.Store.Q,
		st:           opts.Store,
		project:      Project{ID: row.ID, Name: row.Name, RepoPath: row.RepoPath, Slug: row.Slug},
		slots:        slots,
		hubbed:       opts.project != nil,
		agents:       map[string]*agentProc{},
		subs:         map[chan Update]struct{}{},
		assigning:    map[string]bool{},
		startingKind: map[string]bool{},
		naming:       map[string]bool{},
		noRestore:    map[string]string{},
		leadHosts:    leadHosts,
		stalls:       map[string]*stallState{},
	}
	if opts.Sandbox != nil {
		if err := e.prepareSandbox(); err != nil {
			return nil, err
		}
	}
	if err := e.reconcile(ctx); err != nil {
		return nil, err
	}
	if opts.GitHub != nil {
		if err := e.startGitHub(ctx); err != nil {
			return nil, err
		}
	}
	bg, cancel := context.WithCancel(context.Background())
	e.stopBG = cancel
	e.wg.Add(1)
	go e.retentionLoop(bg)
	e.wg.Add(1)
	go e.watchdogLoop(bg)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.restore(bg) // after reconciliation and GitHub's restart rules (ADR 0035)
	}()
	return e, nil
}

// reconcile applies restart semantics (ADR 0011) and worktree cleanup (ADR 0010).
func (e *Engine) reconcile(ctx context.Context) error {
	before, err := e.q.ListVisibleAgents(ctx, e.project.ID)
	if err != nil {
		return fmt.Errorf("engine: load agents: %w", err)
	}
	var interrupted []string // sandboxed agents whose workers died with the daemon
	for _, r := range before {
		if r.State != string(hooks.Exited) && r.Sandbox == SandboxPodman && r.WorktreeStatus != WorktreeRemoved {
			interrupted = append(interrupted, r.ID)
		}
	}
	n, err := e.q.MarkUnfinishedAgentsExited(ctx, db.MarkUnfinishedAgentsExitedParams{ExitedAt: nullTime(now()), ProjectID: e.project.ID})
	if err != nil {
		return fmt.Errorf("engine: reconcile agents: %w", err)
	}
	if err := e.git.Prune(ctx, e.project.RepoPath); err != nil {
		e.log.Warn("git worktree prune failed", "err", err)
	}
	for _, status := range []string{WorktreeActive, WorktreeKept} {
		wts, err := e.q.ListWorktreesWithStatus(ctx, db.ListWorktreesWithStatusParams{ProjectID: e.project.ID, Status: status})
		if err != nil {
			return fmt.Errorf("engine: reconcile worktrees: %w", err)
		}
		for _, wt := range wts {
			if _, err := os.Stat(wt.Path); errors.Is(err, os.ErrNotExist) {
				_ = e.q.SetWorktreeStatus(ctx, db.SetWorktreeStatusParams{Status: WorktreeRemoved, RemovedAt: nullTime(now()), AgentID: wt.AgentID})
			}
		}
	}
	rows, err := e.q.ListVisibleAgents(ctx, e.project.ID)
	if err != nil {
		return fmt.Errorf("engine: load agents: %w", err)
	}
	for _, r := range rows {
		a := &agentProc{info: infoFromRow(r), status: hooks.Status{State: hooks.Exited}, exited: make(chan struct{})}
		if wt, err := e.q.GetWorktree(ctx, r.ID); err == nil {
			a.baseCommit = wt.BaseCommit
		}
		a.info.LastImport = e.loadLastImport(ctx, r.ID)
		close(a.exited)
		e.agents[r.ID] = a
		e.order = append(e.order, r.ID)
	}
	// ADR 0053: requests of agents that can be resumed stay open (an approval
	// applies to their next session); the others' expire.
	for _, id := range e.order {
		if a := e.agents[id]; !holdsMessages(a) {
			e.expireAccessLocked(ctx, id)
		}
	}
	// Agents recorded before names existed get one now, in start order (ADR 0043).
	for _, id := range e.order {
		a := e.agents[id]
		if a.info.Name != "" {
			continue
		}
		a.info.Name = e.nameLocked(id)
		if err := e.q.SetAgentName(ctx, db.SetAgentNameParams{Name: a.info.Name, ID: id}); err != nil {
			return fmt.Errorf("engine: name agent %s: %w", id, err)
		}
	}
	if n > 0 {
		e.log.Info("agents from the previous run marked exited", "count", n, "reason", ExitDaemonRestart)
	}
	e.expireQARuns(ctx)
	if e.opts.Sandbox != nil {
		e.reconcileSandbox(ctx, interrupted)
	}
	return nil
}

// Project returns the engine's project.
func (e *Engine) Project() Project { return e.project }

// Status returns the daemon-wide sandbox mode and warnings.
func (e *Engine) Status() Status {
	st := Status{Sandbox: SandboxHost, Warnings: append([]string{}, e.opts.Warnings...)}
	if sb := e.opts.Sandbox; sb != nil {
		st.Sandbox, st.Image, st.Egress = SandboxPodman, sb.Image, append([]string{}, sb.AllowNames...)
		st.Memory = e.workerMemory()
		if p := sb.Pack; p != nil {
			st.Pack = &PackStatus{Name: p.Name, Description: p.Description, Source: p.Source,
				Egress: append([]string{}, p.Egress...), Secrets: p.SecretStatus(sb.SecretsDir),
				Skills: append([]string{}, p.Skills...), MCPServers: p.ServerNames()}
		}
	}
	p := e.prices()
	st.Prices, st.Budgets = PriceInfo{AsOf: p.AsOf, Source: p.Source, File: p.File}, e.opts.Budgets
	st.QA = e.opts.QA
	if g := e.opts.GitHub; g != nil {
		st.GitHub = &GitHubStatus{Repo: g.Client.Repo().String(), Base: g.Base, Remote: g.Remote, Reviewer: e.gh.reviewer.Name(),
			Rounds: g.Rounds}
	}
	return st
}

// agentName is how an agent is named in commit trailers and messages.
func agentName(id string) string { return "agent-" + id[:6] }

// StartRequest describes a new agent.
type StartRequest struct {
	TaskID string // optional for workers; a lead's or architect's goal (ADR 0017)
	Role   string // a role name (ADR 0029); empty: worker; "lead": tech-lead

	// Set by the engine for reviewers (ADR 0021); never from the API.
	base      string            // start from this commit instead of HEAD
	reviewPR  int               // reviewers: the pull request number, for the branch name
	reviewID  int64             // reviewers: the review row
	prompt    string            // initial prompt instead of the task prompt
	qa        bool              // a QA run started by start_qa (ADR 0033)
	extraRefs map[string]string // sandbox: more branches in the agent's repository (QA's sub-task branches)
}

// Start creates a worktree and launches a new agent in it.
func (e *Engine) Start(ctx context.Context, req StartRequest) (AgentInfo, error) {
	def, err := e.resolveRole(req)
	if err != nil {
		return AgentInfo{}, err
	}
	role := string(def.Behaviour) // the kind; authority comes from it
	if err := e.checkCLI(def.CLI); err != nil {
		return AgentInfo{}, fmt.Errorf("role %s: %w", def.Name, err)
	}
	// The task first: an id of another project is not found, whatever the cap (ADR 0046).
	var task *db.Task
	if req.TaskID != "" {
		t, err := e.q.GetTask(ctx, req.TaskID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && t.ProjectID != e.project.ID) {
			return AgentInfo{}, fmt.Errorf("task %s: %w", req.TaskID, ErrNotFound)
		}
		if err != nil {
			return AgentInfo{}, err
		}
		if (role == KindLead || role == KindArchitect) && t.ParentTaskID.Valid {
			return AgentInfo{}, fmt.Errorf("%w: a sub-task cannot be a goal; start the %s on a top-level task", ErrInvalid, def.Name)
		}
		task = &t
	}

	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return AgentInfo{}, ErrShuttingDown
	}
	if single(role) && (e.startingKind[role] || e.runningKindLocked(role) != nil) {
		e.mu.Unlock()
		if role == KindLead {
			return AgentInfo{}, ErrLeadRunning
		}
		return AgentInfo{}, fmt.Errorf("%w: a %s is already running; stop it first", ErrInvalid, def.Name)
	}
	if err := e.reserveSlotLocked(); err != nil {
		e.mu.Unlock()
		return AgentInfo{}, err
	}
	if single(role) {
		e.startingKind[role] = true
	}
	e.starting++
	id := randomHex(6)
	name := e.nameLocked(id)
	e.naming[name] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.starting--
		delete(e.naming, name) // registered with the agent by now, or not needed
		if single(role) {
			delete(e.startingKind, role)
		}
		e.syncSlotsLocked()
		e.mu.Unlock()
	}()

	sid := newUUID()
	title := ""
	if task != nil {
		title = task.Title
	}
	branch := git.BranchName(title, id)
	switch role {
	case KindReviewer:
		branch = fmt.Sprintf("%sreview-pr%d-%s", git.BranchPrefix, req.reviewPR, id[:6])
	case KindArchitect, KindQA, KindAssistant:
		branch = git.BranchName(role+" "+title, id)
	}
	if req.base == "" && role == KindWorker && task != nil && task.ParentTaskID.Valid {
		// A sub-task starts from its goal's work: its dependencies, or the
		// approved spec (ADR 0031, 0032); a sub-task taken back from an exited
		// worker, from that worker's branch (ADR 0051).
		if _, head, ok := e.previousWork(ctx, task.ID); ok {
			req.base = head
		} else if req.base, err = e.subTaskBase(ctx, *task); err != nil {
			return AgentInfo{}, err
		}
	}
	var base string
	if req.base != "" {
		base, err = e.git.RevParse(ctx, e.project.RepoPath, req.base)
	} else {
		base, err = e.git.HeadCommit(ctx, e.project.RepoPath)
	}
	if err != nil {
		return AgentInfo{}, err
	}
	mode, kind := SandboxHost, KindWorktree
	wtPath := filepath.Join(e.opts.WorktreeRoot, e.project.Slug, id)
	var prep sandboxPrep
	var undoWorktree func()
	if e.opts.Sandbox != nil {
		// Everything that can be checked is checked before state is created.
		if prep, err = e.prepareSandboxStart(ctx, agentName(id), role); err != nil {
			return AgentInfo{}, err
		}
		mode, kind = SandboxPodman, KindClone
		wtPath = filepath.Join(e.opts.Sandbox.WorkspaceRoot, e.project.Slug, id)
		if undoWorktree, err = e.createClone(ctx, id, wtPath, branch, base, req.extraRefs); err != nil {
			return AgentInfo{}, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o700); err != nil {
			return AgentInfo{}, err
		}
		if err := e.git.AddWorktree(ctx, e.project.RepoPath, wtPath, branch, base); err != nil {
			return AgentInfo{}, fmt.Errorf("create worktree: %w", err)
		}
		undoWorktree = func() {
			cctx := context.Background()
			if err := e.git.RemoveWorktree(cctx, e.project.RepoPath, wtPath, true); err != nil {
				e.log.Warn("cleanup worktree", "path", wtPath, "err", err)
			}
			_ = e.git.DeleteBranch(cctx, e.project.RepoPath, branch)
		}
	}

	started := now()
	// The effective value: sandboxed agents never load the user's settings (ADR 0003).
	inherit := e.opts.InheritUserSettings && mode == SandboxHost
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, ProjectID: e.project.ID, TaskID: nullString(req.TaskID), Model: e.modelOf(def),
			InheritUserSettings: inherit, State: string(hooks.Starting), StartedAt: started,
			Sandbox: mode, Kind: role, Role: def.Name, Name: name, Cli: cliOr(def.CLI),
		}); err != nil {
			return err
		}
		if err := q.CreateWorktree(ctx, db.CreateWorktreeParams{AgentID: id, ProjectID: e.project.ID, Path: wtPath,
			Branch: branch, BaseCommit: base, CreatedAt: started, Kind: kind}); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sid, AgentID: id, StartedAt: started, ResumedFrom: ""}); err != nil {
			return err
		}
		if req.reviewID != 0 {
			// Recorded before the reviewer starts, so its submit_review finds the review.
			if _, err := q.StartReview(ctx, db.StartReviewParams{AgentID: nullString(id), UpdatedAt: started,
				ID: req.reviewID}); err != nil {
				return fmt.Errorf("review %d is not queued: %w", req.reviewID, err)
			}
		}
		if task != nil && task.Status != StatusInProgress && movesTask(role) {
			// ADR 0011: starting an agent for a task moves it to in_progress.
			t, err := q.UpdateTask(ctx, db.UpdateTaskParams{Title: task.Title, Description: task.Description,
				Status: StatusInProgress, UpdatedAt: started, ID: task.ID})
			if err != nil {
				return err
			}
			task = &t
		}
		return nil
	})
	if err != nil {
		undoWorktree()
		return AgentInfo{}, fmt.Errorf("record agent: %w", err)
	}

	var a AgentInfo
	prompt := req.prompt
	if prompt == "" {
		prompt = e.initialPrompt(ctx, def, task, branch)
	}
	if prompt != "" {
		var files []string
		if task != nil {
			files = jsonList(task.Files)
		}
		// ADR 0034, 0037: accepted notes whose scope includes this agent.
		prompt += e.memorySection(ctx, def.Name, files, slices.Contains(def.TeamTools, mcp.ToolListMemory))
		prompt += nameSection(name, id) // ADR 0043
	}
	l := launchInfo{id: id, sid: sid, name: name, role: role, def: def, branch: branch, path: wtPath, base: base, taskID: req.TaskID,
		started: started, prompt: prompt, reviewID: req.reviewID, cli: def.CLI}
	if e.opts.Sandbox != nil {
		a, err = e.launchSandboxed(l, prep)
	} else {
		a, err = e.launch(l)
	}
	if err != nil {
		undoWorktree()
		_ = e.q.MarkAgentExited(context.Background(), db.MarkAgentExitedParams{ExitedAt: nullTime(now()),
			ExitReason: ExitStartFailed, ID: id})
		_ = e.q.SetWorktreeStatus(context.Background(), db.SetWorktreeStatusParams{Status: WorktreeRemoved, RemovedAt: nullTime(now()), AgentID: id})
		_ = e.q.MarkAgentRemoved(context.Background(), db.MarkAgentRemovedParams{RemovedAt: nullTime(now()), ID: id})
		return AgentInfo{}, err
	}
	if task != nil && movesTask(role) {
		tv := taskFromRow(*task)
		e.publish(Update{Type: "task", Task: &tv})
	}
	return a, nil
}

// launchInfo is what Start (and a relaunch) hands to the launch paths.
type launchInfo struct {
	id, sid, branch, path, base, taskID string
	name                                string     // the agent's first name (ADR 0043); a relaunch keeps prev's
	role                                string     // the kind
	def                                 *role.Role // the role definition (ADR 0029)
	started                             time.Time
	prompt                              string         // initial prompt; empty for none
	reviewID                            int64          // reviewers (ADR 0021)
	prev                                *agentProc     // a relaunch: the exited agent it replaces (ADR 0022)
	followup                            *followupRound // a relaunch to address a review (ADR 0022)
	resumeFrom                          string         // a resume: the session whose conversation continues (ADR 0035)
	cli                                 string         // the agent CLI (ADR 0050); a relaunch keeps the agent's
	cliResume                           string         // Codex: its session id to continue (codex resume <id>, ADR 0049)
	since                               time.Time      // messages from this time on are delivered; zero: started
}

// roleName is the launched role's name ("" if a test left it out).
func (l launchInfo) roleName() string {
	if l.def == nil {
		return ""
	}
	return l.def.Name
}

// registerLocked adds a freshly launched agent, or replaces the exited one a
// relaunch started again (keeping its place, start time and last import).
func (e *Engine) registerLocked(a *agentProc, l launchInfo) {
	a.reviewID, a.since, a.followup, a.resumeFrom = l.reviewID, l.started, l.followup, l.resumeFrom
	a.cliSession, a.info.CLI = l.cliResume, l.cli
	a.info.ResumedFrom = l.resumeFrom
	if !l.since.IsZero() {
		a.since = l.since
	}
	a.info.Name = l.name
	if l.prev != nil {
		a.info.StartedAt, a.info.LastImport = l.prev.info.StartedAt, l.prev.info.LastImport
		a.info.WorktreeStatus = l.prev.info.WorktreeStatus
		a.info.Name = l.prev.info.Name
	}
	if _, ok := e.agents[a.info.ID]; !ok {
		e.order = append(e.order, a.info.ID)
	}
	e.agents[a.info.ID] = a
	e.ids.set(a.info.ID, a.info.SessionID, a.info.TaskID)
	e.publishAgentLocked(a)
}

// launch writes the agent's settings, opens its hook and MCP sockets and
// starts the CLI.
func (e *Engine) launch(l launchInfo) (AgentInfo, error) {
	id, sid, branch, wtPath, base, taskID, started := l.id, l.sid, l.branch, l.path, l.base, l.taskID, l.started
	dir := filepath.Join(e.opts.RuntimeDir, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AgentInfo{}, err
	}
	log := e.sessionLogger(l)
	settingsPath := filepath.Join(dir, id+".settings.json")
	settings, err := agent.HookSettings(e.opts.HookCommand, l.def.DenyRules())
	if err != nil {
		return AgentInfo{}, err
	}
	if err := os.WriteFile(settingsPath, settings, 0o600); err != nil {
		return AgentInfo{}, fmt.Errorf("write settings: %w", err)
	}
	ln, err := hooks.Listen(filepath.Join(dir, id+".sock"), e.hookHandler(id, log), log)
	if err != nil {
		os.Remove(settingsPath)
		return AgentInfo{}, err
	}
	mcpPath, mcpConfig := filepath.Join(dir, id+".mcp.sock"), filepath.Join(dir, id+".mcp.json")
	var mln *mcp.Listener
	cfg, err := agent.MCPConfig(e.opts.AgentOfficeBin, mcpPath, nil)
	if err == nil {
		err = os.WriteFile(mcpConfig, cfg, 0o600)
	}
	if err == nil {
		mln, err = e.listenMCP(mcpPath, id, l, log)
	}
	var cmd *exec.Cmd
	if err == nil {
		cmd, err = e.adapter.Command(agent.LaunchSpec{
			SessionID: sid, Model: e.modelOf(l.def), WorkDir: wtPath, SettingsPath: settingsPath,
			InheritUserSettings: e.opts.InheritUserSettings, SocketPath: ln.Path(), BaseEnv: e.opts.BaseEnv,
			InitialPrompt: l.prompt, MCPConfigPath: mcpConfig, ResumeFrom: l.resumeFrom,
		})
	}
	var term *terminal.Session
	if err == nil {
		term, err = terminal.Start(cmd, terminal.Options{Size: e.opts.TermSize})
	}
	if err != nil {
		ln.Close()
		if mln != nil {
			mln.Close()
		}
		os.Remove(settingsPath)
		os.Remove(mcpConfig)
		return AgentInfo{}, fmt.Errorf("start agent: %w", err)
	}

	a := &agentProc{
		info: AgentInfo{ID: id, SessionID: sid, TaskID: taskID, Model: e.modelOf(l.def), Sandbox: SandboxHost, Kind: l.role,
			Role:                l.def.Name,
			InheritUserSettings: e.opts.InheritUserSettings, Branch: branch, WorktreePath: wtPath,
			WorkspaceKind: KindWorktree, WorktreeStatus: WorktreeActive, HasTerminal: true, StartedAt: started},
		status:      hooks.Status{State: hooks.Starting},
		baseCommit:  base,
		term:        term,
		listener:    ln,
		settings:    settingsPath,
		exited:      make(chan struct{}),
		afterExit:   make(chan struct{}),
		historyDone: make(chan struct{}),
		mcp:         mln,
		mcpConfig:   mcpConfig,
		hasPrompt:   l.prompt != "",
		usage:       e.newSessionUsage(l, SandboxHost),
		log:         log,
	}
	e.mu.Lock()
	e.registerLocked(a, l)
	view := a.view() // under the lock: the agent's first hook may already be changing its state
	e.mu.Unlock()
	log.Info("agent started", "role", l.role, "relaunch", l.prev != nil, "pid", term.Pid(),
		"branch", branch, "worktree", wtPath, "model", e.modelOf(l.def), "role_name", l.def.Name,
		"inherit_user_settings", e.opts.InheritUserSettings)
	e.keepHistory(a)
	e.wg.Add(1)
	go e.watch(a)
	return view, nil
}

// watch marks the agent exited when its process ends, whether or not a
// SessionEnd hook arrived, and releases its socket and settings file.
func (e *Engine) watch(a *agentProc) {
	defer e.wg.Done()
	<-a.term.Done()
	_ = a.listener.Close()
	e.mu.Lock()
	mln := a.mcp
	a.mcp = nil
	if a.deliverTimer != nil {
		a.deliverTimer.Stop()
		a.deliverTimer = nil
	}
	e.mu.Unlock()
	if mln != nil {
		_ = mln.Close()
	}
	if a.mcpConfig != "" {
		_ = os.Remove(a.mcpConfig)
	}
	alog := a.logger(e)
	x := hostExit(a.term.ExitErr())
	if a.sandbox != nil {
		x = e.sandboxExit(a.sandbox, a.term.ExitErr(), alog) // before the container and its files are removed
		e.closeSandbox(a.sandbox, alog)
		e.mu.Lock()
		a.sandbox = nil
		e.mu.Unlock()
	} else {
		_ = os.Remove(a.settings)
	}

	e.mu.Lock()
	t := now()
	reason := a.exitReason
	if reason == "" {
		reason = ExitProcess
		if resumeFailed(a) {
			reason = ExitResumeFailed
		}
	}
	a.status = hooks.Status{State: hooks.Exited}
	code := x.code
	a.info.ExitedAt, a.info.ExitCode, a.info.ExitReason = &t, &code, reason
	if a.heldSince.IsZero() {
		a.heldSince = t
	}
	e.syncSlotsLocked()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.q.MarkAgentExited(ctx, db.MarkAgentExitedParams{ExitedAt: nullTime(t),
		ExitCode: sql.NullInt64{Int64: int64(code), Valid: true}, ExitReason: reason, ID: a.info.ID}); err != nil {
		alog.Error("persist agent exit", "err", err)
	}
	e.recordExitDetailLocked(ctx, a, x, alog)
	_ = e.q.EndSessions(ctx, db.EndSessionsParams{EndedAt: nullTime(t), AgentID: a.info.ID})
	if reason == ExitResumeFailed {
		// It never ran: a later resume continues the session before it (ADR 0035).
		if err := e.q.DeleteSession(ctx, a.info.SessionID); err != nil {
			alog.Warn("forget the failed resume's session", "err", err)
		}
	}
	e.publishAgentLocked(a)
	if !holdsMessages(a) {
		// ADR 0053: an agent that can be resumed keeps its requests open; an
		// approval applies to its next session.
		e.expireAccessLocked(ctx, a.info.ID)
	}
	e.mu.Unlock()
	if su := a.usage; su != nil {
		e.mu.Lock()
		su.ended = true
		e.stopSettleLocked(su)
		e.mu.Unlock()
		e.readTranscript(su, alog) // host mode: the rest of the transcript, now complete
		su.mu.Lock()
		su.view.EndedAt = &t
		su.mu.Unlock()
		e.publishUsage(su)
	}
	if reason == ExitResumeFailed {
		e.removeSessionHistory(a)
	}
	alog.Info("agent exited", "exit_code", code, "reason", reason, "signal", x.signal, "oom_killed", x.oom)
	close(a.exited)
	exitWork := 5 * time.Minute
	if e.shuttingDown() {
		exitWork = time.Minute // the daemon is stopping: bounded, the next start imports the rest
	}
	ictx, icancel := context.WithTimeout(context.Background(), exitWork)
	// ADR 0051: a worker's uncommitted work is committed before anything else
	// can happen to its workspace.
	saved, err := e.autosave(ictx, a, alog)
	if err != nil {
		alog.Warn("save uncommitted work on exit", "err", err)
	} else if saved != "" {
		alog.Info("uncommitted work saved on exit", "commit", short(saved))
	}
	if a.info.Sandbox == SandboxPodman && imports(a.info.Kind) {
		// ADR 0014: the work reaches the main repository on exit, before a
		// relaunch (ADR 0022) may start a new session in the workspace.
		_, _ = e.importAgent(ictx, a.info.ID, TriggerExit)
	}
	icancel()
	close(a.afterExit)
	if a.reviewID != 0 {
		e.reviewerExited(a.reviewID, a.info.ID)
	}
	if a.info.Kind == KindQA {
		e.qaExited(a.info.ID)
	}
	e.afterExit(a, x, saved, alog)
}

// parseHook reads a hook payload in the format of the agent's CLI.
func parseHook(cli string, payload []byte) (agent.Event, error) {
	if cli == role.CLICodex {
		return agent.ParseCodexHook(payload)
	}
	return agent.ParseClaudeHook(payload)
}

// checkSessionLocked refuses a hook of another session. Claude Code runs
// under the daemon's session id. Codex picks its own: the first SessionStart
// on the agent's socket names it (a resume knows it already), and later hooks
// must carry it (ADR 0049). The event then carries the daemon's id, like a
// Claude Code one.
func (e *Engine) checkSessionLocked(ctx context.Context, a *agentProc, ev *agent.Event, log *slog.Logger) error {
	if a.info.CLI != role.CLICodex {
		if ev.SessionID != a.info.SessionID {
			return ErrSession
		}
		return nil
	}
	if a.cliSession == "" {
		if ev.Kind != agent.SessionStarted || (ev.Detail != "startup" && ev.Detail != "resume") {
			return ErrSession
		}
		// Recorded before it is used, so a resume and a restore can always find
		// it; a failed write refuses the hook and the next SessionStart retries.
		if err := e.q.SetSessionCLIID(ctx, db.SetSessionCLIIDParams{CliSessionID: ev.SessionID, ID: a.info.SessionID}); err != nil {
			log.Error("record the Codex session id", "err", err)
			return err
		}
		a.cliSession = ev.SessionID
	}
	if ev.SessionID != a.cliSession {
		return ErrSession
	}
	ev.SessionID = a.info.SessionID
	return nil
}

func (e *Engine) hookHandler(id string, log *slog.Logger) hooks.Handler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		e.mu.Lock()
		cli := ""
		if a, ok := e.agents[id]; ok {
			cli = a.info.CLI
		}
		e.mu.Unlock()
		ev, err := parseHook(cli, payload)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		a, ok := e.agents[id]
		if !ok {
			return nil, ErrNotFound
		}
		if err := e.checkSessionLocked(ctx, a, &ev, log); err != nil {
			return nil, err
		}
		if ev.Kind == agent.SessionStarted && !ev.Subagent {
			a.sessionStarted = true
		}
		if ev.Kind == agent.TurnStarted && !ev.Subagent && !a.prompted {
			// The CLI keeps a conversation once it gets a prompt, finished turn or
			// not: a resume continues this session (ADR 0035).
			a.prompted = true
			if err := e.q.MarkSessionPrompted(ctx, a.info.SessionID); err != nil {
				log.Warn("record the session's prompt", "err", err)
			}
		}
		e.countLocked(ctx, a, ev, log)
		e.transcriptHookLocked(a, ev, log)
		return e.onHookLocked(ctx, a, ev, log), nil
	}
}

// applyLocked runs the state machine, records the reduced event and publishes
// a change.
func (e *Engine) applyLocked(ctx context.Context, a *agentProc, ev agent.Event, log *slog.Logger) {
	next := hooks.Next(a.status, ev)
	log.Debug("event", "hook", ev.Hook, "kind", ev.Kind, "tool", ev.Tool, "detail", ev.Detail,
		"subagent", ev.Subagent, "from", a.status.State, "state", next.State)
	if err := e.q.InsertEvent(ctx, db.InsertEventParams{AgentID: a.info.ID, SessionID: a.info.SessionID,
		Kind: string(ev.Kind), Hook: ev.Hook, Tool: ev.Tool, Detail: ev.Detail, State: string(next.State), At: now()}); err != nil {
		log.Warn("persist event", "err", err)
	}
	if next == a.status {
		return
	}
	a.status = next
	if err := e.q.UpdateAgentState(ctx, db.UpdateAgentStateParams{State: string(next.State), Activity: next.Activity, ID: a.info.ID}); err != nil {
		log.Warn("persist state", "err", err)
	}
	e.publishAgentLocked(a)
}

// Input sends terminal input from the UI to the agent. A lone Esc or Ctrl-C is
// also treated as a user interrupt (ADR 0008).
func (e *Engine) Input(id string, data []byte) error {
	e.mu.Lock()
	a, ok := e.agents[id]
	if ok {
		a.lastInput = time.Now()
	}
	e.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if a.term == nil {
		return ErrNoTerminal
	}
	if _, err := a.term.Write(data); err != nil {
		return err
	}
	if s := string(data); s == "\x1b" || s == "\x03" {
		e.mu.Lock()
		defer e.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.applyLocked(ctx, a, agent.Event{Kind: agent.UserInterrupted, SessionID: a.info.SessionID, Hook: "input"},
			a.logger(e))
		if a.status.State == hooks.Idle {
			a.turns++ // the interrupt ended the turn, which sends no Stop (ADR 0008)
			if a.stopAfterTurn {
				e.stopAfterReportLocked(a) // the report_done turn ended without a Stop
			} else {
				e.scheduleDeliveryLocked(ctx, a) // ADR 0018: an interrupted agent can take messages again
			}
		}
	}
	return nil
}

// Stop terminates a running agent and waits until it has exited.
func (e *Engine) Stop(id string) error { return e.stop(id, ExitStopped) }

func (e *Engine) stop(id, reason string) error {
	e.mu.Lock()
	a, ok := e.agents[id]
	if ok && a.term != nil && a.info.ExitedAt == nil && a.exitReason == "" {
		a.exitReason = reason
	}
	e.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if a.term == nil {
		return nil // already exited in a previous run
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.StopTimeout)
	defer cancel()
	a.term.Stop(ctx)
	<-a.exited
	return nil
}

// RemoveResult says what happened to an agent's worktree and branch.
type RemoveResult struct {
	Worktree string `json:"worktree"` // "removed" | "kept"
	Branch   string `json:"branch"`   // "deleted" | "kept"
}

// Remove forgets an exited agent and cleans up its worktree without losing
// work (ADR 0010): a dirty worktree is kept and ErrWorktreeDirty returned.
func (e *Engine) Remove(ctx context.Context, id string) (RemoveResult, error) {
	return e.removeAgent(ctx, id, false)
}

// Discard force-removes the agent's worktree, dropping uncommitted changes.
// The branch is kept if it has commits.
func (e *Engine) Discard(ctx context.Context, id string) (RemoveResult, error) {
	return e.removeAgent(ctx, id, true)
}

func (e *Engine) removeAgent(ctx context.Context, id string, force bool) (RemoveResult, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	switch {
	case !ok:
		e.mu.Unlock()
		return RemoveResult{}, ErrNotFound
	case a.status.State != hooks.Exited:
		e.mu.Unlock()
		return RemoveResult{}, ErrStillRunning
	case a.busy:
		e.mu.Unlock()
		return RemoveResult{}, fmt.Errorf("%w: removal already in progress", ErrInvalid)
	}
	a.busy = true
	info, base := a.info, a.baseCommit
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		a.busy = false
		e.mu.Unlock()
	}()
	if a.term != nil {
		<-a.exited // SessionEnd can arrive before the process is gone
	}
	if a.afterExit != nil {
		<-a.afterExit // the exit's saved work and import (ADR 0051) are done before the workspace goes
	}

	var res RemoveResult
	var err error
	if info.Sandbox == SandboxPodman {
		res, err = e.cleanClone(ctx, a, info, base, force)
	} else {
		res, err = e.cleanWorktree(ctx, info, base, force)
	}
	if err != nil {
		if errors.Is(err, ErrWorktreeDirty) {
			_ = e.q.SetWorktreeStatus(ctx, db.SetWorktreeStatusParams{Status: WorktreeKept, AgentID: id})
			e.mu.Lock()
			a.info.WorktreeStatus = WorktreeKept
			e.publishAgentLocked(a)
			e.mu.Unlock()
		}
		return res, err
	}
	if err := e.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetWorktreeStatus(ctx, db.SetWorktreeStatusParams{Status: WorktreeRemoved, RemovedAt: nullTime(now()), AgentID: id}); err != nil {
			return err
		}
		return q.MarkAgentRemoved(ctx, db.MarkAgentRemovedParams{RemovedAt: nullTime(now()), ID: id})
	}); err != nil {
		return res, err
	}
	e.mu.Lock()
	delete(e.agents, id)
	e.order = slices.DeleteFunc(e.order, func(s string) bool { return s == id })
	e.publishLocked(Update{Type: "agent_removed", ID: id})
	e.expireAccessLocked(ctx, id)
	e.mu.Unlock()
	if a.historyDone != nil {
		<-a.historyDone // the exit's last save, which would create the directory again
	}
	e.removeHistory(id)
	e.log.Info("agent removed", "agent_id", id, "worktree", res.Worktree, "branch", res.Branch, "discarded", force)
	return res, nil
}

func (e *Engine) cleanWorktree(ctx context.Context, info AgentInfo, base string, force bool) (RemoveResult, error) {
	repo := e.project.RepoPath
	if _, err := os.Stat(info.WorktreePath); err == nil && info.WorktreeStatus != WorktreeRemoved {
		if !force {
			dirty, err := e.git.IsDirty(ctx, info.WorktreePath)
			if err != nil {
				return RemoveResult{}, err
			}
			if dirty {
				return RemoveResult{Worktree: "kept", Branch: "kept"}, ErrWorktreeDirty
			}
		}
		if err := e.git.RemoveWorktree(ctx, repo, info.WorktreePath, force); err != nil {
			return RemoveResult{}, err
		}
	}
	return e.cleanBranch(ctx, info, base)
}

// cleanBranch deletes the agent's branch in the main repository if it has no
// commits; otherwise it is kept (ADR 0010).
func (e *Engine) cleanBranch(ctx context.Context, info AgentInfo, base string) (RemoveResult, error) {
	repo := e.project.RepoPath
	res := RemoveResult{Worktree: "removed", Branch: "kept"}
	exists, err := e.git.BranchExists(ctx, repo, info.Branch)
	if err != nil {
		return res, err
	}
	if !exists {
		res.Branch = "deleted"
		return res, nil
	}
	ahead, err := e.git.CommitsAhead(ctx, repo, base, info.Branch)
	if err != nil {
		return res, err
	}
	if ahead == 0 {
		if err := e.git.DeleteBranch(ctx, repo, info.Branch); err != nil {
			return res, err
		}
		res.Branch = "deleted"
	}
	return res, nil
}

// List returns all visible agents in start order.
func (e *Engine) List() []AgentInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AgentInfo, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.agents[id].view())
	}
	return out
}

// Get returns one agent.
func (e *Engine) Get(id string) (AgentInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok {
		return AgentInfo{}, ErrNotFound
	}
	return a.view(), nil
}

// Terminal returns the agent's terminal session (also after exit, for replay).
func (e *Engine) Terminal(id string) (*terminal.Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok {
		return nil, ErrNotFound
	}
	if a.term == nil {
		return nil, ErrNoTerminal
	}
	return a.term, nil
}

// Subscribe returns a channel of updates. The channel is closed if the
// subscriber falls behind or cancel is called; callers then resync.
func (e *Engine) Subscribe() (<-chan Update, func()) {
	ch := make(chan Update, 128)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
	}
}

func (e *Engine) publish(u Update) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishLocked(u)
}

func (e *Engine) publishAgentLocked(a *agentProc) {
	v := a.view()
	e.publishLocked(Update{Type: "agent", Agent: &v})
}

func (e *Engine) publishLocked(u Update) {
	u.ProjectID = e.project.ID
	for ch := range e.subs {
		select {
		case ch <- u:
		default:
			delete(e.subs, ch)
			close(ch)
		}
	}
}

// runningLeadLocked returns the running lead, if any.
func (e *Engine) runningLeadLocked() *agentProc { return e.runningKindLocked(KindLead) }

// runningKindLocked returns the running agent of kind, if any.
func (e *Engine) runningKindLocked(kind string) *agentProc {
	for _, a := range e.agents {
		if a.info.Kind == kind && a.running() {
			return a
		}
	}
	return nil
}

// running reports whether the agent's process runs in this daemon.
func (a *agentProc) running() bool { return a.term != nil && a.info.ExitedAt == nil }

// nameLocked chooses a first name for a new agent (ADR 0043): one no other
// agent in the list has (running or exited), nor a start in progress. When
// all are taken, one no running agent has; after that, a numbered name.
func (e *Engine) nameLocked(id string) string {
	listed, running := map[string]bool{}, map[string]bool{}
	for n := range e.naming {
		listed[n], running[n] = true, true
	}
	for _, a := range e.agents {
		listed[a.info.Name] = true
		if a.info.ExitedAt == nil {
			running[a.info.Name] = true
		}
	}
	free := 0
	for _, n := range firstNames {
		if !listed[n] {
			free++
		}
	}
	if free > 0 {
		return pickName(id, func(n string) bool { return listed[n] })
	}
	return pickName(id, func(n string) bool { return running[n] })
}

func (e *Engine) runningLocked() int {
	n := 0
	for _, a := range e.agents {
		if a.term != nil && a.info.ExitedAt == nil {
			n++
		}
	}
	return n
}

// Shutdown stops all running agents and waits for them.
// closeIdle marks the engine closing when no agent runs or starts, so the hub
// can shut it down without stopping anyone (ADR 0046). It reports whether the
// engine is closing now.
func (e *Engine) closeIdle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closing && e.runningLocked()+e.starting > 0 {
		return false
	}
	e.closing = true
	return true
}

func (e *Engine) Shutdown() {
	if e.stopPoll != nil {
		e.stopPoll() // first, so no reviewer or follow-up starts while agents stop
	}
	if e.stopBG != nil {
		e.stopBG()
	}
	e.mu.Lock()
	e.closing = true
	for e.starting > 0 { // a start that passed the check registers its agent first
		e.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		e.mu.Lock()
	}
	ids := slices.Clone(e.order)
	resume := e.resumeStatesLocked()
	e.mu.Unlock()
	e.saveResumeStates(resume)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.stop(id, ExitShutdown) }()
	}
	wg.Wait()
	e.wg.Wait()
	// Subscribers (the UI's events streams) end, so they resync: after a
	// project's settings change they find its new engine (ADR 0046).
	e.mu.Lock()
	for ch := range e.subs {
		delete(e.subs, ch)
		close(ch)
	}
	e.mu.Unlock()
}

// sessionLogger is the logger of one agent session: every line about it
// carries the agent, session and task ids (ADR 0028).
func (e *Engine) sessionLogger(l launchInfo) *slog.Logger {
	args := []any{"agent_id", l.id, "session_id", l.sid}
	if l.taskID != "" {
		args = append(args, "task_id", l.taskID)
	}
	return e.log.With(args...)
}

// logger is the agent's session logger, or one with its id for agents of a
// previous run.
func (a *agentProc) logger(e *Engine) *slog.Logger {
	if a.log != nil {
		return a.log
	}
	return e.log.With("agent_id", a.info.ID)
}

func (a *agentProc) view() AgentInfo {
	v := a.info
	v.State, v.Activity = string(a.status.State), a.status.Activity
	v.HasTerminal = a.term != nil
	v.Resumable = a.status.State == hooks.Exited && !a.busy && resumable(v.Kind) && v.WorktreeStatus != WorktreeRemoved
	return v
}

func infoFromRow(r db.ListVisibleAgentsRow) AgentInfo {
	info := AgentInfo{
		ID: r.ID, Name: r.Name, TaskID: r.TaskID.String, State: r.State, Model: r.Model,
		InheritUserSettings: r.InheritUserSettings && r.Sandbox == SandboxHost, Sandbox: r.Sandbox, CLI: r.Cli, Kind: r.Kind,
		Role: r.Role, Branch: r.Branch,
		WorktreePath:  r.WorktreePath,
		WorkspaceKind: r.WorktreeKind, WorktreeStatus: r.WorktreeStatus, StartedAt: r.StartedAt, ExitReason: r.ExitReason,
		ExitSignal: r.ExitSignal, OOMKilled: r.OomKilled, ExitDetail: r.ExitDetail,
	}
	if r.ExitedAt.Valid {
		t := r.ExitedAt.Time
		info.ExitedAt = &t
	}
	if r.ExitCode.Valid {
		c := int(r.ExitCode.Int64)
		info.ExitCode = &c
	}
	return info
}

func now() time.Time { return time.Now().UTC() }

func nullTime(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: true} }

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
