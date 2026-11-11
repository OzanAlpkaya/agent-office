package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// The stall watchdog (ADR 0052) is daemon code, not an agent: a goal in
// progress whose Tech Lead runs, whose agents are all idle or exited, with no
// message on its way to a running agent and nothing waiting on the user, is
// stuck: everyone waits for someone else. After StallAfter the lead gets a
// status summary with what it can do. Nudges are bounded: each one waits
// twice as long as the one before, at most maxNudges while nothing changes in
// the goal, then the user's inbox says the team is stuck.

// DefaultStallAfter is the watchdog's wait when Options.StallAfter is 0.
const DefaultStallAfter = 3 * time.Minute

// maxNudges is how often the watchdog wakes a lead while nothing changes.
const maxNudges = 3

// stallState is the watchdog's view of one goal.
type stallState struct {
	fingerprint string    // the goal's tasks and agents when nudges was last reset
	idleSince   time.Time // zero while something moves or waits on the user
	nudges      int
	userTold    bool
}

func (e *Engine) stallAfter() time.Duration {
	switch d := e.opts.StallAfter; {
	case d < 0:
		return 0
	case d == 0:
		return DefaultStallAfter
	default:
		return d
	}
}

// watchdogLoop checks every goal a few times per StallAfter.
func (e *Engine) watchdogLoop(ctx context.Context) {
	defer e.wg.Done()
	after := e.stallAfter()
	if after <= 0 {
		return
	}
	tick := min(max(after/6, 50*time.Millisecond), 15*time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.checkStalls(ctx, after)
		}
	}
}

// goalView is what the watchdog gathers about one goal.
type goalView struct {
	goal     db.Task
	children []db.Task
	lead     *agentProc
	agents   []*agentProc // every agent of the goal, the lead included, in start order
}

// checkStalls runs one watchdog round.
func (e *Engine) checkStalls(ctx context.Context, after time.Duration) {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	var leads []*agentProc
	for _, id := range e.order {
		if a := e.agents[id]; a.info.Kind == KindLead && a.running() && a.info.TaskID != "" {
			leads = append(leads, a)
		}
	}
	e.mu.Unlock()
	seen := map[string]bool{}
	for _, lead := range leads {
		goalID := lead.info.TaskID
		seen[goalID] = true
		g, ok := e.goalView(ctx, goalID, lead)
		if !ok || g.goal.Status != StatusInProgress {
			e.forgetStall(goalID)
			continue
		}
		moving, fp := e.goalMoving(ctx, g)
		if moving || e.waitsOnUser(ctx, g) != "" {
			e.mu.Lock()
			if st := e.stalls[goalID]; st != nil {
				st.idleSince = time.Time{}
			}
			e.mu.Unlock()
			continue
		}
		e.mu.Lock()
		st := e.stalls[goalID]
		if st == nil {
			st = &stallState{}
			e.stalls[goalID] = st
		}
		if st.fingerprint != fp { // something changed since the last nudge: a new stall
			st.fingerprint, st.nudges, st.userTold = fp, 0, false
		}
		t := time.Now()
		if st.idleSince.IsZero() {
			st.idleSince = t
		}
		wait := after << st.nudges
		due := t.Sub(st.idleSince) >= wait
		nudge, tell := due && st.nudges < maxNudges, due && st.nudges >= maxNudges && !st.userTold
		idle := t.Sub(st.idleSince).Round(time.Second)
		if nudge {
			st.nudges++
			st.idleSince = t
		}
		if tell {
			st.userTold = true
		}
		e.mu.Unlock()
		switch {
		case nudge:
			body := e.stallSummary(ctx, g, idle)
			if _, err := e.sendMessage(ctx, "", lead.info.ID, truncateRunes(body, MaxMessageLen), false); err != nil {
				e.log.Warn("stall watchdog: wake the lead", "task_id", goalID, "err", err)
			} else {
				e.log.Info("stall watchdog woke the lead", "task_id", goalID, "agent_id", lead.info.ID, "idle", idle)
			}
		case tell:
			body := fmt.Sprintf("The team of goal %q has not moved for a while: its Tech Lead %s was woken %d times with "+
				"the goal's status and nothing changed. Look at its terminal, or give it a hint.", g.goal.Title,
				teamLabel(lead.info.Name, lead.info.ID), maxNudges)
			if _, err := e.addInbox(ctx, "", goalID, goalID, "", InboxInfo, body); err != nil {
				e.log.Warn("stall watchdog: tell the user", "task_id", goalID, "err", err)
			}
		}
	}
	e.mu.Lock()
	for id := range e.stalls {
		if !seen[id] {
			delete(e.stalls, id)
		}
	}
	e.mu.Unlock()
}

func (e *Engine) forgetStall(goalID string) {
	e.mu.Lock()
	delete(e.stalls, goalID)
	e.mu.Unlock()
}

// goalView loads a goal, its sub-tasks and its agents.
func (e *Engine) goalView(ctx context.Context, goalID string, lead *agentProc) (goalView, bool) {
	goal, err := e.getTask(ctx, goalID)
	if err != nil {
		return goalView{}, false
	}
	children, err := e.q.ListChildTasks(ctx, nullString(goalID))
	if err != nil {
		return goalView{}, false
	}
	sub := map[string]bool{}
	for _, c := range children {
		sub[c.ID] = true
	}
	g := goalView{goal: goal, children: children, lead: lead}
	e.mu.Lock()
	for _, id := range e.order {
		if a := e.agents[id]; a.info.TaskID == goalID || sub[a.info.TaskID] {
			g.agents = append(g.agents, a)
		}
	}
	e.mu.Unlock()
	return g, true
}

// goalMoving reports whether anything in the goal is under way, and a
// fingerprint of its tasks and agents.
func (e *Engine) goalMoving(ctx context.Context, g goalView) (bool, string) {
	var fp []string
	for _, c := range g.children {
		fp = append(fp, c.ID+"="+c.Status)
	}
	e.mu.Lock()
	moving := e.starting > 0
	for _, c := range g.children {
		moving = moving || e.assigning[c.ID]
	}
	var idle []*agentProc
	for _, a := range g.agents {
		fp = append(fp, a.info.ID+"@"+a.info.SessionID+"="+fmt.Sprint(a.running()))
		if a.busy {
			moving = true
		}
		if !a.running() {
			continue
		}
		if a.status.State != hooks.Idle || a.delivering || a.deliverTimer != nil {
			moving = true
		}
		idle = append(idle, a)
	}
	e.mu.Unlock()
	slices.Sort(fp)
	if moving {
		return true, strings.Join(fp, ",")
	}
	for _, a := range idle { // a message on its way to a running agent is movement
		if n, err := e.q.CountPendingMessages(ctx, db.CountPendingMessagesParams{ToAgentID: a.info.ID, CreatedAt: a.since}); err != nil || n > 0 {
			return true, strings.Join(fp, ",")
		}
	}
	return false, strings.Join(fp, ",")
}

// waitsOnUser names what of the goal waits on the user, or "": an unanswered
// question or blocked message, a plan waiting for approval, an access request
// the user decides.
func (e *Engine) waitsOnUser(ctx context.Context, g goalView) string {
	if items, err := e.q.ListInbox(ctx, db.ListInboxParams{ProjectID: e.project.ID, Limit: maxInboxListed}); err == nil {
		for _, it := range items {
			if it.GoalID.String == g.goal.ID && it.AgentID.Valid && !it.AnsweredAt.Valid &&
				(it.Kind == InboxQuestion || it.Kind == InboxBlocked) {
				return "an unanswered " + it.Kind + " message"
			}
		}
	}
	if specs, err := e.Specs(ctx); err == nil {
		for _, s := range specs {
			if s.GoalID == g.goal.ID && s.Status == SpecProposed {
				return "a plan waiting for approval"
			}
		}
	}
	for _, r := range e.pendingAccess(ctx, g) {
		if ar := e.withDecider(ctx, accessFromRow(r)); ar.Decider == DecidedByUser {
			return "an access request"
		}
	}
	return ""
}

func (e *Engine) pendingAccess(ctx context.Context, g goalView) []db.AccessRequest {
	ids := make([]string, 0, len(g.agents))
	for _, a := range g.agents {
		ids = append(ids, a.info.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := e.q.ListPendingAccessForAgents(ctx, ids)
	if err != nil {
		return nil
	}
	return rows
}

// stallSummary is the lead's wake-up: who is idle, the open sub-tasks, the
// undelivered messages and what it can do. Everything in it is agent-office's
// own text; titles are quoted.
func (e *Engine) stallSummary(ctx context.Context, g goalView, idle time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agent-office] Status check: every agent of your goal %q has been idle or exited for %s and nothing "+
		"waits on the user, so the goal is not moving. Decide the next step yourself now.", g.goal.Title, idle)
	running := map[string]*agentProc{}
	e.mu.Lock()
	b.WriteString("\n\nAgents:")
	for _, a := range g.agents {
		task := ""
		if a.info.TaskID != "" && a.info.TaskID != g.goal.ID {
			task = ", sub-task " + a.info.TaskID
		}
		state := string(a.status.State)
		if !a.running() {
			state = exitWords(a.info)
		} else {
			running[a.info.TaskID] = a
		}
		you := ""
		if a == g.lead {
			you = ", you"
		}
		fmt.Fprintf(&b, "\n- %s (%s%s%s): %s", teamLabel(a.info.Name, a.info.ID), a.info.Role, task, you, state)
	}
	e.mu.Unlock()
	var open []string
	for _, c := range g.children {
		switch c.Status {
		case StatusTodo, StatusBlocked:
			what := "assign it with assign_task"
			if unmet, err := e.unmetDependencies(ctx, c); err == nil && len(unmet) > 0 {
				what = "waits for " + strings.Join(unmet, ", ")
			}
			open = append(open, fmt.Sprintf("%s %q is %s: %s", c.ID, c.Title, c.Status, what))
		case StatusInProgress:
			if w := running[c.ID]; w != nil {
				open = append(open, fmt.Sprintf("%s %q is in progress; its worker %s is idle: ask it with send_message",
					c.ID, c.Title, teamLabel(w.info.Name, w.info.ID)))
				continue
			}
			what := fmt.Sprintf("reclaim_task(task_id=%q) and assign it again", c.ID)
			if w := e.lastWorkerOf(c.ID); w != nil {
				what = fmt.Sprintf("resume_agent(agent_id=%q) or reclaim_task(task_id=%q) and assign it again",
					w.info.ID, c.ID)
			}
			open = append(open, fmt.Sprintf("%s %q is in progress with no running worker: %s", c.ID, c.Title, what))
		}
	}
	if len(open) > 0 {
		b.WriteString("\n\nOpen sub-tasks:\n- " + strings.Join(open, "\n- "))
	} else {
		next := "report the goal with report_done"
		if e.opts.QA {
			next = "start_qa (or report_done once QA passed)"
		}
		b.WriteString("\n\nEvery sub-task is reported: " + next + ".")
	}
	ids := make([]string, 0, len(g.agents))
	for _, a := range g.agents {
		ids = append(ids, a.info.ID)
	}
	if msgs, err := e.q.ListUndeliveredMessagesTo(ctx, ids); err == nil && len(msgs) > 0 {
		n := map[string]int{}
		var order []string
		for _, m := range msgs {
			if n[m.ToAgentID] == 0 {
				order = append(order, m.ToAgentID)
			}
			n[m.ToAgentID]++
		}
		var parts []string
		e.mu.Lock()
		for _, id := range order {
			parts = append(parts, fmt.Sprintf("%d to %s (delivered when it is resumed)", n[id], e.labelLocked(id)))
		}
		e.mu.Unlock()
		b.WriteString("\n\nUndelivered messages: " + strings.Join(parts, "; ") + ".")
	}
	var access []string
	for _, r := range e.pendingAccess(ctx, g) {
		access = append(access, fmt.Sprintf("#%d %s", r.ID, r.Value))
	}
	if len(access) > 0 {
		b.WriteString("\n\nAccess requests waiting for your decide_access: " + strings.Join(access, ", ") + ".")
	}
	b.WriteString("\n\nAsk the user (message_user) only for merges, pushes, secrets, stack pack or project setting changes, " +
		"or anything that cannot be undone.")
	return b.String()
}

// lastWorkerOf is the latest worker agent on a task.
func (e *Engine) lastWorkerOf(taskID string) *agentProc {
	e.mu.Lock()
	defer e.mu.Unlock()
	var last *agentProc
	for _, id := range e.order {
		if a := e.agents[id]; a.info.TaskID == taskID && a.info.Kind == KindWorker {
			last = a
		}
	}
	return last
}
