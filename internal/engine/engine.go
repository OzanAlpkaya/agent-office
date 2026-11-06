package engine

import (
	"context"
	"crypto/rand"
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
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
)

// Errors returned by the engine.
var (
	ErrNotFound     = errors.New("agent not found")
	ErrAgentRunning = errors.New("an agent is already running (Phase 1 supports one at a time)")
	ErrStillRunning = errors.New("agent is still running; stop it first")
	ErrSession      = errors.New("hook session_id does not match the agent's session")
)

// Options configures the engine. All paths must be absolute.
type Options struct {
	Repo                string        // working directory for agents
	Model               string        // optional default model
	InheritUserSettings bool          // host mode: load the user's own Claude Code settings
	ClaudeBin           string        // claude binary
	HookCommand         string        // shell command Claude Code runs for hooks, e.g. "'/usr/bin/agent-office' hook"
	RuntimeDir          string        // private dir for sockets and settings files
	BaseEnv             []string      // environment the agent env is derived from
	TermSize            terminal.Size // initial terminal size
	StopTimeout         time.Duration // SIGTERM -> SIGKILL grace period
	Logger              *slog.Logger
}

// AgentInfo is the public, JSON-serializable view of an agent.
type AgentInfo struct {
	ID                  string     `json:"id"`
	SessionID           string     `json:"session_id"`
	State               string     `json:"state"`
	Activity            string     `json:"activity,omitempty"`
	Repo                string     `json:"repo"`
	Model               string     `json:"model,omitempty"`
	InheritUserSettings bool       `json:"inherit_user_settings"`
	StartedAt           time.Time  `json:"started_at"`
	ExitedAt            *time.Time `json:"exited_at,omitempty"`
	ExitCode            *int       `json:"exit_code,omitempty"`
}

type agentProc struct {
	info     AgentInfo
	status   hooks.Status
	term     *terminal.Session
	listener *hooks.Listener
	settings string
	exited   chan struct{} // closed by watch after the exit is recorded
}

// Engine owns agent lifecycles. All state changes go through it.
type Engine struct {
	opts    Options
	log     *slog.Logger
	adapter agent.ClaudeCode

	mu     sync.Mutex
	agents map[string]*agentProc
	order  []string
	subs   map[chan AgentInfo]struct{}
	wg     sync.WaitGroup
}

// New validates opts and returns an engine.
func New(opts Options) (*Engine, error) {
	for name, p := range map[string]string{"Repo": opts.Repo, "RuntimeDir": opts.RuntimeDir} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("engine: %s must be an absolute path, got %q", name, p)
		}
	}
	if fi, err := os.Stat(opts.Repo); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("engine: repo %q is not a directory", opts.Repo)
	}
	if opts.ClaudeBin == "" || opts.HookCommand == "" {
		return nil, errors.New("engine: ClaudeBin and HookCommand are required")
	}
	if err := os.MkdirAll(opts.RuntimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("engine: runtime dir: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 5 * time.Second
	}
	return &Engine{
		opts:    opts,
		log:     opts.Logger,
		adapter: agent.ClaudeCode{Bin: opts.ClaudeBin},
		agents:  map[string]*agentProc{},
		subs:    map[chan AgentInfo]struct{}{},
	}, nil
}

// Start launches a new agent in the configured repo.
func (e *Engine) Start() (AgentInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.agents {
		if a.status.State != hooks.Exited {
			return AgentInfo{}, ErrAgentRunning
		}
	}

	id, sid := randomHex(6), newUUID()
	dir := filepath.Join(e.opts.RuntimeDir, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AgentInfo{}, fmt.Errorf("engine: agents dir: %w", err)
	}
	settingsPath := filepath.Join(dir, id+".settings.json")
	settings, err := agent.HookSettings(e.opts.HookCommand)
	if err != nil {
		return AgentInfo{}, err
	}
	if err := os.WriteFile(settingsPath, settings, 0o600); err != nil {
		return AgentInfo{}, fmt.Errorf("engine: write settings: %w", err)
	}
	ln, err := hooks.Listen(filepath.Join(dir, id+".sock"), e.hookHandler(id), e.log.With("agent_id", id))
	if err != nil {
		os.Remove(settingsPath)
		return AgentInfo{}, err
	}
	cmd, err := e.adapter.Command(agent.LaunchSpec{
		SessionID: sid, Model: e.opts.Model, WorkDir: e.opts.Repo, SettingsPath: settingsPath,
		InheritUserSettings: e.opts.InheritUserSettings, SocketPath: ln.Path(), BaseEnv: e.opts.BaseEnv,
	})
	if err == nil {
		var term *terminal.Session
		term, err = terminal.Start(cmd, terminal.Options{Size: e.opts.TermSize})
		if err == nil {
			a := &agentProc{
				info: AgentInfo{ID: id, SessionID: sid, Repo: e.opts.Repo, Model: e.opts.Model,
					InheritUserSettings: e.opts.InheritUserSettings, StartedAt: time.Now().UTC()},
				status:   hooks.Status{State: hooks.Starting},
				term:     term,
				listener: ln,
				settings: settingsPath,
				exited:   make(chan struct{}),
			}
			e.agents[id] = a
			e.order = append(e.order, id)
			e.publishLocked(a)
			e.log.Info("agent started", "agent_id", id, "session_id", sid, "pid", term.Pid(),
				"repo", e.opts.Repo, "model", e.opts.Model, "inherit_user_settings", e.opts.InheritUserSettings)
			e.wg.Add(1)
			go e.watch(a)
			return a.view(), nil
		}
	}
	ln.Close()
	os.Remove(settingsPath)
	return AgentInfo{}, fmt.Errorf("engine: start agent: %w", err)
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
	_ = os.Remove(a.settings)

	e.mu.Lock()
	now := time.Now().UTC()
	a.status = hooks.Status{State: hooks.Exited}
	a.info.ExitedAt, a.info.ExitCode = &now, &code
	e.publishLocked(a)
	e.mu.Unlock()
	close(a.exited)
	e.log.Info("agent exited", "agent_id", a.info.ID, "exit_code", code)
}

func (e *Engine) hookHandler(id string) hooks.Handler {
	log := e.log.With("agent_id", id)
	return func(_ context.Context, payload []byte) ([]byte, error) {
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
		next := hooks.Next(a.status, ev)
		log.Debug("hook", "hook", ev.Hook, "kind", ev.Kind, "tool", ev.Tool, "detail", ev.Detail,
			"subagent", ev.Subagent, "state", next.State)
		if next != a.status {
			a.status = next
			e.publishLocked(a)
		}
		return nil, nil // Phase 4: Stop-hook message injection goes here
	}
}

// Stop terminates a running agent and waits until it has exited.
func (e *Engine) Stop(id string) error {
	e.mu.Lock()
	a, ok := e.agents[id]
	e.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.StopTimeout)
	defer cancel()
	a.term.Stop(ctx)
	<-a.exited
	return nil
}

// Remove forgets an exited agent.
func (e *Engine) Remove(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok {
		return ErrNotFound
	}
	if a.info.ExitedAt == nil {
		return ErrStillRunning
	}
	delete(e.agents, id)
	e.order = slices.DeleteFunc(e.order, func(s string) bool { return s == id })
	return nil
}

// List returns all agents in start order.
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
	return a.term, nil
}

// Subscribe returns a channel of agent updates. The channel is closed if the
// subscriber falls behind or cancel is called; callers then resync via List.
func (e *Engine) Subscribe() (<-chan AgentInfo, func()) {
	ch := make(chan AgentInfo, 64)
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

func (e *Engine) publishLocked(a *agentProc) {
	v := a.view()
	for ch := range e.subs {
		select {
		case ch <- v:
		default:
			delete(e.subs, ch)
			close(ch)
		}
	}
}

// Shutdown stops all running agents and waits for them.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	ids := slices.Clone(e.order)
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.Stop(id) }()
	}
	wg.Wait()
	e.wg.Wait()
}

func (a *agentProc) view() AgentInfo {
	v := a.info
	v.State, v.Activity = string(a.status.State), a.status.Activity
	return v
}

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
