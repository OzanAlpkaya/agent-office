package mcp

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP server's name in an agent's --mcp-config. Claude Code
// shows its tools as mcp__agent-office__<tool>.
const ServerName = "agent-office"

// SocketEnv names the per-agent MCP socket for `agent-office mcp`.
const SocketEnv = "AGENT_OFFICE_MCP_SOCKET"

// Tool names.
const (
	ToolCreateTask     = "create_task"
	ToolAssignTask     = "assign_task"
	ToolListTasks      = "list_tasks"
	ToolSendMessage    = "send_message"
	ToolReportDone     = "report_done"
	ToolRequestAccess  = "request_access"
	ToolSubmitReview   = "submit_review"
	ToolReportNoChange = "report_no_change" // a follow-up round that needs no change (ADR 0022)
	ToolMessageUser    = "message_user"     // a message to the human (ADR 0030)
	ToolSubmitPlan     = "submit_plan"      // the architect's plan (ADR 0031)
	ToolStartQA        = "start_qa"         // the Tech Lead starts QA on the goal (ADR 0033)
	ToolReportQA       = "report_qa"        // QA's verdict and bugs (ADR 0033)
	ToolGetBriefing    = "get_briefing"     // the assistant's facts (ADR 0034)
	ToolListMemory     = "list_memory"      // project memory (ADR 0034)
	ToolProposeMemory  = "propose_memory"   // a memory entry for the user to accept (ADR 0034)
)

// Team is what the tools call. The engine implements it; caller is the agent
// the MCP socket belongs to, never a value from the tool arguments. Every
// method authorizes the caller and validates its input itself (ADR 0017).
type Team interface {
	CreateTask(ctx context.Context, caller string, in CreateTaskInput) (TaskInfo, error)
	AssignTask(ctx context.Context, caller string, in AssignTaskInput) (AssignTaskOutput, error)
	ListTasks(ctx context.Context, caller string) (ListTasksOutput, error)
	SendMessage(ctx context.Context, caller string, in SendMessageInput) (SendMessageOutput, error)
	ReportDone(ctx context.Context, caller string, in ReportDoneInput) (ReportDoneOutput, error)
	ReportNoChange(ctx context.Context, caller string, in ReportNoChangeInput) (ReportNoChangeOutput, error)
	RequestAccess(ctx context.Context, caller string, in RequestAccessInput) (RequestAccessOutput, error)
	SubmitReview(ctx context.Context, caller string, in SubmitReviewInput) (SubmitReviewOutput, error)
	MessageUser(ctx context.Context, caller string, in MessageUserInput) (MessageUserOutput, error)
	SubmitPlan(ctx context.Context, caller string, in SubmitPlanInput) (SubmitPlanOutput, error)
	StartQA(ctx context.Context, caller string) (StartQAOutput, error)
	ReportQA(ctx context.Context, caller string, in ReportQAInput) (ReportQAOutput, error)
	GetBriefing(ctx context.Context, caller string) (Briefing, error)
	ListMemory(ctx context.Context, caller string) (ListMemoryOutput, error)
	ProposeMemory(ctx context.Context, caller string, in ProposeMemoryInput) (ProposeMemoryOutput, error)
}

// Briefing is get_briefing's result: facts computed by agent-office (ADR
// 0034). Text quoted from agents (questions) is marked as theirs.
type Briefing struct {
	At           string       `json:"at"` // UTC, RFC 3339
	Goals        []GoalBrief  `json:"goals"`
	WaitingOnYou []string     `json:"waiting_on_user"` // decisions and answers the user owes, one line each
	Blocked      []string     `json:"blocked"`         // tasks that cannot move, and why
	Running      []AgentBrief `json:"running"`
	Cost         CostBrief    `json:"cost"`
	Memory       MemoryCounts `json:"memory"`
}

// GoalBrief is one goal in a briefing.
type GoalBrief struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   string         `json:"status"`
	SubTasks map[string]int `json:"sub_tasks"`      // by status
	Plan     string         `json:"plan,omitempty"` // e.g. "version 2 approved", "version 1 waiting for approval"
	QA       string         `json:"qa,omitempty"`   // the last QA run: passed, failed, running, error
	Lead     string         `json:"lead,omitempty"` // the Tech Lead's state
}

// AgentBrief is a running agent in a briefing.
type AgentBrief struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	State string `json:"state"`
	Task  string `json:"task,omitempty"`
}

// CostBrief is the estimated spend (micro-USD, ADR 0026).
type CostBrief struct {
	TotalMicroUSD int64 `json:"total_micro_usd"`
	TodayMicroUSD int64 `json:"today_micro_usd"` // sessions started today (UTC)
	Sessions      int   `json:"sessions"`
	GoalBudget    int64 `json:"goal_budget_micro_usd,omitempty"`
}

// MemoryCounts counts project memory entries.
type MemoryCounts struct {
	Accepted int `json:"accepted"`
	Proposed int `json:"proposed"`
}

// MemoryEntry is a project memory entry as an agent sees it.
type MemoryEntry struct {
	ID           int64    `json:"id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Status       string   `json:"status"`          // accepted (agents get it) or proposed (waits for the user; assistant only)
	Roles        []string `json:"roles,omitempty"` // only for these roles; empty: every role
	Paths        []string `json:"paths,omitempty"` // only for tasks touching these files; empty: every task
	AppliesToYou bool     `json:"applies_to_you"`  // its scope includes the caller (ADR 0037)
}

// ListMemoryOutput is list_memory's result.
type ListMemoryOutput struct {
	Entries []MemoryEntry `json:"entries"`
}

// ProposeMemoryInput is propose_memory's input.
type ProposeMemoryInput struct {
	Kind  string   `json:"kind" jsonschema:"decision (something decided and why), convention (how things are done here) or lesson (something learned the hard way)."`
	Title string   `json:"title" jsonschema:"Short title (1-200 characters)."`
	Body  string   `json:"body" jsonschema:"A few sentences every agent should know (1-2000 characters)."`
	Roles []string `json:"roles,omitempty" jsonschema:"Optional: only agents of these role names get it (e.g. worker, qa); empty for every role."`
	Paths []string `json:"paths,omitempty" jsonschema:"Optional: only agents whose task touches these files get it (repository-relative paths, directories ending in /, globs); empty for every task."`
}

// ProposeMemoryOutput is propose_memory's result.
type ProposeMemoryOutput struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Next   string `json:"next"`
}

// StartQAOutput is start_qa's result (ADR 0033).
type StartQAOutput struct {
	RunID    int64    `json:"run_id"`
	AgentID  string   `json:"agent_id"`
	Branch   string   `json:"branch"`   // QA's integration branch
	Branches []string `json:"branches"` // the sub-task branches it merges
	Next     string   `json:"next"`
}

// QABug is one problem QA found.
type QABug struct {
	Title       string `json:"title" jsonschema:"Short title of the bug (1-200 characters)."`
	Description string `json:"description" jsonschema:"What happens, how to reproduce it, what should happen (at most 4000 characters)."`
	TaskID      string `json:"task_id,omitempty" jsonschema:"The sub-task it belongs to, if known."`
}

// ReportQAInput is report_qa's input.
type ReportQAInput struct {
	Verdict string  `json:"verdict" jsonschema:"pass (the tests pass and every acceptance criterion holds) or fail."`
	Summary string  `json:"summary" jsonschema:"What you merged, ran and checked, and the result (1-4000 characters)."`
	Bugs    []QABug `json:"bugs,omitempty" jsonschema:"One entry per problem (at most 10); each becomes a sub-task for the Tech Lead."`
}

// ReportQAOutput is report_qa's result.
type ReportQAOutput struct {
	RunID    int64    `json:"run_id"`
	Status   string   `json:"status"`
	BugTasks []string `json:"bug_tasks"`
	Next     string   `json:"next"`
}

// PlanSubTask is one sub-task of an architect's plan (ADR 0031).
type PlanSubTask struct {
	Key         string   `json:"key" jsonschema:"Short id of the sub-task, used in depends_on: lower-case letters, digits and -, e.g. api."`
	Title       string   `json:"title" jsonschema:"Short title (1-200 characters)."`
	Description string   `json:"description" jsonschema:"What the worker must do, where, and how to check it (at most 20000 characters)."`
	Role        string   `json:"role,omitempty" jsonschema:"The worker role that builds it, e.g. docs or security; empty: worker."`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"Keys of the sub-tasks whose work this one needs; it starts after them, from their work."`
	Files       []string `json:"files,omitempty" jsonschema:"Files it changes: repository-relative paths, directories ending in /, or globs such as internal/api/*.go."`
	Acceptance  []string `json:"acceptance,omitempty" jsonschema:"Acceptance criteria a test can check, one per item."`
}

// SubmitPlanInput is submit_plan's input: the plan for the goal (ADR 0031).
type SubmitPlanInput struct {
	Summary  string        `json:"summary" jsonschema:"The goal as agreed with the user, in two or three sentences (1-2000 characters)."`
	Design   string        `json:"design" jsonschema:"How it will work, in Markdown: components, data, interfaces, the reasons for the main choices (at most 20000 characters)."`
	SubTasks []PlanSubTask `json:"sub_tasks" jsonschema:"The sub-tasks (1-20), each done by one worker on its own branch."`
	Risks    string        `json:"risks,omitempty" jsonschema:"What could go wrong and how the plan deals with it (at most 4000 characters)."`
}

// SubmitPlanOutput is submit_plan's result.
type SubmitPlanOutput struct {
	SpecID  int64  `json:"spec_id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
	Next    string `json:"next"`
}

// MessageUserInput is message_user's input (ADR 0030).
type MessageUserInput struct {
	Kind string `json:"kind" jsonschema:"question (you need an answer), blocked (you cannot go on without the user), info (no answer needed) or briefing (the assistant's summary)."`
	Body string `json:"body" jsonschema:"The message (1-4000 characters). Put every question into one message."`
}

// MessageUserOutput is message_user's result.
type MessageUserOutput struct {
	MessageID int64  `json:"message_id"`
	Status    string `json:"status"`
	Next      string `json:"next"`
}

// CreateTaskInput is create_task's input.
type CreateTaskInput struct {
	Title       string   `json:"title" jsonschema:"Short title of the sub-task (1-200 characters)."`
	Description string   `json:"description,omitempty" jsonschema:"What the worker must do and how to tell it is done (at most 20000 characters)."`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"Ids of sub-tasks of your goal whose work this one needs; it starts only after they are reported, from their work."`
	Files       []string `json:"files,omitempty" jsonschema:"Files it changes: repository-relative paths, directories ending in /, or globs. Sub-tasks with overlapping files cannot run at the same time unless one depends on the other."`
	Acceptance  []string `json:"acceptance,omitempty" jsonschema:"Acceptance criteria QA tests, one per item."`
	Role        string   `json:"role,omitempty" jsonschema:"The worker role that builds it (agent-office lists them in list_tasks); empty: worker."`
}

// AgentRef is an agent as the tools show it.
type AgentRef struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // the behaviour: lead, worker, qa, ...
	Role   string `json:"role"` // the role's name, e.g. tech-lead, docs
	State  string `json:"state"`
	Branch string `json:"branch,omitempty"`
}

// TaskInfo is a task as the tools show it.
type TaskInfo struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Description  string         `json:"description,omitempty"`
	Status       string         `json:"status"`
	ParentTaskID string         `json:"parent_task_id,omitempty"`
	DependsOn    []string       `json:"depends_on,omitempty"`
	Files        []string       `json:"files,omitempty"`
	Acceptance   []string       `json:"acceptance,omitempty"`
	Role         string         `json:"role,omitempty"`
	Conflicts    []ConflictInfo `json:"conflicts,omitempty"`
	Agents       []AgentRef     `json:"agents,omitempty"`
}

// ConflictInfo is a conflict found when a sub-task was reported (ADR 0032).
type ConflictInfo struct {
	With   string   `json:"with,omitempty"` // the other sub-task; empty: the base branch
	Branch string   `json:"branch"`
	Files  []string `json:"files"`
}

// AssignTaskInput is assign_task's input.
type AssignTaskInput struct {
	TaskID string `json:"task_id" jsonschema:"Id of a sub-task of your goal, in status todo or blocked."`
	Role   string `json:"role,omitempty" jsonschema:"The worker role to start (see worker_roles in list_tasks); empty: the sub-task's role, else worker."`
}

// AssignTaskOutput is assign_task's result.
type AssignTaskOutput struct {
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id"`
	Branch  string `json:"branch"`
}

// ListTasksOutput is list_tasks's result: for a lead or an architect its goal
// and sub-tasks, for a worker its own task and its lead.
type ListTasksOutput struct {
	You         AgentRef   `json:"you"`
	Goal        *TaskInfo  `json:"goal,omitempty"`
	Tasks       []TaskInfo `json:"tasks"`
	Lead        *AgentRef  `json:"lead,omitempty"`
	WorkerRoles []string   `json:"worker_roles,omitempty"` // leads: the roles assign_task and create_task accept
}

// SendMessageInput is send_message's input.
type SendMessageInput struct {
	To   string `json:"to" jsonschema:"Recipient: \"lead\" (workers), or the agent id of one of your goal's workers (lead)."`
	Body string `json:"body" jsonschema:"The message (1-4000 characters)."`
}

// SendMessageOutput is send_message's result.
type SendMessageOutput struct {
	MessageID int64  `json:"message_id"`
	To        string `json:"to"`
	Status    string `json:"status"` // queued: delivered at the recipient's next Stop or when it is idle
}

// ReportDoneInput is report_done's input.
type ReportDoneInput struct {
	Summary string `json:"summary" jsonschema:"What you did and anything the reader must know (1-2000 characters)."`
}

// ReportDoneOutput is report_done's result.
type ReportDoneOutput struct {
	TaskID       string `json:"task_id"`
	Status       string `json:"status"`
	Branch       string `json:"branch,omitempty"`
	Commits      int    `json:"commits"`
	LeadNotified bool   `json:"lead_notified"`
	Conflicts    string `json:"conflicts,omitempty"` // files that would not merge (ADR 0032)
	Next         string `json:"next"`
}

// ReportNoChangeInput is report_no_change's input.
type ReportNoChangeInput struct {
	Reason string `json:"reason" jsonschema:"Why the review needs no change to the code (1-2000 characters). It is posted on the pull request."`
}

// ReportNoChangeOutput is report_no_change's result.
type ReportNoChangeOutput struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Posted bool   `json:"posted"` // the reason was posted on the pull request
	URL    string `json:"url,omitempty"`
	Next   string `json:"next"`
}

// RequestAccessInput is request_access's input.
type RequestAccessInput struct {
	Kind   string `json:"kind" jsonschema:"What you need. Only \"domain\" is supported: network access to one host name over HTTPS."`
	Value  string `json:"value" jsonschema:"The exact host name, e.g. docs.example.com (no scheme, port or wildcard)."`
	Reason string `json:"reason" jsonschema:"Why the task needs it (1-1000 characters). A human reads this."`
}

// RequestAccessOutput is request_access's result.
type RequestAccessOutput struct {
	RequestID int64  `json:"request_id"`
	Status    string `json:"status"`
	Next      string `json:"next"`
}

// SubmitReviewInput is submit_review's input (reviewers only, ADR 0021).
type SubmitReviewInput struct {
	Verdict  string          `json:"verdict" jsonschema:"approve (ready for a human to merge), changes_requested (the author must change something before merging) or comment (remarks that need no change)."`
	Summary  string          `json:"summary" jsonschema:"The review: what you checked, what is good, what must change and why (1-4000 characters)."`
	Comments []ReviewComment `json:"comments,omitempty" jsonschema:"Inline comments on lines of the pull request's changed files (at most 20)."`
}

// ReviewComment is one inline review comment.
type ReviewComment struct {
	Path string `json:"path" jsonschema:"File path relative to the repository root, e.g. internal/store/store.go."`
	Line int    `json:"line" jsonschema:"Line number in the file at the reviewed commit (1-based)."`
	Body string `json:"body" jsonschema:"The comment (1-2000 characters)."`
}

// SubmitReviewOutput is submit_review's result.
type SubmitReviewOutput struct {
	ReviewID int64  `json:"review_id"`
	Posted   bool   `json:"posted"` // posted to GitHub
	URL      string `json:"url,omitempty"`
	Next     string `json:"next"`
}

// UserError marks an error whose message is safe and useful to show the
// calling agent (a refusal or invalid input). Other errors are reported to the
// agent as an internal error and only logged.
type UserError struct{ Err error }

func (e UserError) Error() string { return e.Err.Error() }
func (e UserError) Unwrap() error { return e.Err }

// internalError is what an agent sees for errors that are not a UserError.
const internalError = "internal error in agent-office; the details are in the daemon log"

// NewServer returns the MCP server for one agent. Tools are bound to caller;
// only the tools of its role are listed (ADR 0029). The engine authorizes
// every call by the caller's kind anyway (ADR 0017).
func NewServer(team Team, caller string, tools []string, version string, log *slog.Logger) *sdk.Server {
	if log == nil {
		log = slog.Default()
	}
	s := sdk.NewServer(&sdk.Implementation{Name: ServerName, Version: version}, &sdk.ServerOptions{
		Instructions: "Team tools of agent-office. Calls are attributed to you by agent-office; " +
			"you cannot act as another agent.",
		Logger: slog.New(slog.DiscardHandler), // the SDK's own logs carry session details; ours are below
	})
	t := toolset{team: team, caller: caller, log: log.With("caller", caller), tools: tools}
	{
		add(s, t, &sdk.Tool{Name: ToolCreateTask, Description: "Create a sub-task of your goal (status todo). " +
			"Make each sub-task small and independent: one worker on its own branch does it without talking " +
			"to other workers. At most 20 sub-tasks per goal."},
			func(ctx context.Context, in CreateTaskInput) (TaskInfo, error) {
				return team.CreateTask(ctx, caller, in)
			})
		add(s, t, &sdk.Tool{Name: ToolAssignTask, Description: "Start a new worker agent on one of your goal's " +
			"sub-tasks (status todo or blocked). Fails when all agent slots are busy; wait for a worker's report, " +
			"then assign the next one."},
			func(ctx context.Context, in AssignTaskInput) (AssignTaskOutput, error) {
				return team.AssignTask(ctx, caller, in)
			})
	}
	add(s, t, &sdk.Tool{Name: ToolListTasks, Description: "Show your task and its state. For the lead: the goal, " +
		"its sub-tasks, their status and their agents. For a worker: its own task and its lead.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ struct{}) (ListTasksOutput, error) { return team.ListTasks(ctx, caller) })
	{
		add(s, t, &sdk.Tool{Name: ToolSubmitReview, Description: "Submit your review of the pull request, once. " +
			"agent-office posts it on GitHub as a comment review (it never approves or merges) and ends your " +
			"session after this turn. Use changes_requested only for problems the author must fix; the author is " +
			"then given your review as follow-up work."},
			func(ctx context.Context, in SubmitReviewInput) (SubmitReviewOutput, error) {
				return team.SubmitReview(ctx, caller, in)
			})
	}
	add(s, t, &sdk.Tool{Name: ToolSendMessage, Description: "Send a message to the lead (workers: to=\"lead\") " +
		"or to one of your goal's workers (lead: to=<agent id>). It is delivered when the recipient finishes " +
		"its current turn, or right away if it is idle. Do not wait or poll for an answer: end your turn; " +
		"replies are delivered to you as new input."},
		func(ctx context.Context, in SendMessageInput) (SendMessageOutput, error) {
			return team.SendMessage(ctx, caller, in)
		})
	add(s, t, &sdk.Tool{Name: ToolReportDone, Description: "Report that your task is finished. Workers: commit " +
		"all your changes first; agent-office checks the branch, moves the task to review, sends your summary " +
		"to the lead and ends your session after this turn. Lead: call it when every sub-task is reported; it " +
		"moves the goal to review."},
		func(ctx context.Context, in ReportDoneInput) (ReportDoneOutput, error) {
			return team.ReportDone(ctx, caller, in)
		})
	{
		add(s, t, &sdk.Tool{Name: ToolReportNoChange, Description: "Only when you were relaunched to address a " +
			"review of your pull request: report that the review needs no change to the code, with the reason. " +
			"agent-office posts the reason on the pull request and moves your task back to review. If you changed " +
			"anything, commit it and call report_done instead."},
			func(ctx context.Context, in ReportNoChangeInput) (ReportNoChangeOutput, error) {
				return team.ReportNoChange(ctx, caller, in)
			})
	}
	add(s, t, &sdk.Tool{Name: ToolSubmitPlan, Description: "Submit the plan for your goal (architects only). " +
		"agent-office validates it, shows it to the user in the UI and waits for the user's approval; approval commits " +
		"it as docs/specs/<goal>.md, creates the sub-tasks and starts a Tech Lead. Submitting again replaces a plan " +
		"that is not approved yet."},
		func(ctx context.Context, in SubmitPlanInput) (SubmitPlanOutput, error) {
			return team.SubmitPlan(ctx, caller, in)
		})
	add(s, t, &sdk.Tool{Name: ToolStartQA, Description: "Start QA on your goal (Tech Lead only), once every sub-task " +
		"is reported: a QA agent merges the reported branches into an integration branch, runs the tests, adds tests " +
		"for the acceptance criteria, exercises the app and reports; its bugs become sub-tasks. The goal can only be " +
		"reported after QA passed on the current work."},
		func(ctx context.Context, _ struct{}) (StartQAOutput, error) { return team.StartQA(ctx, caller) })
	add(s, t, &sdk.Tool{Name: ToolReportQA, Description: "Report the QA result once (QA only), after committing your " +
		"merges and tests: pass, or fail with the bugs. agent-office imports your branch, creates a sub-task per bug, " +
		"tells the Tech Lead and ends your session after this turn."},
		func(ctx context.Context, in ReportQAInput) (ReportQAOutput, error) {
			return team.ReportQA(ctx, caller, in)
		})
	add(s, t, &sdk.Tool{Name: ToolGetBriefing, Description: "The facts for a briefing (assistant only), computed by " +
		"agent-office: goals and their sub-tasks, what waits on the user, what is blocked, running agents, cost and " +
		"project memory. Exact; use its numbers as they are.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ struct{}) (Briefing, error) { return team.GetBriefing(ctx, caller) })
	add(s, t, &sdk.Tool{Name: ToolListMemory, Description: "List the project memory: the notes the user approved for the " +
		"team (decisions, conventions, lessons), each marked whether it applies to you; your first prompt had the ones " +
		"that fit in it. The assistant also sees proposals waiting for the user.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ struct{}) (ListMemoryOutput, error) { return team.ListMemory(ctx, caller) })
	add(s, t, &sdk.Tool{Name: ToolProposeMemory, Description: "Propose a project memory entry (assistant only): a " +
		"decision, convention or lesson every agent should start with. The user accepts, edits or deletes it in " +
		"agent-office; nobody sees it before that."},
		func(ctx context.Context, in ProposeMemoryInput) (ProposeMemoryOutput, error) {
			return team.ProposeMemory(ctx, caller, in)
		})
	add(s, t, &sdk.Tool{Name: ToolMessageUser, Description: "Send a message to the human user; it appears in the " +
		"agent-office inbox and on your task card. Only agents with nobody above them may do this (the architect, the " +
		"assistant, a Tech Lead without an architect, a worker without a lead); others are told whom to ask. For a " +
		"question, end your turn after sending: the user's answer is delivered to you as new input."},
		func(ctx context.Context, in MessageUserInput) (MessageUserOutput, error) {
			return team.MessageUser(ctx, caller, in)
		})
	add(s, t, &sdk.Tool{Name: ToolRequestAccess, Description: "Ask a human for network access to one more host " +
		"name (kind \"domain\"). Nothing is granted until a human approves it in the agent-office UI; you get a " +
		"message with the decision. Only for sandboxed agents."},
		func(ctx context.Context, in RequestAccessInput) (RequestAccessOutput, error) {
			return team.RequestAccess(ctx, caller, in)
		})
	return s
}

type toolset struct {
	team   Team
	caller string
	log    *slog.Logger
	tools  []string // the tools this agent's role lists
}

// add registers one tool. The SDK validates the arguments against In's JSON
// schema (unknown or missing fields are refused) before fn runs. Logs carry
// the tool name, outcome and duration, never arguments or results.
func add[In, Out any](s *sdk.Server, t toolset, tool *sdk.Tool, fn func(context.Context, In) (Out, error)) {
	if !slices.Contains(t.tools, tool.Name) {
		return
	}
	sdk.AddTool(s, tool, func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		start := time.Now()
		out, err := fn(ctx, in)
		d := time.Since(start).Round(time.Millisecond)
		if err != nil {
			var ue UserError
			var zero Out
			if errors.As(err, &ue) {
				t.log.Info("team tool refused", "tool", tool.Name, "err", err, "duration", d)
				return nil, zero, ue
			}
			t.log.Error("team tool failed", "tool", tool.Name, "err", err, "duration", d)
			return nil, zero, errors.New(internalError)
		}
		t.log.Info("team tool", "tool", tool.Name, "duration", d)
		return nil, out, nil
	})
}
