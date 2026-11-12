package engine

import (
	"fmt"
	"slices"

	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// liveOptions are the settings an engine reads when an agent starts or a
// team tool runs. A project may change them while its agents run: they apply
// to the next start, without reopening the project (ADR 0061). Readers load
// the pointer once per use; a change stores a new value, never edits one.
type liveOptions struct {
	MaxAgents       int // the project's own cap; the daemon-wide one applies too (maxAgents)
	MaxSubTasks     int // open sub-tasks per goal (ADR 0062)
	Model           string
	CodexModel      string
	Roles           *role.Set
	Budgets         Budgets
	AccessDecider   string
	LeadAccessHosts []string
	LeadHosts       sandbox.Allowlist // LeadAccessHosts, parsed
	MergeTarget     string
	Memory          string // sandboxed workers' memory limit; "": podman's default
	Pids            int    // their process limit; 0: none set
}

// liveFrom validates and copies the live part of opts.
func liveFrom(o Options) (*liveOptions, error) {
	lv := &liveOptions{MaxAgents: o.MaxAgents, MaxSubTasks: o.MaxSubTasks, Model: o.Model, CodexModel: o.CodexModel,
		Roles: o.Roles, Budgets: o.Budgets, AccessDecider: o.AccessDecider, LeadAccessHosts: slices.Clone(o.LeadAccessHosts),
		MergeTarget: o.MergeTarget}
	if lv.MaxAgents <= 0 {
		lv.MaxAgents = 4
	}
	if lv.MaxSubTasks <= 0 {
		lv.MaxSubTasks = DefaultMaxSubTasks
	}
	if lv.MaxSubTasks > MaxProjectSubTasks {
		return nil, fmt.Errorf("engine: MaxSubTasks must be at most %d, got %d", MaxProjectSubTasks, lv.MaxSubTasks)
	}
	if lv.Roles == nil {
		lv.Roles = role.MustBuiltins()
	}
	if b := lv.Budgets; b.SessionMicroUSD < 0 || b.GoalMicroUSD < 0 {
		return nil, fmt.Errorf("engine: budgets must not be negative, got %+v", b)
	}
	switch lv.AccessDecider {
	case "", DeciderUser, DeciderLead, DeciderLeadPolicy:
	default:
		return nil, fmt.Errorf("engine: AccessDecider %q", lv.AccessDecider)
	}
	hosts, err := sandbox.ParseAllowlist(lv.LeadAccessHosts)
	if err != nil {
		return nil, fmt.Errorf("engine: LeadAccessHosts: %w", err)
	}
	lv.LeadHosts = hosts
	if sb := o.Sandbox; sb != nil {
		lv.Memory, lv.Pids = sb.Memory, sb.Pids
	}
	return lv, nil
}

// lv returns the engine's current live settings.
func (e *Engine) lv() *liveOptions { return e.live.Load() }

// maxAgents is the cap a start in this project meets: the smaller of the
// project's own and the daemon-wide one (ADR 0047, 0061).
func (e *Engine) maxAgents() int { return min(e.lv().MaxAgents, e.slots.cap()) }

// maxSubTasks is the project's limit of open sub-tasks per goal (ADR 0062).
func (e *Engine) maxSubTasks() int { return e.lv().MaxSubTasks }

// applyLive replaces the live settings with those of o, which must differ from
// the engine's options only in live fields. Running agents keep what they
// started with; the next start uses the new values.
func (e *Engine) applyLive(o Options) error {
	lv, err := liveFrom(o)
	if err != nil {
		return err
	}
	e.live.Store(lv)
	e.log.Info("live settings changed", "max_agents", lv.MaxAgents, "max_sub_tasks", lv.MaxSubTasks, "model", lv.Model,
		"access_decider", lv.AccessDecider, "merge_target", lv.MergeTarget, "worker_memory", lv.Memory, "worker_pids", lv.Pids)
	e.publishCaps()
	return nil
}

// Caps is what limits this project's agents and sub-tasks, for the UI.
type Caps struct {
	MaxAgents   int `json:"max_agents"`    // the project's own setting
	DaemonMax   int `json:"daemon_max"`    // the daemon-wide cap
	Effective   int `json:"effective"`     // the smaller one: what a start meets
	Running     int `json:"running"`       // this project's agents running or starting
	MaxSubTasks int `json:"max_sub_tasks"` // open sub-tasks per goal
}

// Caps reports the project's caps now.
func (e *Engine) Caps() Caps {
	lv := e.lv()
	e.mu.Lock()
	running := e.runningLocked() + e.starting
	e.mu.Unlock()
	d := e.slots.cap()
	return Caps{MaxAgents: lv.MaxAgents, DaemonMax: d, Effective: min(lv.MaxAgents, d), Running: running,
		MaxSubTasks: lv.MaxSubTasks}
}

func (e *Engine) publishCaps() {
	c := e.Caps()
	e.publish(Update{Type: "caps", Caps: &c})
}
