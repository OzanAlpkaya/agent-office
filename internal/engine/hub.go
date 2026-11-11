package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Errors of the project registry (ADR 0046).
var (
	ErrProjectExists      = errors.New("the repository is already a project")
	ErrProjectBusy        = errors.New("the project has running agents; stop them first")
	ErrProjectUnavailable = errors.New("the project could not be opened")
)

// Project states (ProjectInfo.State).
const (
	ProjectOpen  = "open"  // its engine runs
	ProjectError = "error" // opening failed; Error says why, Retry tries again
)

// HubOptions configures the daemon: the part of the engine that is not one
// project (ADR 0046).
type HubOptions struct {
	// Base is what every project engine starts from: the daemon-wide fields
	// (store, runtime dir, Claude binary, logger, ...). Repo is ignored, and
	// MaxAgents is the daemon-wide cap (ADR 0047). Sandbox holds the
	// daemon-wide sandbox fields; Configure fills in a project's pack.
	Base Options
	// Defaults are the daemon's flags as project settings, with every field
	// set; a project's settings override them.
	Defaults ProjectSettings
	// Configure resolves what needs the outside world for one project: its
	// pack's image, allowlist and secrets, its GitHub client, more warnings.
	// It edits o, a copy of Base with the project's models, budgets and cap
	// already applied (o.Sandbox is a copy too). nil: nothing to add.
	Configure func(ctx context.Context, p Project, s ProjectSettings, o *Options) error
	// Containers, when set, are this daemon instance's sandbox containers to
	// remove once at start, before any project opens (the previous run died).
	Containers *SandboxInstance
}

// SandboxInstance names this daemon's containers.
type SandboxInstance struct {
	Podman   sandbox.Podman
	Instance string
}

// ProjectInfo is a project as the registry and the UI see it.
type ProjectInfo struct {
	Project
	Settings  ProjectSettings `json:"settings"`  // its own overrides
	Effective ProjectSettings `json:"effective"` // what it uses: the daemon's flags with its overrides
	State     string          `json:"state"`     // ProjectOpen | ProjectError
	Error     string          `json:"error,omitempty"`
	Default   bool            `json:"default"` // `serve --repo`'s project: the UI opens it first
	CreatedAt time.Time       `json:"created_at"`
}

// HubUpdate is a change of the project list, broadcast to subscribers.
type HubUpdate struct {
	Type    string       `json:"type"` // "project" | "project_removed"
	Project *ProjectInfo `json:"project,omitempty"`
	ID      string       `json:"id,omitempty"`
}

// HubStatus is the daemon-wide state.
type HubStatus struct {
	Sandbox   string          `json:"sandbox"`    // SandboxHost | SandboxPodman, for every project
	MaxAgents int             `json:"max_agents"` // the daemon-wide cap (ADR 0047)
	Running   int             `json:"running"`    // agents running or starting in every project
	Warnings  []string        `json:"warnings"`   // daemon-wide
	Defaults  ProjectSettings `json:"defaults"`   // the daemon's flags
	Default   string          `json:"default_project,omitempty"`
}

// AddProject describes a repository to register.
type AddProject struct {
	Repo     string          // any path inside the repository
	Name     string          // "": the repository's directory name
	Settings ProjectSettings // overrides of the daemon's flags
}

// UpdateProject changes a project; nil fields stay.
type UpdateProject struct {
	Name     *string
	Settings *ProjectSettings
}

type projectEntry struct {
	row      db.Project
	settings ProjectSettings
	eng      *Engine // nil in ProjectError
	err      error
}

// Hub runs one engine per project with a shared store and agent cap (ADR
// 0046, 0047). Project operations are serialized; reads are not blocked by
// them.
type Hub struct {
	opts  HubOptions
	log   *slog.Logger
	q     *db.Queries
	slots *slotPool

	op sync.Mutex // serializes Add, Register, Update, Remove, Retry and Shutdown

	mu        sync.Mutex
	projects  map[string]*projectEntry
	order     []string
	defaultID string
	subs      map[chan HubUpdate]struct{}
	closed    bool
}

// NewHub removes the previous run's containers and opens every registered
// project. A project that cannot open is kept in ProjectError, so one broken
// repository does not stop the daemon.
func NewHub(ctx context.Context, opts HubOptions) (*Hub, error) {
	if opts.Base.Store == nil {
		return nil, errors.New("engine: Base.Store is required")
	}
	if err := opts.Defaults.complete(); err != nil {
		return nil, err
	}
	if err := opts.Defaults.Validate(); err != nil {
		return nil, fmt.Errorf("engine: daemon defaults: %w", err)
	}
	if opts.Base.MaxAgents <= 0 {
		opts.Base.MaxAgents = 4
	}
	if opts.Base.Logger == nil {
		opts.Base.Logger = slog.Default()
	}
	// Agents' repositories are never registered or run git on (ADR 0014).
	if sb := opts.Base.Sandbox; sb != nil {
		opts.Base.Git.UntrustedRoots = append(slices.Clone(opts.Base.Git.UntrustedRoots), sb.WorkspaceRoot)
	}
	h := &Hub{opts: opts, log: opts.Base.Logger, q: opts.Base.Store.Q, slots: newSlotPool(opts.Base.MaxAgents),
		projects: map[string]*projectEntry{}, subs: map[chan HubUpdate]struct{}{}}
	if c := opts.Containers; c != nil {
		removeOrphanContainers(ctx, c.Podman, c.Instance, h.log)
	}
	rows, err := h.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: list projects: %w", err)
	}
	entries := make([]*projectEntry, len(rows))
	var wg sync.WaitGroup
	for i, r := range rows {
		wg.Add(1)
		go func() { // projects are independent; a slow GitHub check must not hold up the others
			defer wg.Done()
			entries[i] = h.open(ctx, r)
		}()
	}
	wg.Wait()
	h.mu.Lock()
	for _, e := range entries {
		h.projects[e.row.ID] = e
		h.order = append(h.order, e.row.ID)
	}
	h.mu.Unlock()
	return h, nil
}

// open starts the project's engine; a failure is recorded in the entry.
func (h *Hub) open(ctx context.Context, row db.Project) *projectEntry {
	pe := &projectEntry{row: row}
	log := h.log.With("project", row.Slug)
	s, err := ParseProjectSettings([]byte(row.Settings))
	if err != nil {
		pe.err = fmt.Errorf("stored settings: %w", err)
		log.Error("project not opened", "repo", row.RepoPath, "err", pe.err)
		return pe
	}
	pe.settings = s
	o, err := h.options(ctx, row, s)
	if err == nil {
		pe.eng, err = New(ctx, o)
	}
	if err != nil {
		pe.err = err
		log.Error("project not opened", "repo", row.RepoPath, "err", err)
		return pe
	}
	log.Info("project opened", "repo", row.RepoPath, "name", row.Name, "max_agents", o.MaxAgents,
		"pack", h.opts.Defaults.Merge(s).PackName())
	return pe
}

// options is the engine options of one project: the daemon's with the
// project's settings applied.
func (h *Hub) options(ctx context.Context, row db.Project, s ProjectSettings) (Options, error) {
	eff := h.opts.Defaults.Merge(s)
	o := h.opts.Base
	o.project, o.slots, o.Repo = &row, h.slots, row.RepoPath
	o.Warnings = slices.Clone(o.Warnings)
	if o.Sandbox != nil {
		sb := *o.Sandbox
		o.Sandbox = &sb
	}
	if d := *h.opts.Defaults.SecretsDir; *eff.SecretsDir != d && !within(*eff.SecretsDir, d) {
		// A project's secrets live in the daemon's secrets dir or one of its
		// subdirectories, so no setting points a pack at other files (found in the
		// Phase 14 security review).
		return o, invalid("secrets_dir %s: must be %s or a directory inside it, e.g. %s", *eff.SecretsDir, d,
			filepath.Join(d, row.Slug))
	}
	o.Model, o.CodexModel = *eff.Model, *eff.CodexModel
	if len(eff.RoleCLIs) > 0 {
		if o.Roles == nil {
			o.Roles = role.MustBuiltins()
		}
		roles, err := o.Roles.WithCLIs(eff.RoleCLIs)
		if err != nil {
			return o, fmt.Errorf("%w: role_clis: %v", ErrInvalid, err)
		}
		o.Roles = roles
	}
	o.MaxAgents = min(*eff.MaxAgents, h.opts.Base.MaxAgents)
	o.Budgets = Budgets{SessionMicroUSD: *eff.BudgetSessionMicroUSD, GoalMicroUSD: *eff.BudgetGoalMicroUSD}
	if len(eff.RoleModels) > 0 {
		if o.Roles == nil {
			o.Roles = role.MustBuiltins()
		}
		roles, err := o.Roles.WithModels(eff.RoleModels)
		if err != nil {
			return o, fmt.Errorf("%w: role_models: %v", ErrInvalid, err)
		}
		o.Roles = roles
	}
	o.AccessDecider, o.LeadAccessHosts = *eff.AccessDecider, slices.Clone(eff.LeadAccessHosts)
	o.MergeTarget, o.PushRemote = *eff.MergeTarget, *eff.GitHubRemote // ADR 0055
	if h.opts.Configure != nil {
		p := Project{ID: row.ID, Name: row.Name, RepoPath: row.RepoPath, Slug: row.Slug}
		if err := h.opts.Configure(ctx, p, eff, &o); err != nil {
			return o, err
		}
	}
	if m := eff.WorkerMemory; m != nil && *m != "" && o.Sandbox != nil {
		o.Sandbox.Memory = *m // the project's own copy (above); over the pack's and --sandbox-memory (ADR 0051)
	}
	if n := eff.WorkerPids; n != nil && *n != 0 && o.Sandbox != nil {
		o.Sandbox.Pids = *n // over the pack's and --sandbox-pids, like memory (Phase 17)
	}
	return o, nil
}

// Projects lists the registered projects, oldest first.
func (h *Hub) Projects() []ProjectInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ProjectInfo, 0, len(h.order))
	for _, id := range h.order {
		out = append(out, h.infoLocked(h.projects[id]))
	}
	return out
}

// Project returns one project.
func (h *Hub) Project(id string) (ProjectInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pe, ok := h.projects[id]
	if !ok {
		return ProjectInfo{}, fmt.Errorf("project %s: %w", id, ErrNotFound)
	}
	return h.infoLocked(pe), nil
}

// Find resolves a project by id or, when unique, by name (the CLI's
// argument).
func (h *Hub) Find(ref string) (ProjectInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pe, ok := h.projects[ref]; ok {
		return h.infoLocked(pe), nil
	}
	var found []*projectEntry
	for _, id := range h.order {
		if strings.EqualFold(h.projects[id].row.Name, ref) {
			found = append(found, h.projects[id])
		}
	}
	switch len(found) {
	case 1:
		return h.infoLocked(found[0]), nil
	case 0:
		return ProjectInfo{}, fmt.Errorf("project %q: %w", ref, ErrNotFound)
	default:
		return ProjectInfo{}, invalid("%d projects are named %q; use the id", len(found), ref)
	}
}

// Engine returns the open project's engine.
func (h *Hub) Engine(id string) (*Engine, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pe, ok := h.projects[id]
	if !ok {
		return nil, fmt.Errorf("project %s: %w", id, ErrNotFound)
	}
	if pe.eng == nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectUnavailable, pe.err)
	}
	return pe.eng, nil
}

// Status is the daemon-wide state.
func (h *Hub) Status() HubStatus {
	st := HubStatus{Sandbox: SandboxHost, MaxAgents: h.opts.Base.MaxAgents, Running: h.slots.total(),
		Warnings: append([]string{}, h.opts.Base.Warnings...), Defaults: h.opts.Defaults}
	if h.opts.Base.Sandbox != nil {
		st.Sandbox = SandboxPodman
	}
	h.mu.Lock()
	st.Default = h.defaultID
	h.mu.Unlock()
	return st
}

func (h *Hub) infoLocked(pe *projectEntry) ProjectInfo {
	r := pe.row
	in := ProjectInfo{Project: Project{ID: r.ID, Name: r.Name, RepoPath: r.RepoPath, Slug: r.Slug},
		Settings: pe.settings, Effective: h.opts.Defaults.Merge(pe.settings), State: ProjectOpen,
		Default: r.ID == h.defaultID, CreatedAt: r.CreatedAt}
	if m := min(*in.Effective.MaxAgents, h.opts.Base.MaxAgents); m != *in.Effective.MaxAgents {
		in.Effective.MaxAgents = &m
	}
	if pe.eng == nil {
		in.State, in.Error = ProjectError, errText(pe.err)
	}
	return in
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Register makes repo a project if it is not one yet (or brings it back),
// opens it and makes it the default: `serve --repo` (ADR 0046). Its settings
// are not touched. Unlike other projects, a failure is returned.
func (h *Hub) Register(ctx context.Context, repo string) (ProjectInfo, error) {
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closedErr(); err != nil {
		return ProjectInfo{}, err
	}
	if err := h.ownPath(repo); err != nil {
		return ProjectInfo{}, err
	}
	row, _, err := registerProject(ctx, h.q, h.opts.Base.Git, repo, "", "")
	if err != nil {
		return ProjectInfo{}, err
	}
	h.mu.Lock()
	pe, ok := h.projects[row.ID]
	h.mu.Unlock()
	if !ok || pe.eng == nil {
		if pe = h.open(ctx, row); pe.eng == nil {
			h.put(pe)
			return ProjectInfo{}, pe.err
		}
		h.put(pe)
	}
	h.mu.Lock()
	h.defaultID = row.ID
	info := h.infoLocked(pe)
	h.mu.Unlock()
	h.publish(HubUpdate{Type: "project", Project: &info})
	return info, nil
}

// Add registers a repository as a new project (or brings a removed one back,
// with its history) and opens it. If it cannot open, nothing is registered.
func (h *Hub) Add(ctx context.Context, in AddProject) (ProjectInfo, error) {
	if err := in.Settings.Validate(); err != nil {
		return ProjectInfo{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name != "" {
		if err := validProjectName(name); err != nil {
			return ProjectInfo{}, err
		}
	}
	if !filepath.IsAbs(in.Repo) {
		return ProjectInfo{}, invalid("the repository path must be absolute, got %q", in.Repo)
	}
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closedErr(); err != nil {
		return ProjectInfo{}, err
	}
	enc, err := in.Settings.encode()
	if err != nil {
		return ProjectInfo{}, err
	}
	if err := h.ownPath(in.Repo); err != nil {
		return ProjectInfo{}, err
	}
	row, how, err := registerProject(ctx, h.q, h.opts.Base.Git, in.Repo, name, enc)
	if err != nil {
		return ProjectInfo{}, err
	}
	if how == registeredExisting {
		return ProjectInfo{}, fmt.Errorf("%w: %s is the project %q", ErrProjectExists, row.RepoPath, row.Name)
	}
	if how == registeredRestored && (name != "" || enc != "{}") {
		// Brought back: what this add says replaces what it had.
		if name == "" {
			name = row.Name
		}
		if row, err = h.q.UpdateProject(ctx, db.UpdateProjectParams{Name: name, Settings: enc, ID: row.ID}); err != nil {
			return ProjectInfo{}, fmt.Errorf("engine: %w", err)
		}
	}
	pe := h.open(context.WithoutCancel(ctx), row)
	if pe.eng == nil {
		if _, rerr := h.q.RemoveProject(context.Background(), db.RemoveProjectParams{RemovedAt: nullTime(now()), ID: row.ID}); rerr != nil {
			h.log.Error("unregister the project that did not open", "project", row.Slug, "err", rerr)
		}
		return ProjectInfo{}, fmt.Errorf("%w: %v", ErrProjectUnavailable, pe.err)
	}
	info := h.put(pe)
	h.log.Info("project added", "project", row.Slug, "repo", row.RepoPath, "restored", how == registeredRestored,
		"settings", row.Settings)
	return info, nil
}

// Update renames a project or changes its settings. New settings reopen the
// project, so they are refused while its agents run.
func (h *Hub) Update(ctx context.Context, id string, in UpdateProject) (ProjectInfo, error) {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if err := validProjectName(n); err != nil {
			return ProjectInfo{}, err
		}
		in.Name = &n
	}
	if in.Settings != nil {
		if err := in.Settings.Validate(); err != nil {
			return ProjectInfo{}, err
		}
	}
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closedErr(); err != nil {
		return ProjectInfo{}, err
	}
	h.mu.Lock()
	pe, ok := h.projects[id]
	h.mu.Unlock()
	if !ok {
		return ProjectInfo{}, fmt.Errorf("project %s: %w", id, ErrNotFound)
	}
	name, settings := pe.row.Name, pe.settings
	if in.Name != nil {
		name = *in.Name
	}
	reopen := false
	if in.Settings != nil {
		a, _ := pe.settings.encode()
		b, err := in.Settings.encode()
		if err != nil {
			return ProjectInfo{}, err
		}
		reopen = a != b || pe.eng == nil
		settings = *in.Settings
	}
	enc, err := settings.encode()
	if err != nil {
		return ProjectInfo{}, err
	}
	// Once the old engine stops, the open must not depend on the caller staying.
	octx := context.WithoutCancel(ctx)
	if reopen {
		// Everything the new settings need (pack image, secrets, GitHub, role
		// models) is checked before anything stops.
		cand := pe.row
		cand.Name, cand.Settings = name, enc
		if _, err := h.options(ctx, cand, settings); err != nil {
			return ProjectInfo{}, fmt.Errorf("%w: %v", ErrProjectUnavailable, err)
		}
	}
	if reopen && pe.eng != nil {
		if !pe.eng.closeIdle() {
			return ProjectInfo{}, ErrProjectBusy
		}
		pe.eng.Shutdown()
	}
	row, err := h.q.UpdateProject(octx, db.UpdateProjectParams{Name: name, Settings: enc, ID: id})
	if err != nil {
		if reopen && pe.eng != nil { // closed above: open it again as it was
			h.put(h.open(octx, pe.row))
		}
		return ProjectInfo{}, fmt.Errorf("engine: %w", err)
	}
	if !reopen {
		h.mu.Lock()
		pe.row = row
		info := h.infoLocked(pe)
		h.mu.Unlock()
		h.publish(HubUpdate{Type: "project", Project: &info})
		return info, nil
	}
	ne := h.open(octx, row)
	if ne.eng == nil && pe.eng != nil {
		// It ran before and does not with the new settings: back to the old ones.
		if _, rerr := h.q.UpdateProject(octx, db.UpdateProjectParams{Name: pe.row.Name, Settings: pe.row.Settings, ID: id}); rerr != nil {
			h.log.Error("restore the project's previous settings", "project", row.Slug, "err", rerr)
		}
		h.put(h.open(octx, pe.row))
		return ProjectInfo{}, fmt.Errorf("%w: %v", ErrProjectUnavailable, ne.err)
	}
	info := h.put(ne)
	h.log.Info("project settings changed", "project", row.Slug, "state", info.State, "from", pe.row.Settings, "to", row.Settings)
	return info, nil
}

// Retry opens a project that failed to open, e.g. after its pack image was
// built.
func (h *Hub) Retry(ctx context.Context, id string) (ProjectInfo, error) {
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closedErr(); err != nil {
		return ProjectInfo{}, err
	}
	h.mu.Lock()
	pe, ok := h.projects[id]
	h.mu.Unlock()
	if !ok {
		return ProjectInfo{}, fmt.Errorf("project %s: %w", id, ErrNotFound)
	}
	if pe.eng != nil {
		return h.Project(id)
	}
	row, err := h.q.GetProject(ctx, id)
	if err != nil {
		return ProjectInfo{}, fmt.Errorf("engine: %w", err)
	}
	return h.put(h.open(context.WithoutCancel(ctx), row)), nil
}

// Remove unregisters a project whose agents are all stopped. Its tasks,
// history, branches, worktrees and usage stay; adding the repository again
// brings them back (ADR 0046).
func (h *Hub) Remove(ctx context.Context, id string) error {
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closedErr(); err != nil {
		return err
	}
	h.mu.Lock()
	pe, ok := h.projects[id]
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("project %s: %w", id, ErrNotFound)
	}
	if pe.eng != nil && !pe.eng.closeIdle() {
		return ErrProjectBusy
	}
	if _, err := h.q.RemoveProject(context.WithoutCancel(ctx), db.RemoveProjectParams{RemovedAt: nullTime(now()), ID: id}); err != nil {
		if pe.eng != nil { // closing but not stopped: open it again so it is not left half-closed
			pe.eng.Shutdown()
			h.put(h.open(context.WithoutCancel(ctx), pe.row))
		}
		return fmt.Errorf("engine: %w", err)
	}
	if pe.eng != nil {
		pe.eng.Shutdown()
	}
	h.mu.Lock()
	delete(h.projects, id)
	h.order = slices.DeleteFunc(h.order, func(x string) bool { return x == id })
	if h.defaultID == id {
		h.defaultID = ""
	}
	h.mu.Unlock()
	h.log.Info("project removed", "project", pe.row.Slug, "repo", pe.row.RepoPath)
	h.publish(HubUpdate{Type: "project_removed", ID: id})
	return nil
}

// put stores an entry (new or replacing) and publishes it.
func (h *Hub) put(pe *projectEntry) ProjectInfo {
	h.mu.Lock()
	if _, ok := h.projects[pe.row.ID]; !ok {
		h.order = append(h.order, pe.row.ID)
	}
	h.projects[pe.row.ID] = pe
	info := h.infoLocked(pe)
	h.mu.Unlock()
	h.publish(HubUpdate{Type: "project", Project: &info})
	return info
}

func (h *Hub) closedErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrShuttingDown
	}
	return nil
}

// Subscribe returns a channel of project list changes; see Engine.Subscribe.
func (h *Hub) Subscribe() (<-chan HubUpdate, func()) {
	ch := make(chan HubUpdate, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *Hub) publish(u HubUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- u:
		default: // a slow subscriber is dropped and resyncs
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Shutdown stops every project's agents (keeping what a restore needs, ADR
// 0035) and waits for them.
func (h *Hub) Shutdown() {
	h.op.Lock()
	defer h.op.Unlock()
	h.mu.Lock()
	h.closed = true
	var engines []*Engine
	for _, id := range h.order {
		if e := h.projects[id].eng; e != nil {
			engines = append(engines, e)
		}
	}
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func() { defer wg.Done(); e.Shutdown() }()
	}
	wg.Wait()
}

// ownPath refuses a repository under the data dir: an agent's worktree or
// sandbox clone is the agent's, never a project of its own.
func (h *Hub) ownPath(repo string) error {
	roots := []string{h.opts.Base.WorktreeRoot}
	if sb := h.opts.Base.Sandbox; sb != nil {
		roots = append(roots, sb.WorkspaceRoot, sb.AgentsRoot)
	}
	clean := filepath.Clean(repo)
	for _, r := range roots {
		if r != "" && (clean == r || within(clean, r)) {
			return invalid("%s is inside agent-office's data dir (%s): agents' workspaces cannot be projects", repo, r)
		}
	}
	return nil
}

// within reports whether path is strictly inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// How registerProject found the repository.
const (
	registeredCreated  = "created"
	registeredRestored = "restored" // a removed project, brought back
	registeredExisting = "existing"
)

// registerProject finds or creates the project of the repository containing
// repo; a removed one is brought back. name and settings apply to a new one
// only ("" = the directory name, "" = no overrides).
func registerProject(ctx context.Context, q *db.Queries, g git.Git, repo, name, settings string) (db.Project, string, error) {
	top, err := g.TopLevel(ctx, repo)
	if err != nil {
		return db.Project{}, "", invalid("%s is not inside a git repository: %v", repo, err)
	}
	if _, err := g.HeadCommit(ctx, top); err != nil {
		return db.Project{}, "", invalid("the repository %s has no commits yet: %v", top, err)
	}
	if fi, err := os.Lstat(filepath.Join(top, ".git")); err == nil && !fi.IsDir() {
		// A linked worktree (or submodule) shares another repository's objects and branches.
		return db.Project{}, "", invalid("%s is a linked worktree or submodule, not a repository of its own; add its main repository", top)
	}
	row, err := q.GetProjectByRepo(ctx, top)
	switch {
	case err == nil && row.RemovedAt.Valid:
		row, err = q.RestoreProject(ctx, row.ID)
		if err != nil {
			return db.Project{}, "", fmt.Errorf("restore project: %w", err)
		}
		return row, registeredRestored, nil
	case err == nil:
		return row, registeredExisting, nil
	case !errors.Is(err, sql.ErrNoRows):
		return db.Project{}, "", fmt.Errorf("find project: %w", err)
	}
	if name == "" {
		name = filepath.Base(top)
		if utf8.RuneCountInString(name) > maxProjectName {
			name = string([]rune(name)[:maxProjectName])
		}
	}
	if settings == "" {
		settings = "{}"
	}
	sum := sha256.Sum256([]byte(top))
	slug := git.Slug(filepath.Base(top), 30)
	if slug == "" {
		slug = "repo"
	}
	slug += "-" + hex.EncodeToString(sum[:])[:8]
	row, err = q.CreateProject(ctx, db.CreateProjectParams{ID: randomHex(6), Name: name, RepoPath: top, Slug: slug,
		CreatedAt: now(), Settings: settings})
	if err != nil {
		return db.Project{}, "", fmt.Errorf("register project: %w", err)
	}
	return row, registeredCreated, nil
}

func validProjectName(n string) error {
	if n == "" || utf8.RuneCountInString(n) > maxProjectName || !utf8.ValidString(n) {
		return invalid("a project name has 1 to %d characters", maxProjectName)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return invalid("a project name has no control characters")
		}
	}
	return nil
}

// ProjectUsage is one project's usage totals (ADR 0046).
type ProjectUsage struct {
	ProjectID string      `json:"project_id"`
	Name      string      `json:"name"`
	State     string      `json:"state"`
	Totals    UsageTotals `json:"totals"` // zero for a project that is not open
}

// DaemonUsage is GET /api/usage: totals per project and overall.
type DaemonUsage struct {
	Prices   PriceInfo      `json:"prices"`
	Projects []ProjectUsage `json:"projects"`
	Totals   UsageTotals    `json:"totals"`
}

// Usage sums every open project's usage.
func (h *Hub) Usage(ctx context.Context) (DaemonUsage, error) {
	out := DaemonUsage{Projects: []ProjectUsage{}}
	if p := h.opts.Base.Prices; p != nil {
		out.Prices = PriceInfo{AsOf: p.AsOf, Source: p.Source, File: p.File}
	}
	for _, p := range h.Projects() {
		pu := ProjectUsage{ProjectID: p.ID, Name: p.Name, State: p.State}
		if e, err := h.Engine(p.ID); err == nil {
			rep, err := e.Usage(ctx)
			if err != nil {
				return out, err
			}
			pu.Totals = rep.Totals
			out.Prices = rep.Prices
		}
		t := &out.Totals
		t.Sessions += pu.Totals.Sessions
		t.Messages += pu.Totals.Messages
		t.Tokens = t.Tokens.Add(pu.Totals.Tokens)
		t.UnpricedTokens = satAdd(t.UnpricedTokens, pu.Totals.UnpricedTokens)
		t.CostMicroUSD = satAdd(t.CostMicroUSD, pu.Totals.CostMicroUSD)
		out.Projects = append(out.Projects, pu)
	}
	return out, nil
}

// satAdd adds non-negative amounts without wrapping (money is never allowed
// to wrap, ADR 0026).
func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
