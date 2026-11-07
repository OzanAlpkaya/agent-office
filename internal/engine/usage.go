package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

// Usage sources (usage_messages.source, ADR 0026).
const (
	SourceAPI        = "api"        // metered on a sandboxed agent's API socket
	SourceTranscript = "transcript" // read from a host-mode agent's transcript
)

// Budgets are estimated-cost limits for sandboxed agents in micro-USD (ADR
// 0027); 0 is off. Money is integer (ADR 0026).
type Budgets struct {
	SessionMicroUSD int64 `json:"session_micro_usd"`
	GoalMicroUSD    int64 `json:"goal_micro_usd"`
}

func (b Budgets) on() bool { return b.SessionMicroUSD > 0 || b.GoalMicroUSD > 0 }

// dollars formats micro-USD for messages.
func dollars(micro int64) string {
	return fmt.Sprintf("$%d.%02d", micro/1_000_000, micro%1_000_000/10_000)
}

// transcriptEvery bounds transcript reads during a turn (host mode).
const transcriptEvery = 10 * time.Second

// SessionUsage is one agent session's usage and counters, as the UI shows it.
type SessionUsage struct {
	SessionID string       `json:"session_id"`
	AgentID   string       `json:"agent_id"`
	TaskID    string       `json:"task_id,omitempty"`
	GoalID    string       `json:"goal_id,omitempty"` // the task's goal: its parent, or the task itself
	Role      string       `json:"role"`
	Sandbox   string       `json:"sandbox"`
	Source    string       `json:"source"` // SourceAPI | SourceTranscript
	StartedAt time.Time    `json:"started_at"`
	EndedAt   *time.Time   `json:"ended_at,omitempty"`
	Turns     int64        `json:"turns"`
	ToolCalls int64        `json:"tool_calls"`
	Messages  int          `json:"messages"`
	Tokens    usage.Tokens `json:"tokens"`
	// CostMicroUSD is the estimated cost of the priced messages in micro-USD.
	CostMicroUSD int64 `json:"cost_micro_usd"`
	// UnpricedTokens are tokens of models without a price: their cost is
	// unknown, never counted as zero.
	UnpricedTokens  int64    `json:"unpriced_tokens"`
	CLICostMicroUSD *int64   `json:"cli_cost_micro_usd,omitempty"` // Claude Code's own figure (host mode)
	Models          []string `json:"models"`

	nano int64 // the cost before rounding, for sums
}

// ModelUsage is the usage of one model across the project.
type ModelUsage struct {
	Model        string       `json:"model"`
	Priced       bool         `json:"priced"`
	Messages     int          `json:"messages"`
	Tokens       usage.Tokens `json:"tokens"`
	CostMicroUSD int64        `json:"cost_micro_usd"`
	nano         int64
}

// UsageTotals sums a report.
type UsageTotals struct {
	Sessions       int          `json:"sessions"`
	Messages       int          `json:"messages"`
	Tokens         usage.Tokens `json:"tokens"`
	CostMicroUSD   int64        `json:"cost_micro_usd"`
	UnpricedTokens int64        `json:"unpriced_tokens"`
}

// PriceInfo describes the price table in use.
type PriceInfo struct {
	AsOf   string `json:"as_of"`
	Source string `json:"source"`
	File   string `json:"file,omitempty"`
}

// UsageReport is GET /api/usage: every session of the project with its
// usage, and the usage per model. The UI groups sessions by agent, task and
// goal.
type UsageReport struct {
	Prices   PriceInfo      `json:"prices"`
	Budgets  Budgets        `json:"budgets"`
	Totals   UsageTotals    `json:"totals"`
	Sessions []SessionUsage `json:"sessions"`
	Models   []ModelUsage   `json:"models"`
}

// sessionUsage is a running session's usage, kept in memory for live
// updates and the session budget. Its own lock: the API proxy reports usage
// from its goroutines without the engine lock.
type sessionUsage struct {
	view SessionUsage // identity and counters; Tokens, CostUSD etc. are computed

	mu       sync.Mutex
	msgs     map[string]usage.Message
	order    []string
	pubTimer *time.Timer
	lastPub  time.Time

	// Host mode: the transcript, read by one goroutine at a time.
	readMu     sync.Mutex
	transcript *usage.Session
	refused    bool      // the path was refused; logged once; guarded by Engine.mu
	lastRead   time.Time // guarded by Engine.mu
}

func (e *Engine) prices() *usage.Prices { return e.opts.Prices }

// newSessionUsage starts the usage record of a launched session.
func (e *Engine) newSessionUsage(l launchInfo, sandboxMode string) *sessionUsage {
	source := SourceTranscript
	if sandboxMode == SandboxPodman {
		source = SourceAPI
	}
	su := &sessionUsage{msgs: map[string]usage.Message{}, view: SessionUsage{SessionID: l.sid, AgentID: l.id,
		TaskID: l.taskID, Role: l.role, Sandbox: sandboxMode, Source: source, StartedAt: l.started}}
	if l.taskID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if t, err := e.q.GetTask(ctx, l.taskID); err == nil {
			su.view.GoalID = goalOf(t)
		}
	}
	return su
}

func goalOf(t db.Task) string {
	if t.ParentTaskID.Valid && t.ParentTaskID.String != "" {
		return t.ParentTaskID.String
	}
	return t.ID
}

// snapshot returns the session's usage priced with p. The caller holds su.mu.
func (su *sessionUsage) snapshotLocked(p *usage.Prices) SessionUsage {
	v := su.view
	v.Tokens, v.nano, v.UnpricedTokens, v.Messages = usage.Tokens{}, 0, 0, len(su.order)
	models := map[string]bool{}
	for _, id := range su.order {
		m := su.msgs[id]
		v.Tokens = v.Tokens.Add(m.Tokens)
		if c, ok := p.Cost(m); ok {
			v.nano += c
		} else {
			v.UnpricedTokens += m.Total()
		}
		models[m.Model] = true
	}
	v.Models = sortedKeys(models)
	v.CostMicroUSD = usage.MicroUSD(v.nano)
	return v
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// recordUsage stores messages of a session and schedules a live update.
func (e *Engine) recordUsage(su *sessionUsage, msgs []usage.Message, log *slog.Logger) {
	if len(msgs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t := now()
	for _, m := range msgs {
		if err := e.q.UpsertUsage(ctx, db.UpsertUsageParams{SessionID: su.view.SessionID, AgentID: su.view.AgentID,
			TaskID: nullString(su.view.TaskID), Source: su.view.Source, MessageID: m.ID, RequestID: m.RequestID,
			Model: m.Model, Speed: m.Speed, InferenceGeo: m.Geo, InputTokens: m.Input, OutputTokens: m.Output,
			CacheRead: m.CacheRead, CacheWrite5m: m.CacheWrite5m, CacheWrite1h: m.CacheWrite1h, At: t, UpdatedAt: t}); err != nil {
			log.Warn("persist usage", "message_id", m.ID, "err", err)
			continue
		}
		su.mu.Lock()
		if _, ok := su.msgs[m.ID]; !ok {
			su.order = append(su.order, m.ID)
		}
		su.msgs[m.ID] = m
		su.mu.Unlock()
	}
	e.publishUsageSoon(su)
}

// countLocked adds to the session's turn and tool-call counters (main agent
// only). The caller holds e.mu.
func (e *Engine) countLocked(ctx context.Context, a *agentProc, ev agent.Event, log *slog.Logger) {
	su := a.usage
	if su == nil || ev.Subagent {
		return
	}
	var turns, tools int64
	switch ev.Kind {
	case agent.TurnFinished:
		turns = 1
	case agent.ToolStarted:
		tools = 1
	default:
		return
	}
	if err := e.q.AddSessionCounts(ctx, db.AddSessionCountsParams{Turns: turns, ToolCalls: tools, ID: su.view.SessionID}); err != nil {
		log.Warn("persist session counts", "err", err)
	}
	su.mu.Lock()
	su.view.Turns += turns
	su.view.ToolCalls += tools
	su.mu.Unlock()
	e.publishUsageSoon(su)
}

// publishUsageSoon sends the session's usage to subscribers, at most once
// per usageThrottle.
func (e *Engine) publishUsageSoon(su *sessionUsage) {
	su.mu.Lock()
	defer su.mu.Unlock()
	if su.pubTimer != nil {
		return
	}
	wait := max(e.opts.usageThrottle-time.Since(su.lastPub), 0)
	su.pubTimer = time.AfterFunc(wait, func() { e.publishUsage(su) })
}

// publishUsage sends the session's usage now.
func (e *Engine) publishUsage(su *sessionUsage) {
	su.mu.Lock()
	if su.pubTimer != nil {
		su.pubTimer.Stop()
		su.pubTimer = nil
	}
	su.lastPub = time.Now()
	v := su.snapshotLocked(e.prices())
	su.mu.Unlock()
	e.publish(Update{Type: "usage", Usage: &v})
}

// ---- Host mode: transcripts ----

// transcriptHookLocked learns a host session's transcript path from its
// hooks and reads new usage after turns, subagents, the session's end and at
// most every transcriptEvery during a turn. The caller holds e.mu.
func (e *Engine) transcriptHookLocked(a *agentProc, ev agent.Event, log *slog.Logger) {
	su := a.usage
	if su == nil || a.info.Sandbox != SandboxHost {
		return
	}
	if su.transcript == nil && !su.refused && ev.TranscriptPath != "" {
		s, err := usage.SessionTranscript(ev.TranscriptPath, a.info.SessionID)
		if err != nil {
			su.refused = true
			log.Warn("transcript path refused; usage of this session is not recorded", "err", err)
			return
		}
		su.readMu.Lock()
		su.transcript = s
		su.readMu.Unlock()
		su.lastRead = time.Now() // nothing to read yet at session start
	}
	if su.transcript == nil {
		return
	}
	due := ev.Kind == agent.TurnFinished || ev.Kind == agent.SessionEnded || ev.Hook == "SubagentStop" ||
		time.Since(su.lastRead) >= transcriptEvery
	if !due {
		return
	}
	su.lastRead = time.Now()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.readTranscript(su, log)
	}()
}

// readTranscript reads what was added to the session's transcript.
func (e *Engine) readTranscript(su *sessionUsage, log *slog.Logger) {
	su.readMu.Lock()
	defer su.readMu.Unlock()
	if su.transcript == nil {
		return
	}
	r, err := su.transcript.ReadNew()
	if errors.Is(err, os.ErrNotExist) {
		return // not written yet
	}
	if err != nil {
		log.Warn("read transcript", "err", err) // the error never names the path
		return
	}
	if r.Skipped > 0 {
		log.Warn("transcript lines skipped", "count", r.Skipped)
	}
	if r.CLICostMicroUSD != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := e.q.SetSessionCLICost(ctx, db.SetSessionCLICostParams{CliCostMicroUsd: sql.NullInt64{Int64: *r.CLICostMicroUSD, Valid: true},
			ID: su.view.SessionID}); err != nil {
			log.Warn("persist cli cost", "err", err)
		}
		cancel()
		su.mu.Lock()
		v := *r.CLICostMicroUSD
		su.view.CLICostMicroUSD = &v
		su.mu.Unlock()
	}
	e.recordUsage(su, r.Messages, log)
}

// ---- Budgets (ADR 0027) ----

var errUnpriced = errors.New("no price for a model this agent used, so its spend is unknown; add it to the price table")

// allowRequest is the API proxy's budget check for a sandboxed session. It
// runs before every request is forwarded.
func (e *Engine) allowRequest(su *sessionUsage) error {
	b := e.opts.Budgets
	if !b.on() {
		return nil
	}
	p := e.prices()
	if b.SessionMicroUSD > 0 {
		su.mu.Lock()
		v := su.snapshotLocked(p)
		su.mu.Unlock()
		if v.UnpricedTokens > 0 {
			return errUnpriced
		}
		if v.nano >= b.SessionMicroUSD*1000 {
			return fmt.Errorf("this session's budget of %s is spent (%s estimated)", dollars(b.SessionMicroUSD), dollars(v.CostMicroUSD))
		}
	}
	if b.GoalMicroUSD > 0 && su.view.GoalID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := e.q.ListGoalUsage(ctx, nullString(su.view.GoalID))
		if err != nil {
			return fmt.Errorf("the goal's spend cannot be read (%v); refusing while a goal budget is set", err)
		}
		var total int64 // nano-USD
		for _, r := range rows {
			c, ok := p.Cost(messageFromUsageRow(r))
			if !ok {
				return errUnpriced
			}
			total += c
		}
		if total >= b.GoalMicroUSD*1000 {
			return fmt.Errorf("the goal's budget of %s is spent (%s estimated)", dollars(b.GoalMicroUSD), dollars(usage.MicroUSD(total)))
		}
	}
	return nil
}

func messageFromUsageRow(r db.UsageMessage) usage.Message {
	return usage.Message{ID: r.MessageID, RequestID: r.RequestID, Model: r.Model, Speed: r.Speed, Geo: r.InferenceGeo,
		Tokens: usage.Tokens{Input: r.InputTokens, Output: r.OutputTokens, CacheRead: r.CacheRead,
			CacheWrite5m: r.CacheWrite5m, CacheWrite1h: r.CacheWrite1h}}
}

// ---- Report ----

// Usage returns every session of the project with its usage, priced with
// the current table (ADR 0026).
func (e *Engine) Usage(ctx context.Context) (UsageReport, error) {
	p := e.prices()
	rep := UsageReport{Prices: PriceInfo{AsOf: p.AsOf, Source: p.Source, File: p.File}, Budgets: e.opts.Budgets,
		Sessions: []SessionUsage{}, Models: []ModelUsage{}}
	sessions, err := e.q.ListProjectSessions(ctx, e.project.ID)
	if err != nil {
		return rep, fmt.Errorf("engine: list sessions: %w", err)
	}
	rows, err := e.q.ListProjectUsage(ctx, e.project.ID)
	if err != nil {
		return rep, fmt.Errorf("engine: list usage: %w", err)
	}
	tasks, err := e.q.ListTasks(ctx, e.project.ID)
	if err != nil {
		return rep, fmt.Errorf("engine: list tasks: %w", err)
	}
	goals := make(map[string]string, len(tasks))
	for _, t := range tasks {
		goals[t.ID] = goalOf(t)
	}
	idx := make(map[string]int, len(sessions))
	models := map[string]bool{}
	for _, s := range sessions {
		v := SessionUsage{SessionID: s.ID, AgentID: s.AgentID, TaskID: s.TaskID.String, GoalID: goals[s.TaskID.String],
			Role: s.Role, Sandbox: s.Sandbox, Source: SourceTranscript, StartedAt: s.StartedAt.UTC(), Turns: s.Turns,
			ToolCalls: s.ToolCalls, Models: []string{}}
		if s.Sandbox == SandboxPodman {
			v.Source = SourceAPI
		}
		if s.EndedAt.Valid {
			t := s.EndedAt.Time.UTC()
			v.EndedAt = &t
		}
		if s.CliCostMicroUsd.Valid {
			c := s.CliCostMicroUsd.Int64
			v.CLICostMicroUSD = &c
		}
		idx[s.ID] = len(rep.Sessions)
		rep.Sessions = append(rep.Sessions, v)
	}
	byModel := map[string]*ModelUsage{}
	var totalNano int64
	sessModels := make([]map[string]bool, len(rep.Sessions))
	for _, r := range rows {
		m := messageFromUsageRow(r)
		c, priced := p.Cost(m)
		mu := byModel[m.Model]
		if mu == nil {
			mu = &ModelUsage{Model: m.Model, Priced: priced}
			byModel[m.Model] = mu
		}
		mu.Messages++
		mu.Tokens = mu.Tokens.Add(m.Tokens)
		mu.nano += c
		models[m.Model] = true
		rep.Totals.Messages++
		rep.Totals.Tokens = rep.Totals.Tokens.Add(m.Tokens)
		totalNano += c
		if !priced {
			rep.Totals.UnpricedTokens += m.Total()
		}
		i, ok := idx[r.SessionID]
		if !ok {
			continue // a session row is missing (should not happen); counted in the totals
		}
		s := &rep.Sessions[i]
		s.Messages++
		s.Tokens = s.Tokens.Add(m.Tokens)
		s.nano += c
		if !priced {
			s.UnpricedTokens += m.Total()
		}
		if sessModels[i] == nil {
			sessModels[i] = map[string]bool{}
		}
		sessModels[i][m.Model] = true
	}
	for i := range rep.Sessions {
		if sessModels[i] != nil {
			rep.Sessions[i].Models = sortedKeys(sessModels[i])
		}
		rep.Sessions[i].CostMicroUSD = usage.MicroUSD(rep.Sessions[i].nano)
	}
	rep.Totals.Sessions = len(rep.Sessions)
	rep.Totals.CostMicroUSD = usage.MicroUSD(totalNano)
	for _, name := range sortedKeys(models) {
		mu := byModel[name]
		mu.CostMicroUSD = usage.MicroUSD(mu.nano)
		rep.Models = append(rep.Models, *mu)
	}
	return rep, nil
}

// ---- Retention (ADR 0028) ----

// pruneEvents deletes reduced events older than the retention period.
func (e *Engine) pruneEvents(ctx context.Context) {
	if e.opts.EventRetention <= 0 {
		return
	}
	n, err := e.q.DeleteEventsBefore(ctx, now().Add(-e.opts.EventRetention))
	if err != nil {
		e.log.Warn("event retention", "err", err)
		return
	}
	if n > 0 {
		e.log.Info("old events deleted", "count", n, "older_than_days", int(e.opts.EventRetention.Hours()/24))
	}
}

// retentionLoop prunes events at startup and once a day until ctx ends.
func (e *Engine) retentionLoop(ctx context.Context) {
	defer e.wg.Done()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, time.Minute)
		e.pruneEvents(pctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
