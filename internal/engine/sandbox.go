package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
)

// SandboxOptions turns on sandboxed workers (ADR 0007, 0014–0016). With nil
// Options.Sandbox the engine runs agents on the host with permission prompts.
type SandboxOptions struct {
	Instance      string // daemon instance (a data dir hash); labels its containers
	Podman        sandbox.Podman
	Image         string            // worker image (sandbox.WorkerImage)
	WorkspaceRoot string            // agent repositories; untrusted (ADR 0014)
	AgentsRoot    string            // per-agent Claude Code config dirs
	GitHooks      string            // written by sandbox.WriteGitHooks
	AgentOffice   string            // static agent-office binary, mounted read-only
	Claude        string            // the resolved, versioned claude binary
	ClaudeVersion string            // e.g. "2.1.319", for the seeded config
	TokenFile     string            // OAuth token file (ADR 0015), read at every launch
	Allow         sandbox.Allowlist // egress allowlist
	Memory        string            // per-worker memory limit; empty: sandbox default
	Pids          int               // per-worker pids limit; 0: sandbox default
}

// workspacesReadme is put into the workspaces root (ADR 0014).
const workspacesReadme = `Do not cd into or open these folders.

Each folder is the git repository of one sandboxed agent-office agent. The agent
controls everything in it, including git config that runs commands for whoever runs
git there. The folders belong to a subordinate user id, so git refuses to work in
them; do not mark them as safe, and do not open them in an editor or file manager.

To look at an agent's work, use its branch (agent-office/...) in your own repository.
agent-office imports it there when the agent exits.
`

// maxSandboxEvents bounds the proxy events recorded per agent and minute, so
// a misbehaving agent cannot flood the database or the UI.
const maxSandboxEvents = 60

// SandboxEvent is a proxy refusal or failure for one agent, published as an
// Update of type "sandbox_event".
type SandboxEvent struct {
	AgentID string        `json:"agent_id"`
	Event   sandbox.Event `json:"event"`
}

// sandboxRes holds the per-agent sandbox resources released on exit.
type sandboxRes struct {
	dir       string // runtime dir with sockets and settings
	container string
	egress    net.Listener
	api       *http.Server
}

// eventLimit is the per-agent rate limit for sandbox events.
type eventLimit struct {
	windowAt time.Time
	n        int
	dropped  int
}

func (o *SandboxOptions) validate() error {
	for name, p := range map[string]string{"WorkspaceRoot": o.WorkspaceRoot, "AgentsRoot": o.AgentsRoot,
		"GitHooks": o.GitHooks, "AgentOffice": o.AgentOffice, "Claude": o.Claude, "TokenFile": o.TokenFile} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("engine: Sandbox.%s must be an absolute path, got %q", name, p)
		}
	}
	if o.Image == "" || o.ClaudeVersion == "" || !sandbox.InstancePattern.MatchString(o.Instance) {
		return errors.New("engine: Sandbox.Image, Sandbox.ClaudeVersion and a valid Sandbox.Instance are required")
	}
	return nil
}

// prepareSandbox creates the sandbox roots and the warning README, and makes
// the git wrapper refuse agent repositories.
func (e *Engine) prepareSandbox() error {
	s := e.opts.Sandbox
	for _, d := range []string{s.WorkspaceRoot, s.AgentsRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("engine: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.WorkspaceRoot, "README.txt"), []byte(workspacesReadme), 0o644); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	e.git.UntrustedRoots = append(e.git.UntrustedRoots, s.WorkspaceRoot)
	return nil
}

// sandboxPrep is what Start gathers before recording a sandboxed agent.
type sandboxPrep struct {
	objects string
	ident   sandbox.GitIdentity
	token   sandbox.Secret
}

// prepareSandboxStart checks everything a sandboxed start needs before any
// state is created: the repository's object store, the user's git identity
// and the OAuth token file.
func (e *Engine) prepareSandboxStart(ctx context.Context, agentName string) (sandboxPrep, error) {
	objects, err := e.git.ObjectsDir(ctx, e.project.RepoPath)
	if err != nil {
		return sandboxPrep{}, err
	}
	name, email, err := e.git.UserIdentity(ctx, e.project.RepoPath)
	if err != nil {
		return sandboxPrep{}, fmt.Errorf("%w: sandboxed agents commit as you: %v", ErrInvalid, err)
	}
	token, err := sandbox.ReadTokenFile(e.opts.Sandbox.TokenFile)
	if err != nil {
		return sandboxPrep{}, err
	}
	return sandboxPrep{objects: objects, token: token,
		ident: sandbox.GitIdentity{Name: name, Email: email, CoAuthor: sandbox.CoAuthor(agentName)}}, nil
}

// createClone sets up the agent repository (ADR 0014) and returns an undo.
func (e *Engine) createClone(ctx context.Context, id, path, branch, base string) (func(), error) {
	s := e.opts.Sandbox
	undo := func() {
		cctx := context.Background()
		if err := s.Podman.RemoveAll(cctx, s.WorkspaceRoot, path); err != nil {
			e.log.Warn("cleanup workspace", "path", path, "err", err)
		}
		if err := s.Podman.RemoveAll(cctx, s.AgentsRoot, filepath.Join(s.AgentsRoot, id)); err != nil {
			e.log.Warn("cleanup agent config", "agent_id", id, "err", err)
		}
		_ = e.git.UnprotectBase(cctx, e.project.RepoPath, id)
	}
	if err := e.git.CreateSharedClone(ctx, e.project.RepoPath, path, branch, base); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	if err := e.git.ProtectBase(ctx, e.project.RepoPath, id, base); err != nil {
		undo()
		return nil, err
	}
	if err := s.Podman.Chown(ctx, s.WorkspaceRoot, path); err != nil {
		undo()
		return nil, err
	}
	return undo, nil
}

// launchSandboxed starts the per-agent sockets and proxies and the worker
// container in a PTY.
func (e *Engine) launchSandboxed(id, sid, branch, wsPath, base, taskID string, started time.Time,
	task *db.Task, prep sandboxPrep) (AgentInfo, error) {
	s := e.opts.Sandbox
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log := e.log.With("agent_id", id)

	res := &sandboxRes{dir: filepath.Join(e.opts.RuntimeDir, "agents", id), container: sandbox.ContainerName(id)}
	cfgDir := filepath.Join(s.AgentsRoot, id, "config")
	var ln *hooks.Listener
	fail := func(err error) (AgentInfo, error) {
		if ln != nil {
			ln.Close()
		}
		e.closeSandbox(res, log)
		return AgentInfo{}, err
	}
	if err := os.MkdirAll(res.dir, 0o700); err != nil {
		return fail(err)
	}
	settings, err := agent.SandboxSettings(sandbox.AgentOfficeBin + " hook")
	if err != nil {
		return fail(err)
	}
	settingsPath := filepath.Join(res.dir, "settings.json")
	if err := os.WriteFile(settingsPath, settings, 0o644); err != nil { // read by the container user; dir is 0700
		return fail(err)
	}
	seed, err := agent.SeedConfig(s.ClaudeVersion, sandbox.WorkspaceDir)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, ".claude.json"), seed, 0o644); err != nil {
		return fail(err)
	}

	if ln, err = hooks.Listen(filepath.Join(res.dir, "hook.sock"), e.hookHandler(id), log); err != nil {
		return fail(err)
	}
	onEvent := func(ev sandbox.Event) { e.sandboxEvent(id, ev) }
	egressPath, apiPath := filepath.Join(res.dir, "egress.sock"), filepath.Join(res.dir, "api.sock")
	if res.egress, err = listenUnix(egressPath); err != nil {
		return fail(err)
	}
	go func() {
		p := &sandbox.EgressProxy{Allow: s.Allow, OnEvent: onEvent, Log: log.With("proxy", "egress")}
		if err := p.Serve(res.egress); err != nil {
			log.Warn("egress proxy stopped", "err", err)
		}
	}()
	apiLn, err := listenUnix(apiPath)
	if err != nil {
		return fail(err)
	}
	api := &sandbox.APIProxy{Token: prep.token, OnEvent: onEvent, Log: log.With("proxy", "api")}
	res.api = &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = res.api.Serve(apiLn) }()

	if err := s.Podman.Chown(ctx, e.opts.RuntimeDir, ln.Path(), egressPath, apiPath); err != nil {
		return fail(err)
	}
	if err := s.Podman.Chown(ctx, s.AgentsRoot, filepath.Join(s.AgentsRoot, id)); err != nil {
		return fail(err)
	}

	prompt := ""
	if task != nil {
		prompt = taskPrompt(task.Title, task.Description, branch)
	}
	command, err := agent.ContainerArgs(agent.ContainerSpec{Bin: sandbox.ClaudeBin, SessionID: sid,
		Model: e.opts.Model, SettingsPath: sandbox.SettingsFile, InitialPrompt: prompt})
	if err != nil {
		return fail(err)
	}
	args, err := sandbox.WorkerArgs(sandbox.WorkerSpec{
		Instance: s.Instance, AgentID: id, Image: s.Image, Workspace: wsPath, ObjectsDir: prep.objects, ConfigDir: cfgDir,
		HookSocket: ln.Path(), EgressSocket: egressPath, APISocket: apiPath,
		AgentOffice: s.AgentOffice, Claude: s.Claude, Settings: settingsPath, GitHooks: s.GitHooks,
		Git: prep.ident, Memory: s.Memory, Pids: s.Pids, Command: command,
	})
	if err != nil {
		return fail(err)
	}
	term, err := terminal.Start(s.Podman.Command(args), terminal.Options{Size: e.opts.TermSize})
	if err != nil {
		return fail(fmt.Errorf("start worker: %w", err))
	}

	a := &agentProc{
		info: AgentInfo{ID: id, SessionID: sid, TaskID: taskID, Model: e.opts.Model, Sandbox: SandboxPodman,
			Branch: branch, WorktreePath: wsPath, WorkspaceKind: KindClone, WorktreeStatus: WorktreeActive,
			HasTerminal: true, StartedAt: started},
		status:     hooks.Status{State: hooks.Starting},
		baseCommit: base,
		term:       term,
		listener:   ln,
		settings:   settingsPath,
		sandbox:    res,
		exited:     make(chan struct{}),
	}
	e.mu.Lock()
	e.agents[id] = a
	e.order = append(e.order, id)
	e.publishAgentLocked(a)
	e.mu.Unlock()
	log.Info("agent started", "sandbox", SandboxPodman, "session_id", sid, "task_id", taskID, "branch", branch,
		"workspace", wsPath, "container", res.container, "image", s.Image, "model", e.opts.Model)
	e.wg.Add(1)
	go e.watch(a)
	return a.view(), nil
}

// closeSandbox stops the proxies, removes the container if it is still there
// (killing the podman client does not stop it) and deletes the runtime dir.
func (e *Engine) closeSandbox(res *sandboxRes, log interface{ Warn(string, ...any) }) {
	if res.egress != nil {
		res.egress.Close()
	}
	if res.api != nil {
		res.api.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.opts.Sandbox.Podman.Remove(ctx, res.container); err != nil {
		log.Warn("remove worker container", "container", res.container, "err", err)
	}
	if err := os.RemoveAll(res.dir); err != nil {
		log.Warn("remove runtime dir", "dir", res.dir, "err", err)
	}
}

// sandboxEvent records and publishes a proxy refusal for agent id, at most
// maxSandboxEvents per minute.
func (e *Engine) sandboxEvent(id string, ev sandbox.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok || a.info.Sandbox != SandboxPodman {
		return
	}
	r := &a.events
	if time.Since(r.windowAt) > time.Minute {
		if r.dropped > 0 {
			e.log.Warn("sandbox events dropped", "agent_id", id, "count", r.dropped)
		}
		r.windowAt, r.n, r.dropped = time.Now(), 0, 0
	}
	if r.n >= maxSandboxEvents {
		r.dropped++
		return
	}
	r.n++
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.q.InsertEvent(ctx, db.InsertEventParams{AgentID: id, SessionID: a.info.SessionID,
		Kind: string(ev.Kind), Hook: "sandbox", Detail: ev.Detail, State: string(a.status.State), At: ev.At}); err != nil {
		e.log.Warn("persist sandbox event", "agent_id", id, "err", err)
	}
	e.publishLocked(Update{Type: "sandbox_event", SandboxEvent: &SandboxEvent{AgentID: id, Event: ev}})
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
