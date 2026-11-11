package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/secret"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
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
	AllowNames    []string          // the same entries, for the UI
	Memory        string            // per-worker memory limit; empty: sandbox default
	Pids          int               // per-worker pids limit; 0: sandbox default
	Pack          *pack.Pack        // stack pack (ADR 0023); nil: none. Image and Allow already reflect it.
	Codex         *CodexOptions     // Codex agents (ADR 0049); nil: not set up, Codex roles are refused
	SecretsDir    string            // where the pack's secret values live (pack.DefaultSecretsDir)

	apiUpstream *url.URL // tests only: the API proxy's upstream; nil: the Anthropic API
}

// CodexOptions is the daemon's Codex installation and login (ADR 0049),
// shared by every project.
type CodexOptions struct {
	Dir     string                             // host dir with the static codex and codex-code-mode-host binaries
	Version string                             // e.g. "0.167.1"
	Login   func() (sandbox.CodexLogin, error) // the user's ChatGPT login, read at every request
	CA      *sandbox.LocalCA                   // the loopback TLS leg's CA

	upstream *url.URL // tests only: the ChatGPT backend; nil: sandbox.ChatGPTBackend
}

// codexFiles are a Codex agent's daemon-written files (ADR 0049).
type codexFiles struct {
	dir, config, login, ca string // host paths, mounted read-only
	bearer                 string // the placeholder login's bearer, which the API socket expects
}

// writeCodexFiles writes a Codex agent's config.toml, its placeholder login
// and the loopback CA into runDir (daemon-owned, mounted read-only), and the
// mount points into its config dir. The user's login is read for its account
// id and plan only; its token stays on the host.
func (e *Engine) writeCodexFiles(runDir, cfgDir string, fresh bool) (codexFiles, error) {
	s := e.opts.Sandbox
	login, err := s.Codex.Login()
	if err != nil {
		return codexFiles{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	placeholder, bearer, err := agent.CodexPlaceholder(login.Account, login.Plan)
	if err != nil {
		return codexFiles{}, err
	}
	cfg, err := agent.CodexConfig(agent.CodexConfigSpec{BackendOrigin: sandbox.CodexOrigin, HookCommand: sandbox.AgentOfficeBin + " hook",
		MCPCommand: sandbox.AgentOfficeBin, MCPSocket: sandbox.MCPSocket, Workspace: sandbox.WorkspaceDir, Extra: packServers(s.Pack)})
	if err != nil {
		return codexFiles{}, err
	}
	cx := codexFiles{dir: s.Codex.Dir, config: filepath.Join(runDir, "codex-config.toml"),
		login: filepath.Join(runDir, "codex-login.json"), ca: filepath.Join(runDir, "loopback-ca.pem"), bearer: bearer}
	for p, b := range map[string][]byte{cx.config: cfg, cx.login: placeholder, cx.ca: s.Codex.CA.CertPEM()} {
		if err := os.WriteFile(p, b, 0o644); err != nil { // read by the container user; nothing secret; dir is 0700
			return codexFiles{}, err
		}
	}
	if fresh { // mount points, while the config dir still belongs to the user
		for _, name := range []string{"config.toml", "auth.json"} {
			if err := os.WriteFile(filepath.Join(cfgDir, name), nil, 0o644); err != nil {
				return codexFiles{}, err
			}
		}
	}
	return cx, nil
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
	secrets   net.Listener    // one-shot pack secrets socket (ADR 0023); nil without secrets
	grants    *sandbox.Grants // hosts approved at run time (ADR 0019)
	memory    string          // the worker's memory limit, e.g. "2g" (ADR 0051)
	oomKills  atomic.Int64    // the worker cgroup's oom_kill count when the running watch last read it
	oomTold   atomic.Int64    // OOM kills the watch told the agent about: processes it ran, not itself
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
	if o.Pack != nil && len(o.Pack.Env) > 0 && !filepath.IsAbs(o.SecretsDir) {
		return fmt.Errorf("engine: Sandbox.SecretsDir must be an absolute path, got %q", o.SecretsDir)
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
	secrets map[string]secret.Secret // the pack's secrets for this agent (ADR 0023)
}

// prepareSandboxStart checks everything a sandboxed start needs before any
// state is created: the repository's object store, the user's git identity,
// the OAuth token file and, for workers and reviewers, the pack's secrets.
func (e *Engine) prepareSandboxStart(ctx context.Context, agentName, role string) (sandboxPrep, error) {
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
	var secrets map[string]secret.Secret
	// Workers only: leads plan and run no code, and reviewers read the agent-written
	// diff, the most injectable input there is (ADR 0023).
	if p := e.opts.Sandbox.Pack; p != nil && role == KindWorker {
		var skipped []string
		if secrets, skipped, err = p.ReadSecrets(e.opts.Sandbox.SecretsDir); err != nil {
			return sandboxPrep{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if len(skipped) > 0 {
			e.log.Warn("optional pack secrets not set", "pack", p.Name, "names", skipped)
		}
	}
	return sandboxPrep{objects: objects, token: token, secrets: secrets,
		ident: sandbox.GitIdentity{Name: name, Email: email, CoAuthor: sandbox.CoAuthor(agentName)}}, nil
}

// createClone sets up the agent repository (ADR 0014) and returns an undo.
func (e *Engine) createClone(ctx context.Context, id, path, branch, base string, extra map[string]string) (func(), error) {
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
	if err := e.git.CreateSharedCloneWith(ctx, e.project.RepoPath, path, branch, base, extra); err != nil {
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
func (e *Engine) launchSandboxed(l launchInfo, prep sandboxPrep) (AgentInfo, error) {
	id, sid, branch, wsPath, base, taskID, started := l.id, l.sid, l.branch, l.path, l.base, l.taskID, l.started
	s := e.opts.Sandbox
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log := e.sessionLogger(l)
	su := e.newSessionUsage(l, SandboxPodman)

	res := &sandboxRes{dir: filepath.Join(e.opts.RuntimeDir, "agents", id), container: sandbox.ContainerName(id),
		grants: &sandbox.Grants{}, memory: e.workerMemory()}
	cfgDir := filepath.Join(s.AgentsRoot, id, "config")
	var ln *hooks.Listener
	var mln *mcp.Listener
	fail := func(err error) (AgentInfo, error) {
		if ln != nil {
			ln.Close()
		}
		if mln != nil {
			mln.Close()
		}
		e.closeSandbox(res, log)
		return AgentInfo{}, err
	}
	if err := os.MkdirAll(res.dir, 0o700); err != nil {
		return fail(err)
	}
	if err := e.loadGrants(ctx, id, res.grants); err != nil { // approvals of earlier sessions (ADR 0053)
		return fail(err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fail(err)
	}
	codex := l.cli == role.CLICodex
	var settingsPath string
	var cx codexFiles
	if codex {
		var err error
		if cx, err = e.writeCodexFiles(res.dir, cfgDir, l.prev == nil); err != nil {
			return fail(err)
		}
	} else {
		settings, err := agent.SandboxSettings(sandbox.AgentOfficeBin+" hook", l.def.DenyRules())
		if err != nil {
			return fail(err)
		}
		settingsPath = filepath.Join(res.dir, "settings.json")
		if err := os.WriteFile(settingsPath, settings, 0o644); err != nil { // read by the container user; dir is 0700
			return fail(err)
		}
		seed, err := agent.SeedConfig(s.ClaudeVersion, sandbox.WorkspaceDir)
		if err != nil {
			return fail(err)
		}
		// A relaunch (ADR 0022) keeps the config the agent's first session left; it
		// belongs to the sandbox user by now.
		if _, err := os.Lstat(filepath.Join(cfgDir, ".claude.json")); l.prev == nil || err != nil {
			if err := os.WriteFile(filepath.Join(cfgDir, ".claude.json"), seed, 0o644); err != nil {
				return fail(err)
			}
		}
	}
	// The pack's skills become the agent's personal skills (ADR 0023): written
	// fresh for every launch into the daemon-owned runtime dir and mounted
	// read-only over CLAUDE_CONFIG_DIR/skills, so the agent cannot change them
	// and a relaunch gets the current pack's. The mount point is created
	// while the config dir still belongs to the user.
	if l.prev == nil {
		if err := os.MkdirAll(filepath.Join(cfgDir, "skills"), 0o755); err != nil {
			return fail(err)
		}
	}
	var skillsPath string
	var err error
	if p := s.Pack; p != nil && len(p.Skills) > 0 {
		skillsPath = filepath.Join(res.dir, "skills")
		if err := p.WriteSkills(skillsPath); err != nil {
			return fail(err)
		}
	}

	if ln, err = hooks.Listen(filepath.Join(res.dir, "hook.sock"), e.hookHandler(id, log), log); err != nil {
		return fail(err)
	}
	mcpPath, mcpConfig := filepath.Join(res.dir, "mcp.sock"), ""
	if !codex { // Codex's MCP servers are in its config.toml
		mcpConfig = filepath.Join(res.dir, "mcp.json")
		cfg, err := agent.MCPConfig(sandbox.AgentOfficeBin, sandbox.MCPSocket, packServers(s.Pack))
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(mcpConfig, cfg, 0o644); err != nil { // read by the container user; dir is 0700
			return fail(err)
		}
	}
	if mln, err = e.listenMCP(mcpPath, id, l, log); err != nil {
		return fail(err)
	}
	onEvent := func(ev sandbox.Event) { e.sandboxEvent(id, ev) }
	egressPath, apiPath := filepath.Join(res.dir, "egress.sock"), filepath.Join(res.dir, "api.sock")
	if res.egress, err = listenUnix(egressPath); err != nil {
		return fail(err)
	}
	go func() {
		p := &sandbox.EgressProxy{Allow: s.Allow, Grants: res.grants, OnEvent: onEvent, Log: log.With("proxy", "egress")}
		if err := p.Serve(res.egress); err != nil {
			log.Warn("egress proxy stopped", "err", err)
		}
	}()
	apiLn, err := listenUnix(apiPath)
	if err != nil {
		return fail(err)
	}
	apiLog := log.With("proxy", "api")
	var handler http.Handler
	if codex {
		// The ChatGPT backend with the user's login, over TLS on the loopback leg (ADR 0049).
		cp := &sandbox.CodexProxy{Login: s.Codex.Login, Placeholder: cx.bearer, Upstream: s.Codex.upstream, OnEvent: onEvent,
			Log: apiLog, OnUsage: func(m usage.Message) { e.meterUsage(su, m, apiLog) },
			Allow: func() error { return e.allowRequest(su) }}
		handler, apiLn = cp.Handler(), s.Codex.CA.Listener(apiLn)
	} else {
		api := &sandbox.APIProxy{Token: prep.token, Upstream: s.apiUpstream, OnEvent: onEvent, Log: apiLog,
			OnUsage: func(m usage.Message) { e.meterUsage(su, m, apiLog) },
			Allow:   func() error { return e.allowRequest(su) }}
		handler = api.Handler()
	}
	res.api = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = res.api.Serve(apiLn) }()

	exitReport := filepath.Join(res.dir, exitReportName) // sandbox-init's report (ADR 0051)
	if err := os.WriteFile(exitReport, nil, 0o644); err != nil {
		return fail(err)
	}
	socks := []string{ln.Path(), mcpPath, egressPath, apiPath, exitReport}
	var secretsPath string
	if len(prep.secrets) > 0 {
		secretsPath = filepath.Join(res.dir, "secrets.sock")
		if res.secrets, err = listenUnix(secretsPath); err != nil {
			return fail(err)
		}
		socks = append(socks, secretsPath)
		go func(l net.Listener, vals map[string]secret.Secret) {
			if err := sandbox.ServeSecretsOnce(l, vals); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Warn("secrets socket", "err", err)
			}
		}(res.secrets, prep.secrets)
	}
	if err := s.Podman.Chown(ctx, e.opts.RuntimeDir, socks...); err != nil {
		return fail(err)
	}
	if err := s.Podman.Chown(ctx, s.AgentsRoot, filepath.Join(s.AgentsRoot, id)); err != nil {
		return fail(err)
	}

	var command []string
	if codex {
		command, err = agent.CodexContainerArgs(agent.CodexContainerSpec{Bin: sandbox.CodexBin, Model: e.modelOf(l.def),
			InitialPrompt: l.prompt, ResumeFrom: l.cliResume})
	} else {
		command, err = agent.ContainerArgs(agent.ContainerSpec{Bin: sandbox.ClaudeBin, SessionID: sid,
			Model: e.modelOf(l.def), SettingsPath: sandbox.SettingsFile, MCPConfigPath: sandbox.MCPConfigFile, InitialPrompt: l.prompt,
			ResumeFrom: l.resumeFrom})
	}
	if err != nil {
		return fail(err)
	}
	args, err := sandbox.WorkerArgs(sandbox.WorkerSpec{
		Instance: s.Instance, AgentID: id, Image: s.Image, Workspace: wsPath, ObjectsDir: prep.objects, ConfigDir: cfgDir,
		HookSocket: ln.Path(), MCPSocket: mcpPath, MCPConfig: mcpConfig, EgressSocket: egressPath, APISocket: apiPath,
		SecretsSocket: secretsPath, ExitReport: exitReport, Skills: skillsPath, AgentOffice: s.AgentOffice, Claude: s.Claude, Settings: settingsPath, GitHooks: s.GitHooks,
		Git: prep.ident, Memory: res.memory, Pids: s.Pids, Command: command, ReadOnlyWorkspace: l.def.ReadOnly,
		CLI: cliOr(l.cli), Codex: cx.dir, CodexConfig: cx.config, CodexAuth: cx.login, CodexCA: cx.ca,
	})
	if err != nil {
		return fail(err)
	}
	term, err := terminal.Start(s.Podman.Command(args), terminal.Options{Size: e.opts.TermSize})
	if err != nil {
		return fail(fmt.Errorf("start worker: %w", err))
	}

	a := &agentProc{
		info: AgentInfo{ID: id, SessionID: sid, TaskID: taskID, Model: e.modelOf(l.def), Sandbox: SandboxPodman, Kind: l.role,
			Role:   l.def.Name,
			Branch: branch, WorktreePath: wsPath, WorkspaceKind: KindClone, WorktreeStatus: WorktreeActive,
			HasTerminal: true, StartedAt: started},
		status:      hooks.Status{State: hooks.Starting},
		baseCommit:  base,
		term:        term,
		listener:    ln,
		settings:    settingsPath,
		sandbox:     res,
		exited:      make(chan struct{}),
		afterExit:   make(chan struct{}),
		historyDone: make(chan struct{}),
		mcp:         mln,
		hasPrompt:   l.prompt != "",
		usage:       su,
		log:         log,
	}
	e.mu.Lock()
	e.registerLocked(a, l)
	view := a.view() // under the lock: the agent's first hook may already be changing its state
	e.mu.Unlock()
	// A decision committed after the first load reached only running agents:
	// load again now that this one is registered (ADR 0053).
	if err := e.loadGrants(ctx, id, res.grants); err != nil {
		log.Warn("reload access grants", "err", err)
	}
	log.Info("agent started", "role", l.role, "relaunch", l.prev != nil, "sandbox", SandboxPodman, "branch", branch,
		"workspace", wsPath, "container", res.container, "image", s.Image, "pack", packName(s.Pack),
		"secrets", secretNames(prep.secrets), "model", e.modelOf(l.def), "role_name", l.def.Name, "read_only", l.def.ReadOnly)
	e.keepHistory(a)
	if codex && l.prompt == "" {
		e.codexReadyWithoutPrompt(a, log)
	}
	e.wg.Add(2)
	go e.watch(a)
	go e.watchMemory(a, res)
	return view, nil
}

// workerMemory is a worker's memory limit (ADR 0051).
func (e *Engine) workerMemory() string {
	if m := e.opts.Sandbox.Memory; m != "" {
		return m
	}
	return sandbox.DefaultMemory
}

// codexSettle is how long a Codex TUI takes to come up (Spike G: about a
// second); tests shorten it.
var codexSettle = 3 * time.Second

// codexReadyWithoutPrompt marks a Codex agent idle that started without a
// prompt (a restore or Resume of an idle conversation, a worker without a
// task): Codex sends SessionStart only with the first prompt (checked with
// 0.167.1, fresh and resumed), so no hook would end "starting". Its session
// id is learned from that first prompt's SessionStart (ADR 0049).
func (e *Engine) codexReadyWithoutPrompt(a *agentProc, log *slog.Logger) {
	e.wg.Add(1)
	time.AfterFunc(codexSettle, func() {
		defer e.wg.Done()
		e.mu.Lock()
		defer e.mu.Unlock()
		if !a.running() || a.status.State != hooks.Starting {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Up for codexSettle with its conversation: not a failed resume (ADR 0035).
		a.sessionStarted = true
		e.applyLocked(ctx, a, agent.Event{Kind: agent.Idle, SessionID: a.info.SessionID, Hook: "codex_ready"}, log)
		e.scheduleDeliveryLocked(ctx, a) // what was queued for it is typed in once it settles
	})
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
	if res.secrets != nil {
		res.secrets.Close()
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

// MaxSandboxEventsListed bounds SandboxEvents.
const MaxSandboxEventsListed = 100

// SandboxEvents returns agent id's most recent recorded sandbox events
// (proxy refusals and import problems), newest first, at most limit (1 to
// MaxSandboxEventsListed). Host agents have none.
func (e *Engine) SandboxEvents(ctx context.Context, id string, limit int) ([]sandbox.Event, error) {
	if limit < 1 || limit > MaxSandboxEventsListed {
		return nil, fmt.Errorf("%w: limit must be 1 to %d", ErrInvalid, MaxSandboxEventsListed)
	}
	e.mu.Lock()
	_, ok := e.agents[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := e.q.ListSandboxEvents(ctx, db.ListSandboxEventsParams{AgentID: id, Limit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("engine: list sandbox events: %w", err)
	}
	out := make([]sandbox.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, sandbox.Event{Kind: sandbox.EventKind(r.Kind), Detail: r.Detail, At: r.At.UTC()})
	}
	return out, nil
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

// packServers converts a pack's MCP servers for the agent's MCP config.
func packServers(p *pack.Pack) map[string]agent.MCPServer {
	if p == nil {
		return nil
	}
	out := make(map[string]agent.MCPServer, len(p.MCPServers))
	for n, s := range p.MCPServers {
		out[n] = agent.MCPServer{Command: s.Command, Args: s.Args, URL: s.URL}
	}
	return out
}

func packName(p *pack.Pack) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// secretNames lists the names (never the values) of an agent's secrets.
func secretNames(m map[string]secret.Secret) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
