package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"github.com/OzanAlpkaya/agent-office/internal/store"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
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
)

// Exit reasons recorded for agents.
const (
	ExitProcess       = "process_exit"
	ExitStopped       = "stopped"
	ExitShutdown      = "daemon_shutdown"
	ExitDaemonRestart = "daemon_restart"
	ExitStartFailed   = "start_failed"
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

// Options configures the engine. All paths must be absolute.
type Options struct {
	Repo                string        // any path inside the repository
	WorktreeRoot        string        // e.g. $XDG_DATA_HOME/agent-office/worktrees
	Store               *store.Store  // required
	Git                 git.Git       // git CLI wrapper
	MaxAgents           int           // running agents at once; default 4
	Model               string        // optional default model
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
}

// Status is the daemon-wide state the UI shows next to the board.
type Status struct {
	Sandbox  string   `json:"sandbox"` // SandboxHost | SandboxPodman
	Image    string   `json:"image,omitempty"`
	Warnings []string `json:"warnings"`
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
	ID                  string     `json:"id"`
	SessionID           string     `json:"session_id,omitempty"`
	TaskID              string     `json:"task_id,omitempty"`
	State               string     `json:"state"`
	Activity            string     `json:"activity,omitempty"`
	Model               string     `json:"model,omitempty"`
	InheritUserSettings bool       `json:"inherit_user_settings"`
	Sandbox             string     `json:"sandbox"` // SandboxHost | SandboxPodman
	Branch              string     `json:"branch"`
	WorktreePath        string     `json:"worktree_path"`
	WorkspaceKind       string     `json:"workspace_kind"` // KindWorktree | KindClone
	WorktreeStatus      string     `json:"worktree_status"`
	HasTerminal         bool       `json:"has_terminal"`
	StartedAt           time.Time  `json:"started_at"`
	ExitedAt            *time.Time `json:"exited_at,omitempty"`
	ExitCode            *int       `json:"exit_code,omitempty"`
	ExitReason          string     `json:"exit_reason,omitempty"`
}

// Update is one change broadcast to subscribers.
type Update struct {
	Type         string        `json:"type"` // "agent" | "agent_removed" | "task" | "task_deleted" | "sandbox_event"
	Agent        *AgentInfo    `json:"agent,omitempty"`
	Task         *Task         `json:"task,omitempty"`
	ID           string        `json:"id,omitempty"`
	SandboxEvent *SandboxEvent `json:"sandbox_event,omitempty"`
}

type agentProc struct {
	info       AgentInfo
	status     hooks.Status
	baseCommit string
	term       *terminal.Session // nil for agents from a previous daemon run
	listener   *hooks.Listener
	settings   string
	exited     chan struct{} // closed by watch after the exit is recorded
	exitReason string        // set before stopping on purpose
	busy       bool          // a remove/discard is in progress
	sandbox    *sandboxRes   // nil in host mode
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

	mu       sync.Mutex
	agents   map[string]*agentProc
	order    []string
	starting int // slots reserved by Start while git runs outside the lock
	subs     map[chan Update]struct{}
	wg       sync.WaitGroup
}

// New validates opts, registers the repository as a project, reconciles state
// left by a previous run and loads visible agents.
func New(ctx context.Context, opts Options) (*Engine, error) {
	for name, p := range map[string]string{"Repo": opts.Repo, "RuntimeDir": opts.RuntimeDir, "WorktreeRoot": opts.WorktreeRoot} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("engine: %s must be an absolute path, got %q", name, p)
		}
	}
	if opts.Store == nil || opts.ClaudeBin == "" || opts.HookCommand == "" {
		return nil, errors.New("engine: Store, ClaudeBin and HookCommand are required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 5 * time.Second
	}
	if opts.MaxAgents <= 0 {
		opts.MaxAgents = 4
	}
	if opts.Sandbox != nil {
		if err := opts.Sandbox.validate(); err != nil {
			return nil, err
		}
	}
	for _, d := range []string{opts.RuntimeDir, opts.WorktreeRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
	}
	top, err := opts.Git.TopLevel(ctx, opts.Repo)
	if err != nil {
		return nil, fmt.Errorf("engine: %s is not inside a git repository: %w", opts.Repo, err)
	}
	if _, err := opts.Git.HeadCommit(ctx, top); err != nil {
		return nil, fmt.Errorf("engine: repository has no commits yet: %w", err)
	}
	sum := sha256.Sum256([]byte(top))
	slug := git.Slug(filepath.Base(top), 30)
	if slug == "" {
		slug = "repo"
	}
	slug += "-" + hex.EncodeToString(sum[:])[:8]
	row, err := opts.Store.Q.UpsertProject(ctx, db.UpsertProjectParams{
		ID: randomHex(6), Name: filepath.Base(top), RepoPath: top, Slug: slug, CreatedAt: now(),
	})
	if err != nil {
		return nil, fmt.Errorf("engine: register project: %w", err)
	}
	e := &Engine{
		opts:    opts,
		log:     opts.Logger,
		adapter: agent.ClaudeCode{Bin: opts.ClaudeBin},
		git:     opts.Git,
		q:       opts.Store.Q,
		st:      opts.Store,
		project: Project{ID: row.ID, Name: row.Name, RepoPath: row.RepoPath, Slug: row.Slug},
		agents:  map[string]*agentProc{},
		subs:    map[chan Update]struct{}{},
	}
	if opts.Sandbox != nil {
		if err := e.prepareSandbox(); err != nil {
			return nil, err
		}
	}
	if err := e.reconcile(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// reconcile applies restart semantics (ADR 0011) and worktree cleanup (ADR 0010).
func (e *Engine) reconcile(ctx context.Context) error {
	n, err := e.q.MarkUnfinishedAgentsExited(ctx, nullTime(now()))
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
		close(a.exited)
		e.agents[r.ID] = a
		e.order = append(e.order, r.ID)
	}
	if n > 0 {
		e.log.Info("agents from the previous run marked exited", "count", n, "reason", ExitDaemonRestart)
	}
	return nil
}

// Project returns the engine's project.
func (e *Engine) Project() Project { return e.project }

// Status returns the daemon-wide sandbox mode and warnings.
func (e *Engine) Status() Status {
	st := Status{Sandbox: SandboxHost, Warnings: append([]string{}, e.opts.Warnings...)}
	if e.opts.Sandbox != nil {
		st.Sandbox, st.Image = SandboxPodman, e.opts.Sandbox.Image
	}
	return st
}

// agentName is how an agent is named in commit trailers until agents get
// names of their own (Phase 4).
func agentName(id string) string { return "agent-" + id[:6] }

// StartRequest describes a new agent.
type StartRequest struct {
	TaskID string // optional: the agent works on this task
}

// Start creates a worktree and launches a new agent in it.
func (e *Engine) Start(ctx context.Context, req StartRequest) (AgentInfo, error) {
	e.mu.Lock()
	if e.runningLocked()+e.starting >= e.opts.MaxAgents {
		e.mu.Unlock()
		return AgentInfo{}, ErrTooManyAgents
	}
	e.starting++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.starting--
		e.mu.Unlock()
	}()

	var task *db.Task
	if req.TaskID != "" {
		t, err := e.q.GetTask(ctx, req.TaskID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && t.ProjectID != e.project.ID) {
			return AgentInfo{}, fmt.Errorf("task %s: %w", req.TaskID, ErrNotFound)
		}
		if err != nil {
			return AgentInfo{}, err
		}
		task = &t
	}

	id, sid := randomHex(6), newUUID()
	title := ""
	if task != nil {
		title = task.Title
	}
	branch := git.BranchName(title, id)
	base, err := e.git.HeadCommit(ctx, e.project.RepoPath)
	if err != nil {
		return AgentInfo{}, err
	}
	mode, kind := SandboxHost, KindWorktree
	wtPath := filepath.Join(e.opts.WorktreeRoot, e.project.Slug, id)
	var prep sandboxPrep
	var undoWorktree func()
	if e.opts.Sandbox != nil {
		// Everything that can be checked is checked before state is created.
		if prep, err = e.prepareSandboxStart(ctx, agentName(id)); err != nil {
			return AgentInfo{}, err
		}
		mode, kind = SandboxPodman, KindClone
		wtPath = filepath.Join(e.opts.Sandbox.WorkspaceRoot, e.project.Slug, id)
		if undoWorktree, err = e.createClone(ctx, id, wtPath, branch, base); err != nil {
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
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, ProjectID: e.project.ID, TaskID: nullString(req.TaskID), Model: e.opts.Model,
			InheritUserSettings: e.opts.InheritUserSettings, State: string(hooks.Starting), StartedAt: started,
			Sandbox: mode,
		}); err != nil {
			return err
		}
		if err := q.CreateWorktree(ctx, db.CreateWorktreeParams{AgentID: id, ProjectID: e.project.ID, Path: wtPath,
			Branch: branch, BaseCommit: base, CreatedAt: started, Kind: kind}); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sid, AgentID: id, StartedAt: started}); err != nil {
			return err
		}
		if task != nil && task.Status != StatusInProgress {
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
	if e.opts.Sandbox != nil {
		a, err = e.launchSandboxed(id, sid, branch, wtPath, base, req.TaskID, started, task, prep)
	} else {
		a, err = e.launch(id, sid, branch, wtPath, base, req.TaskID, started, task)
	}
	if err != nil {
		undoWorktree()
		_ = e.q.MarkAgentExited(context.Background(), db.MarkAgentExitedParams{ExitedAt: nullTime(now()),
			ExitReason: ExitStartFailed, ID: id})
		_ = e.q.SetWorktreeStatus(context.Background(), db.SetWorktreeStatusParams{Status: WorktreeRemoved, RemovedAt: nullTime(now()), AgentID: id})
		_ = e.q.MarkAgentRemoved(context.Background(), db.MarkAgentRemovedParams{RemovedAt: nullTime(now()), ID: id})
		return AgentInfo{}, err
	}
	if task != nil {
		tv := taskFromRow(*task)
		e.publish(Update{Type: "task", Task: &tv})
	}
	return a, nil
}

// launch writes the agent's settings, opens its hook socket and starts the CLI.
func (e *Engine) launch(id, sid, branch, wtPath, base, taskID string, started time.Time, task *db.Task) (AgentInfo, error) {
	dir := filepath.Join(e.opts.RuntimeDir, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AgentInfo{}, err
	}
	settingsPath := filepath.Join(dir, id+".settings.json")
	settings, err := agent.HookSettings(e.opts.HookCommand)
	if err != nil {
		return AgentInfo{}, err
	}
	if err := os.WriteFile(settingsPath, settings, 0o600); err != nil {
		return AgentInfo{}, fmt.Errorf("write settings: %w", err)
	}
	ln, err := hooks.Listen(filepath.Join(dir, id+".sock"), e.hookHandler(id), e.log.With("agent_id", id))
	if err != nil {
		os.Remove(settingsPath)
		return AgentInfo{}, err
	}
	prompt := ""
	if task != nil {
		prompt = taskPrompt(task.Title, task.Description, branch)
	}
	cmd, err := e.adapter.Command(agent.LaunchSpec{
		SessionID: sid, Model: e.opts.Model, WorkDir: wtPath, SettingsPath: settingsPath,
		InheritUserSettings: e.opts.InheritUserSettings, SocketPath: ln.Path(), BaseEnv: e.opts.BaseEnv,
		InitialPrompt: prompt,
	})
	var term *terminal.Session
	if err == nil {
		term, err = terminal.Start(cmd, terminal.Options{Size: e.opts.TermSize})
	}
	if err != nil {
		ln.Close()
		os.Remove(settingsPath)
		return AgentInfo{}, fmt.Errorf("start agent: %w", err)
	}

	a := &agentProc{
		info: AgentInfo{ID: id, SessionID: sid, TaskID: taskID, Model: e.opts.Model, Sandbox: SandboxHost,
			InheritUserSettings: e.opts.InheritUserSettings, Branch: branch, WorktreePath: wtPath,
			WorkspaceKind: KindWorktree, WorktreeStatus: WorktreeActive, HasTerminal: true, StartedAt: started},
		status:     hooks.Status{State: hooks.Starting},
		baseCommit: base,
		term:       term,
		listener:   ln,
		settings:   settingsPath,
		exited:     make(chan struct{}),
	}
	e.mu.Lock()
	e.agents[id] = a
	e.order = append(e.order, id)
	e.publishAgentLocked(a)
	e.mu.Unlock()
	e.log.Info("agent started", "agent_id", id, "session_id", sid, "pid", term.Pid(), "task_id", taskID,
		"branch", branch, "worktree", wtPath, "model", e.opts.Model, "inherit_user_settings", e.opts.InheritUserSettings)
	e.wg.Add(1)
	go e.watch(a)
	return a.view(), nil
}

// watch marks the agent exited when its process ends, whether or not a
// SessionEnd hook arrived, and releases its socket and settings file.
func (e *Engine) watch(a *agentProc) {
	defer e.wg.Done()
	<-a.term.Done()
	code := 0
	var exitErr *exec.ExitError
	if err := a.term.ExitErr(); errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	_ = a.listener.Close()
	if a.sandbox != nil {
		e.closeSandbox(a.sandbox, e.log.With("agent_id", a.info.ID))
	} else {
		_ = os.Remove(a.settings)
	}

	e.mu.Lock()
	t := now()
	reason := a.exitReason
	if reason == "" {
		reason = ExitProcess
	}
	a.status = hooks.Status{State: hooks.Exited}
	a.info.ExitedAt, a.info.ExitCode, a.info.ExitReason = &t, &code, reason
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.q.MarkAgentExited(ctx, db.MarkAgentExitedParams{ExitedAt: nullTime(t),
		ExitCode: sql.NullInt64{Int64: int64(code), Valid: true}, ExitReason: reason, ID: a.info.ID}); err != nil {
		e.log.Error("persist agent exit", "agent_id", a.info.ID, "err", err)
	}
	_ = e.q.EndSessions(ctx, db.EndSessionsParams{EndedAt: nullTime(t), AgentID: a.info.ID})
	e.publishAgentLocked(a)
	e.mu.Unlock()
	close(a.exited)
	e.log.Info("agent exited", "agent_id", a.info.ID, "exit_code", code, "reason", reason)
}

func (e *Engine) hookHandler(id string) hooks.Handler {
	log := e.log.With("agent_id", id)
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		ev, err := agent.ParseClaudeHook(payload)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		a, ok := e.agents[id]
		if !ok {
			return nil, ErrNotFound
		}
		if ev.SessionID != a.info.SessionID {
			return nil, ErrSession
		}
		e.applyLocked(ctx, a, ev, log)
		return nil, nil // Phase 4: Stop-hook message injection goes here
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
			e.log.With("agent_id", id))
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

	res, err := e.cleanWorktree(ctx, info, base, force)
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
	e.mu.Unlock()
	e.log.Info("agent removed", "agent_id", id, "worktree", res.Worktree, "branch", res.Branch, "discarded", force)
	return res, nil
}

func (e *Engine) cleanWorktree(ctx context.Context, info AgentInfo, base string, force bool) (RemoveResult, error) {
	repo := e.project.RepoPath
	res := RemoveResult{Worktree: "removed", Branch: "kept"}
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
	for ch := range e.subs {
		select {
		case ch <- u:
		default:
			delete(e.subs, ch)
			close(ch)
		}
	}
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
func (e *Engine) Shutdown() {
	e.mu.Lock()
	ids := slices.Clone(e.order)
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.stop(id, ExitShutdown) }()
	}
	wg.Wait()
	e.wg.Wait()
}

func (a *agentProc) view() AgentInfo {
	v := a.info
	v.State, v.Activity = string(a.status.State), a.status.Activity
	v.HasTerminal = a.term != nil
	return v
}

func infoFromRow(r db.ListVisibleAgentsRow) AgentInfo {
	info := AgentInfo{
		ID: r.ID, TaskID: r.TaskID.String, State: r.State, Model: r.Model,
		InheritUserSettings: r.InheritUserSettings, Sandbox: r.Sandbox, Branch: r.Branch, WorktreePath: r.WorktreePath,
		WorkspaceKind: r.WorktreeKind, WorktreeStatus: r.WorktreeStatus, StartedAt: r.StartedAt, ExitReason: r.ExitReason,
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
