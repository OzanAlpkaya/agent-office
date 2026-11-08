package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// The assistant and project memory (ADR 0034). The facts of a briefing are
// computed here, so counts and costs are exact; the assistant words them.
// Memory proposed by the assistant reaches other agents only after the user
// accepted it.

// Memory kinds and statuses.
const (
	MemoryDecision   = "decision"
	MemoryConvention = "convention"
	MemoryLesson     = "lesson"
	MemoryProposed   = "proposed"
	MemoryAccepted   = "accepted"
)

// Memory limits.
const (
	maxMemoryBody     = 2000
	maxMemoryProposed = 50   // open proposals; the user decides the rest first
	maxMemoryPrompt   = 6000 // characters of accepted memory in a prompt
)

// MemoryEntry is one project memory entry. A proposed entry's text was
// written by the assistant: render it as text only.
type MemoryEntry struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	Source    string    `json:"source,omitempty"` // the proposing agent; empty: the user
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func memoryFromRow(r db.Memory) MemoryEntry {
	return MemoryEntry{ID: r.ID, Kind: r.Kind, Title: r.Title, Body: r.Body, Status: r.Status, Source: r.Source,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func validMemoryKind(k string) bool {
	return k == MemoryDecision || k == MemoryConvention || k == MemoryLesson
}

// MemoryInput adds or changes a memory entry (the user's).
type MemoryInput struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status,omitempty"` // PATCH: "accepted" accepts a proposal
}

func (e *Engine) normalizeMemory(in MemoryInput) (MemoryInput, error) {
	if !validMemoryKind(in.Kind) {
		return in, invalid("kind must be decision, convention or lesson")
	}
	var err error
	if in.Title, err = normalizeTitle(in.Title); err != nil {
		return in, err
	}
	if in.Body, err = normalizeText(in.Body, maxMemoryBody, "body"); err != nil {
		return in, err
	}
	return in, nil
}

func (e *Engine) publishMemory(r db.Memory) {
	v := memoryFromRow(r)
	e.publish(Update{Type: "memory", Memory: &v})
}

// Memory lists the project memory, newest first.
func (e *Engine) Memory(ctx context.Context) ([]MemoryEntry, error) {
	rows, err := e.q.ListMemory(ctx, e.project.ID)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, memoryFromRow(r))
	}
	return out, nil
}

// AddMemory stores the user's own entry, accepted.
func (e *Engine) AddMemory(ctx context.Context, in MemoryInput) (MemoryEntry, error) {
	in, err := e.normalizeMemory(in)
	if err != nil {
		return MemoryEntry{}, err
	}
	row, err := e.q.InsertMemory(ctx, db.InsertMemoryParams{ProjectID: e.project.ID, Kind: in.Kind, Title: in.Title,
		Body: in.Body, Status: MemoryAccepted, Source: "", CreatedAt: now(), UpdatedAt: now()})
	if err != nil {
		return MemoryEntry{}, err
	}
	e.publishMemory(row)
	return memoryFromRow(row), nil
}

func (e *Engine) getMemory(ctx context.Context, id int64) (db.Memory, error) {
	r, err := e.q.GetMemory(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.ProjectID != e.project.ID) {
		return db.Memory{}, ErrNotFound
	}
	return r, err
}

// UpdateMemory edits an entry; fields left empty keep their value, and status
// "accepted" accepts a proposal.
func (e *Engine) UpdateMemory(ctx context.Context, id int64, p MemoryInput) (MemoryEntry, error) {
	cur, err := e.getMemory(ctx, id)
	if err != nil {
		return MemoryEntry{}, err
	}
	in := MemoryInput{Kind: cur.Kind, Title: cur.Title, Body: cur.Body}
	if p.Kind != "" {
		in.Kind = p.Kind
	}
	if p.Title != "" {
		in.Title = p.Title
	}
	if p.Body != "" {
		in.Body = p.Body
	}
	if in, err = e.normalizeMemory(in); err != nil {
		return MemoryEntry{}, err
	}
	status := cur.Status
	switch p.Status {
	case "":
	case MemoryAccepted:
		status = MemoryAccepted
	default:
		return MemoryEntry{}, invalid("status can only become accepted; delete an entry to drop it")
	}
	row, err := e.q.UpdateMemory(ctx, db.UpdateMemoryParams{Kind: in.Kind, Title: in.Title, Body: in.Body, Status: status,
		UpdatedAt: now(), ID: id})
	if err != nil {
		return MemoryEntry{}, err
	}
	e.publishMemory(row)
	return memoryFromRow(row), nil
}

// DeleteMemory removes an entry.
func (e *Engine) DeleteMemory(ctx context.Context, id int64) error {
	if _, err := e.getMemory(ctx, id); err != nil {
		return err
	}
	if _, err := e.q.DeleteMemory(ctx, id); err != nil {
		return err
	}
	e.publish(Update{Type: "memory_deleted", ID: strconv.FormatInt(id, 10)})
	return nil
}

// memorySection is the accepted project memory every agent starts with,
// newest first, within maxMemoryPrompt characters (ADR 0034).
func (e *Engine) memorySection(ctx context.Context) string {
	rows, err := e.q.ListAcceptedMemory(ctx, e.project.ID)
	if err != nil || len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nProject memory (notes the user approved for every agent; follow them unless your task says otherwise):")
	n, left := 0, 0
	for _, r := range rows {
		line := fmt.Sprintf("\n- [%s] %s: %s", r.Kind, r.Title, strings.ReplaceAll(r.Body, "\n", " "))
		if b.Len()+len(line) > maxMemoryPrompt {
			left++
			continue
		}
		b.WriteString(line)
		n++
	}
	if n == 0 {
		return ""
	}
	if left > 0 {
		fmt.Fprintf(&b, "\n(%d older note(s) left out for length.)", left)
	}
	return b.String()
}

// asAssistant checks that caller is the running assistant.
func (e *Engine) asAssistant(caller, tool string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.callerLocked(caller)
	if err != nil {
		return err
	}
	if a.info.Kind != KindAssistant {
		return forbidden("only the assistant uses %s", tool)
	}
	return nil
}

func (t team) ListMemory(ctx context.Context, caller string) (mcp.ListMemoryOutput, error) {
	if err := t.e.asAssistant(caller, mcp.ToolListMemory); err != nil {
		return mcp.ListMemoryOutput{}, userErr(err)
	}
	entries, err := t.e.Memory(ctx)
	if err != nil {
		return mcp.ListMemoryOutput{}, err
	}
	out := mcp.ListMemoryOutput{Entries: []mcp.MemoryEntry{}}
	for _, m := range entries {
		out.Entries = append(out.Entries, mcp.MemoryEntry{ID: m.ID, Kind: m.Kind, Title: m.Title, Body: m.Body, Status: m.Status})
	}
	return out, nil
}

func (t team) ProposeMemory(ctx context.Context, caller string, in mcp.ProposeMemoryInput) (mcp.ProposeMemoryOutput, error) {
	e := t.e
	if err := e.asAssistant(caller, mcp.ToolProposeMemory); err != nil {
		return mcp.ProposeMemoryOutput{}, userErr(err)
	}
	mi, err := e.normalizeMemory(MemoryInput{Kind: in.Kind, Title: in.Title, Body: in.Body})
	if err != nil {
		return mcp.ProposeMemoryOutput{}, userErr(err)
	}
	if n, err := e.q.CountProposedMemory(ctx, e.project.ID); err != nil {
		return mcp.ProposeMemoryOutput{}, err
	} else if n >= maxMemoryProposed {
		return mcp.ProposeMemoryOutput{}, userErr(fmt.Errorf("%w: %d proposals wait for the user; let them decide first",
			ErrLimit, n))
	}
	row, err := e.q.InsertMemory(ctx, db.InsertMemoryParams{ProjectID: e.project.ID, Kind: mi.Kind, Title: mi.Title,
		Body: mi.Body, Status: MemoryProposed, Source: caller, CreatedAt: now(), UpdatedAt: now()})
	if err != nil {
		return mcp.ProposeMemoryOutput{}, err
	}
	e.publishMemory(row)
	e.log.Info("memory proposed", "agent_id", caller, "memory_id", row.ID, "kind", row.Kind)
	return mcp.ProposeMemoryOutput{ID: row.ID, Status: MemoryProposed,
		Next: "Proposed. The user accepts, edits or deletes it in agent-office; no agent sees it before that."}, nil
}

func (t team) GetBriefing(ctx context.Context, caller string) (mcp.Briefing, error) {
	if err := t.e.asAssistant(caller, mcp.ToolGetBriefing); err != nil {
		return mcp.Briefing{}, userErr(err)
	}
	return t.e.Briefing(ctx)
}

// Briefing computes the facts of a briefing from the database (ADR 0034).
func (e *Engine) Briefing(ctx context.Context) (mcp.Briefing, error) {
	b := mcp.Briefing{At: now().Format(time.RFC3339), Goals: []mcp.GoalBrief{}, WaitingOnYou: []string{},
		Blocked: []string{}, Running: []mcp.AgentBrief{}}
	tasks, err := e.ListTasks(ctx)
	if err != nil {
		return b, err
	}
	title := map[string]string{}
	for _, t := range tasks {
		title[t.ID] = t.Title
	}
	specs, err := e.Specs(ctx)
	if err != nil {
		return b, err
	}
	runs, err := e.QARuns(ctx)
	if err != nil {
		return b, err
	}
	e.mu.Lock()
	leadState := map[string]string{}
	for _, id := range e.order {
		a := e.agents[id]
		if !a.running() {
			continue
		}
		v := a.view()
		b.Running = append(b.Running, mcp.AgentBrief{ID: v.ID, Role: v.Role, State: v.State, Task: title[v.TaskID]})
		if v.Kind == KindLead {
			leadState[v.TaskID] = v.State
		}
	}
	e.mu.Unlock()
	for _, t := range tasks {
		if t.ParentTaskID != "" {
			continue
		}
		g := mcp.GoalBrief{ID: t.ID, Title: t.Title, Status: t.Status, SubTasks: map[string]int{}, Lead: leadState[t.ID]}
		for _, c := range tasks {
			if c.ParentTaskID == t.ID {
				g.SubTasks[c.Status]++
			}
		}
		for _, s := range specs { // newest version first per goal
			if s.GoalID == t.ID {
				switch s.Status {
				case SpecApproved:
					g.Plan = fmt.Sprintf("version %d approved", s.Version)
				case SpecProposed:
					g.Plan = fmt.Sprintf("version %d waiting for approval", s.Version)
					b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Approve or edit plan version %d for goal %q.",
						s.Version, t.Title))
				}
				if g.Plan != "" {
					break
				}
			}
		}
		for _, r := range runs { // newest first
			if r.GoalID == t.ID {
				g.QA = r.Status
				break
			}
		}
		if len(g.SubTasks) > 0 || t.Status != StatusDone {
			b.Goals = append(b.Goals, g)
		}
		if t.Status == StatusReview && len(g.SubTasks) > 0 {
			b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Goal %q is reported: review it and mark it done, or "+
				"give its Tech Lead follow-up work.", t.Title))
		}
	}
	for _, t := range tasks {
		switch {
		case t.Status == StatusBlocked:
			b.Blocked = append(b.Blocked, fmt.Sprintf("Task %q is marked blocked.", t.Title))
		case t.Status == StatusTodo && len(t.DependsOn) > 0:
			var waiting []string
			for _, d := range t.DependsOn {
				for _, x := range tasks {
					if x.ID == d && x.Status != StatusReview && x.Status != StatusDone {
						waiting = append(waiting, fmt.Sprintf("%q (%s)", x.Title, x.Status))
					}
				}
			}
			if len(waiting) > 0 {
				b.Blocked = append(b.Blocked, fmt.Sprintf("Sub-task %q waits for %s.", t.Title, strings.Join(waiting, ", ")))
			}
		}
		if len(t.Conflicts) > 0 {
			b.Blocked = append(b.Blocked, fmt.Sprintf("Task %q has conflicts %s.", t.Title, describeConflicts(t.Conflicts)))
		}
	}
	if items, err := e.Inbox(ctx); err == nil {
		for _, it := range items {
			if it.AgentID != "" && it.AnsweredAt == nil && (it.Kind == InboxQuestion || it.Kind == InboxBlocked) {
				b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Answer the %s %s (its words, quoted): %q", it.Role,
					agentName(it.AgentID), truncateRunes(it.Body, 200)))
			}
		}
	}
	if reqs, err := e.AccessRequests(ctx); err == nil {
		for _, r := range reqs {
			if r.Status == "pending" {
				b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Decide the access request of %s for %s %s.",
					agentName(r.AgentID), r.Kind, r.Value))
			}
		}
	}
	if e.gh != nil {
		if prs, err := e.PullRequests(ctx); err == nil {
			for _, p := range prs {
				switch {
				case len(p.Flagged) > 0:
					b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Look at %d flagged commit(s) on branch %s before "+
						"it is pushed.", len(p.Flagged), p.Branch))
				case p.State == PROpen:
					b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("Pull request #%d (%s) is open; merging is yours.",
						p.Number, title[p.TaskID]))
				}
			}
		}
	}
	if rep, err := e.Usage(ctx); err == nil {
		b.Cost.TotalMicroUSD, b.Cost.Sessions = rep.Totals.CostMicroUSD, len(rep.Sessions)
		day := now().Truncate(24 * time.Hour)
		for _, s := range rep.Sessions {
			if !s.StartedAt.Before(day) {
				b.Cost.TodayMicroUSD += s.CostMicroUSD
			}
		}
		b.Cost.GoalBudget = e.opts.Budgets.GoalMicroUSD
	}
	if mem, err := e.Memory(ctx); err == nil {
		for _, m := range mem {
			if m.Status == MemoryAccepted {
				b.Memory.Accepted++
			} else {
				b.Memory.Proposed++
			}
		}
		if b.Memory.Proposed > 0 {
			b.WaitingOnYou = append(b.WaitingOnYou, fmt.Sprintf("%d project memory proposal(s) wait for you.",
				b.Memory.Proposed))
		}
	}
	slices.Sort(b.Blocked)
	return b, nil
}

// AskBriefing asks the assistant for a briefing: a message to the running
// one, or a new assistant, whose first prompt asks for one.
func (e *Engine) AskBriefing(ctx context.Context) (AgentInfo, error) {
	e.mu.Lock()
	a := e.runningKindLocked(KindAssistant)
	var id string
	if a != nil {
		id = a.info.ID
	}
	e.mu.Unlock()
	if id != "" {
		if _, err := e.MessageAgent(ctx, id, "Please give me a new briefing."); err != nil {
			return AgentInfo{}, err
		}
		return e.Get(id)
	}
	return e.Start(ctx, StartRequest{Role: RoleAssistant})
}
