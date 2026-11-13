package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Plan changes by request, and the Architect's exit (Phase 19; ADR 0031
// amended). The user writes what to change; the Architect revises the plan
// and submits a new version, which the user approves as before. An Architect
// whose goal has an approved plan and nothing left to answer ends itself, so
// it does not hold an agent slot.

// maxChangeRequest bounds a change request's text.
const maxChangeRequest = 4000

// architectIdleGrace is how long an idle Architect waits for a message that is
// on its way before it ends (a lead's escalation is delivered after its first
// turn); tests shorten it.
var architectIdleGrace = 3 * time.Second

// RequestSpecChanges is the user's request to change a proposed or approved
// plan version. The Architect gets it as the user's words: a running one as
// a message, an exited one started again with it in its prompt.
func (e *Engine) RequestSpecChanges(ctx context.Context, id int64, text string) (Spec, error) {
	text, err := normalizeText(text, maxChangeRequest, "changes")
	if err != nil {
		return Spec{}, err
	}
	row, goal, err := e.getSpec(ctx, id)
	if err != nil {
		return Spec{}, err
	}
	switch {
	case goal.Status == StatusDone:
		return Spec{}, invalid("the goal is done")
	case row.Status != SpecProposed && row.Status != SpecApproved:
		return Spec{}, invalid("version %d is %s; ask for changes on the current version", row.Version, row.Status)
	}
	updated, err := e.q.RequestSpecChanges(ctx, db.RequestSpecChangesParams{ChangeRequest: text,
		ChangeRequestedAt: nullTime(now()), ID: id})
	if err != nil {
		return Spec{}, invalid("version %d was approved or replaced meanwhile", row.Version)
	}
	s := specFromRow(updated)
	asked := s
	if withdrawn := updated.Status == SpecSuperseded; withdrawn {
		// The version is withdrawn (Phase 20): its approval no longer waits on the
		// user. From the updated row: it may have been approved meanwhile (code review).
		e.closePlanNotices(ctx, goal.ID)
		asked.Status = SpecProposed // the Architect reads the status it was asked about
	}
	if err := e.deliverChangeRequest(ctx, goal, asked, text); err != nil {
		return Spec{}, err
	}
	e.publishSpecs(ctx, goal.ID)
	e.log.Info("plan changes requested", "task_id", goal.ID, "spec_id", id, "version", s.Version, "chars", len(text))
	return s, nil
}

// closePlanNotices marks a goal's unread "plan ready" notices read.
func (e *Engine) closePlanNotices(ctx context.Context, goalID string) {
	rows, err := e.q.MarkGoalPlanNoticesRead(ctx, db.MarkGoalPlanNoticesReadParams{ReadAt: nullTime(now()), GoalID: nullString(goalID)})
	if err != nil {
		e.log.Warn("close plan notices", "task_id", goalID, "err", err)
		return
	}
	for _, r := range rows {
		it := inboxFromRow(r)
		e.publish(Update{Type: "inbox", Inbox: &it})
	}
}

func (e *Engine) deliverChangeRequest(ctx context.Context, goal db.Task, s Spec, text string) error {
	e.mu.Lock()
	arch := e.architectOfGoalLocked(goal.ID)
	var archID string
	running := false
	if arch != nil {
		archID, running = arch.info.ID, arch.running() && !arch.stopAfterTurn
	}
	e.mu.Unlock()
	ask := fmt.Sprintf("The user asks for changes to version %d of the plan (%s):\n\n%s\n\nRevise the plan and submit it "+
		"with submit_plan: it becomes a new version the user approves again; sub-tasks already worked on stay as they are. "+
		"If something is unclear, ask the user with message_user.", s.Version, s.Status, text)
	if running {
		_, err := e.sendUserMessage(ctx, archID, ask)
		return err
	}
	def := e.roleOf(RoleArchitect, KindArchitect)
	prompt := e.expandPrompt(def) + "\n\n" + goalSection(&goal) +
		fmt.Sprintf("\nThe current plan, version %d (%s):\n\n%s\n", s.Version, s.Status, s.Markdown) + "\n" + ask
	if archID != "" {
		if _, err := e.relaunch(ctx, archID, prompt, nil); err != nil {
			return fmt.Errorf("start the architect again: %w", err)
		}
		e.log.Info("architect relaunched for a change request", "agent_id", archID, "task_id", goal.ID)
		return nil
	}
	a, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleArchitect, prompt: prompt})
	if err != nil {
		return fmt.Errorf("start an architect: %w", err)
	}
	e.log.Info("architect started for a change request", "agent_id", a.ID, "task_id", goal.ID)
	return nil
}

// maybeEndArchitectLocked ends an idle Architect whose work is done: its
// goal has an approved plan, no proposed version, and nothing waits for or
// from it (no message on its way, no question to the user). It checks again
// after architectIdleGrace, so a message delivered after the turn wins.
func (e *Engine) maybeEndArchitectLocked(a *agentProc) {
	if a.info.Kind != KindArchitect || a.endCheck || a.netWaiting {
		return // a turn cut off by the network is resumed, not ended (code review)
	}
	a.endCheck = true
	id, goalID := a.info.ID, a.info.TaskID
	e.wg.Add(1)
	time.AfterFunc(architectIdleGrace, func() {
		defer e.wg.Done()
		e.mu.Lock()
		a.endCheck = false
		idle := e.agents[id] == a && a.running() && !a.stopAfterTurn && a.status.State == hooks.Idle &&
			a.deliverTimer == nil && !a.delivering
		since := a.since
		e.mu.Unlock()
		if !idle || e.shuttingDown() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if why := e.architectBusy(ctx, id, goalID, since); why != "" {
			return
		}
		e.log.Info("architect ends: its plan is approved and nothing waits for it", "agent_id", id, "task_id", goalID)
		_ = e.stop(id, ExitPlanApproved)
	})
}

// architectBusy says why an Architect still has work, or "" when it has none.
func (e *Engine) architectBusy(ctx context.Context, id, goalID string, since time.Time) string {
	if _, err := e.q.LatestApprovedSpec(ctx, goalID); err != nil {
		return "no approved plan"
	}
	specs, err := e.q.ListGoalSpecs(ctx, goalID)
	if err != nil {
		return "specs: " + err.Error()
	}
	for _, s := range specs {
		if s.Status == SpecProposed {
			return "a proposed version waits for the user"
		}
	}
	if changeRequested(specs) {
		return "a change request waits for a new version"
	}
	if n, err := e.q.CountPendingMessages(ctx, db.CountPendingMessagesParams{ToAgentID: id, CreatedAt: since}); err != nil || n > 0 {
		return "a message waits for it"
	}
	if n, err := e.q.CountOpenQuestionsFrom(ctx, nullString(id)); err != nil || n > 0 {
		return "it asked the user a question"
	}
	return ""
}

// architectRestorable reports whether a restore resumes an Architect: not
// when its goal has an approved plan and no proposed version (ADR 0031).
func (e *Engine) architectRestorable(ctx context.Context, goalID string) bool {
	if _, err := e.q.LatestApprovedSpec(ctx, goalID); err != nil {
		return true
	}
	specs, err := e.q.ListGoalSpecs(ctx, goalID)
	if err != nil {
		return true
	}
	for _, s := range specs {
		if s.Status == SpecProposed {
			return true
		}
	}
	return changeRequested(specs)
}

// changeRequested reports whether the newest version of a goal's plan has a
// change request the Architect has not answered with a newer version.
func changeRequested(specs []db.Spec) bool {
	var newest *db.Spec
	for i := range specs {
		if newest == nil || specs[i].Version > newest.Version {
			newest = &specs[i]
		}
	}
	// A proposed version with a change request is withdrawn (superseded) until
	// the Architect submits the next one (Phase 20).
	return newest != nil && newest.ChangeRequest != ""
}
