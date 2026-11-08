package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Messages to the user and the escalation chain (ADR 0030). An agent talks
// to the user only when nobody is above it: worker -> Tech Lead -> architect
// -> user. The assistant and the architect talk to the user; a worker without
// a lead, and a lead without an architect, may leave messages too.

// Inbox kinds (inbox.kind).
const (
	InboxQuestion = "question"
	InboxInfo     = "info"
	InboxBlocked  = "blocked"
	InboxBriefing = "briefing"
	InboxPlan     = "plan" // agent-office: a plan waits for approval (ADR 0031)
)

// Limits on messages to the user (ADR 0018's caps, per agent).
const (
	maxInboxPerWindow = 30 // per agent per sendWindow
	maxInboxUnread    = 20 // unread items from one agent
	maxInboxListed    = 200
)

// InboxItem is a message to the user. The body is written by an agent:
// render it as text only.
type InboxItem struct {
	ID         int64      `json:"id"`
	AgentID    string     `json:"agent_id,omitempty"` // empty: agent-office itself
	TaskID     string     `json:"task_id,omitempty"`
	GoalID     string     `json:"goal_id,omitempty"`
	Role       string     `json:"role,omitempty"`
	Kind       string     `json:"kind"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	Reply      string     `json:"reply,omitempty"` // the user's answer
}

func inboxFromRow(r db.Inbox) InboxItem {
	it := InboxItem{ID: r.ID, AgentID: r.AgentID.String, TaskID: r.TaskID.String, GoalID: r.GoalID.String, Role: r.Role,
		Kind: r.Kind, Body: r.Body, CreatedAt: r.CreatedAt.UTC(), Reply: r.Reply}
	if r.ReadAt.Valid {
		t := r.ReadAt.Time.UTC()
		it.ReadAt = &t
	}
	if r.AnsweredAt.Valid {
		t := r.AnsweredAt.Time.UTC()
		it.AnsweredAt = &t
	}
	return it
}

// goalOfTask returns the goal a task belongs to: its parent, or itself.
func goalOfTask(t db.Task) string {
	if t.ParentTaskID.Valid {
		return t.ParentTaskID.String
	}
	return t.ID
}

// hasArchitectLocked reports whether the goal has an architect agent (running
// or not): then the Tech Lead escalates to it instead of the user.
func (e *Engine) hasArchitectLocked(goalID string) bool {
	for _, a := range e.agents {
		if a.info.Kind == KindArchitect && a.info.TaskID == goalID {
			return true
		}
	}
	return false
}

// architectOfGoalLocked returns the goal's most recent architect agent.
func (e *Engine) architectOfGoalLocked(goalID string) *agentProc {
	var last *agentProc
	for _, id := range e.order {
		if a := e.agents[id]; a.info.Kind == KindArchitect && a.info.TaskID == goalID {
			last = a
		}
	}
	return last
}

// mayTellUser decides whether caller may message the user, and if not, who
// it should ask instead (ADR 0030).
func (e *Engine) mayTellUser(ctx context.Context, a *agentProc) error {
	info := a.info
	switch info.Kind {
	case KindArchitect, KindAssistant:
		return nil
	case KindLead:
		def := e.roleOf(info.Role, info.Kind)
		e.mu.Lock()
		arch := e.hasArchitectLocked(info.TaskID)
		e.mu.Unlock()
		if arch && def.ReportsTo != role.ToUser {
			return forbidden(`your goal has an architect: escalate to it with send_message(to="architect"); ` +
				"the architect talks to the user")
		}
		return nil
	case KindWorker:
		if info.TaskID == "" {
			return nil
		}
		task, err := e.getTask(ctx, info.TaskID)
		if err != nil {
			return err
		}
		if !task.ParentTaskID.Valid {
			return nil // a user's own task: the user is the lead
		}
		e.mu.Lock()
		lead := e.leadOfGoalLocked(task.ParentTaskID.String)
		e.mu.Unlock()
		if lead != nil {
			return forbidden(`your task has a lead: ask it with send_message(to="lead"); it escalates if needed`)
		}
		return nil
	}
	return forbidden("a %s does not message the user; put it into your report", info.Kind)
}

func (t team) MessageUser(ctx context.Context, caller string, in mcp.MessageUserInput) (mcp.MessageUserOutput, error) {
	it, err := t.e.messageUser(ctx, caller, in.Kind, in.Body)
	if err != nil {
		return mcp.MessageUserOutput{}, userErr(err)
	}
	next := "Shown to the user in agent-office. End your turn; the user's answer is delivered to you as new input."
	if it.Kind == InboxInfo || it.Kind == InboxBriefing {
		next = "Shown to the user in agent-office. No answer is expected; continue or end your turn."
	}
	return mcp.MessageUserOutput{MessageID: it.ID, Status: "shown", Next: next}, nil
}

func (e *Engine) messageUser(ctx context.Context, caller, kind, body string) (InboxItem, error) {
	switch kind {
	case InboxQuestion, InboxInfo, InboxBlocked, InboxBriefing:
	default:
		return InboxItem{}, invalid("kind must be question, info, blocked or briefing")
	}
	body, err := normalizeText(body, MaxMessageLen, "body")
	if err != nil {
		return InboxItem{}, err
	}
	e.mu.Lock()
	a, err := e.callerLocked(caller)
	e.mu.Unlock()
	if err != nil {
		return InboxItem{}, err
	}
	if err := e.mayTellUser(ctx, a); err != nil {
		return InboxItem{}, err
	}
	n, err := e.q.CountInboxFromSince(ctx, db.CountInboxFromSinceParams{AgentID: nullString(caller),
		CreatedAt: now().Add(-sendWindow)})
	if err != nil {
		return InboxItem{}, err
	}
	if n >= maxInboxPerWindow {
		return InboxItem{}, fmt.Errorf("%w: at most %d messages to the user per %s", ErrLimit, maxInboxPerWindow, sendWindow)
	}
	if n, err = e.q.CountUnreadInboxFrom(ctx, nullString(caller)); err != nil {
		return InboxItem{}, err
	} else if n >= maxInboxUnread {
		return InboxItem{}, fmt.Errorf("%w: the user has %d unread messages from you; wait for them to be read", ErrLimit, n)
	}
	var goal string
	if a.info.TaskID != "" {
		if t, err := e.getTask(ctx, a.info.TaskID); err == nil {
			goal = goalOfTask(t)
		}
	}
	return e.addInbox(ctx, caller, a.info.TaskID, goal, a.info.Role, kind, body)
}

// addInbox stores and publishes an inbox item; agent "" is agent-office.
func (e *Engine) addInbox(ctx context.Context, agent, taskID, goalID, roleName, kind, body string) (InboxItem, error) {
	row, err := e.q.InsertInbox(ctx, db.InsertInboxParams{ProjectID: e.project.ID, AgentID: nullString(agent),
		TaskID: nullString(taskID), GoalID: nullString(goalID), Role: roleName, Kind: kind,
		Body: truncateRunes(body, MaxMessageLen), CreatedAt: now()})
	if err != nil {
		return InboxItem{}, err
	}
	it := inboxFromRow(row)
	e.log.Info("message to the user", "agent_id", agent, "inbox_id", it.ID, "kind", kind, "chars", len([]rune(body)))
	e.publish(Update{Type: "inbox", Inbox: &it})
	return it, nil
}

// Inbox lists the user's messages, newest first.
func (e *Engine) Inbox(ctx context.Context) ([]InboxItem, error) {
	rows, err := e.q.ListInbox(ctx, db.ListInboxParams{ProjectID: e.project.ID, Limit: maxInboxListed})
	if err != nil {
		return nil, err
	}
	out := make([]InboxItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, inboxFromRow(r))
	}
	return out, nil
}

func (e *Engine) getInbox(ctx context.Context, id int64) (db.Inbox, error) {
	r, err := e.q.GetInbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.ProjectID != e.project.ID) {
		return db.Inbox{}, ErrNotFound
	}
	return r, err
}

// MarkInboxRead marks an inbox item read.
func (e *Engine) MarkInboxRead(ctx context.Context, id int64) (InboxItem, error) {
	if _, err := e.getInbox(ctx, id); err != nil {
		return InboxItem{}, err
	}
	row, err := e.q.MarkInboxRead(ctx, db.MarkInboxReadParams{ReadAt: nullTime(now()), ID: id})
	if err != nil {
		return InboxItem{}, err
	}
	it := inboxFromRow(row)
	e.publish(Update{Type: "inbox", Inbox: &it})
	return it, nil
}

// ReplyInbox answers an agent's message: the reply reaches the agent as a
// message from the user (delivered like team messages, ADR 0018) and is kept
// on the item.
func (e *Engine) ReplyInbox(ctx context.Context, id int64, body string) (InboxItem, error) {
	r, err := e.getInbox(ctx, id)
	if err != nil {
		return InboxItem{}, err
	}
	if !r.AgentID.Valid {
		return InboxItem{}, invalid("this message is from agent-office; there is no agent to answer")
	}
	if _, err := e.MessageAgent(ctx, r.AgentID.String, body); err != nil {
		return InboxItem{}, err
	}
	body, _ = normalizeText(body, MaxMessageLen, "body")
	row, err := e.q.AnswerInbox(ctx, db.AnswerInboxParams{Reply: body, AnsweredAt: nullTime(now()),
		ReadAt: nullTime(now()), ID: id})
	if err != nil {
		return InboxItem{}, err
	}
	it := inboxFromRow(row)
	e.publish(Update{Type: "inbox", Inbox: &it})
	return it, nil
}

// MessageAgent sends the user's words to a running agent (ADR 0030): from
// the inbox, the briefing button or a card. They are delivered with the team's
// messages, framed as the user's.
func (e *Engine) MessageAgent(ctx context.Context, id, body string) (Message, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	running := ok && a.running()
	e.mu.Unlock()
	switch {
	case !ok:
		return Message{}, ErrNotFound
	case !running:
		return Message{}, invalid("agent %s is not running; start it again to continue", id)
	}
	return e.sendUserMessage(ctx, id, body)
}

// kindsThatAnswerUser are the kinds the user may write to (everything but
// reviewers, whose only input is the pull request).
var kindsThatAnswerUser = []string{KindArchitect, KindLead, KindWorker, KindQA, KindAssistant}

func (e *Engine) sendUserMessage(ctx context.Context, to, body string) (Message, error) {
	e.mu.Lock()
	kind := ""
	if a, ok := e.agents[to]; ok {
		kind = a.info.Kind
	}
	e.mu.Unlock()
	if !slices.Contains(kindsThatAnswerUser, kind) {
		return Message{}, invalid("a %s does not take messages", kind)
	}
	return e.queueMessage(ctx, "", to, body, false, true)
}

// architectFor resolves a lead's escalation to its goal's architect (ADR
// 0030): the running architect, or one relaunched for it (ADR 0031).
func (e *Engine) architectFor(ctx context.Context, lead, goalID string) (string, error) {
	e.mu.Lock()
	a := e.architectOfGoalLocked(goalID)
	running := a != nil && a.running()
	e.mu.Unlock()
	switch {
	case a == nil:
		return "", invalid("your goal has no architect; if the plan cannot work, tell the user with message_user")
	case running:
		return a.info.ID, nil
	}
	return e.relaunchArchitect(ctx, a.info.ID, lead)
}
