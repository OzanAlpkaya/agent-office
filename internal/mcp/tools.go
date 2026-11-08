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
}

// CreateTaskInput is create_task's input.
type CreateTaskInput struct {
	Title       string `json:"title" jsonschema:"Short title of the sub-task (1-200 characters)."`
	Description string `json:"description,omitempty" jsonschema:"What the worker must do and how to tell it is done (at most 20000 characters)."`
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
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Status       string     `json:"status"`
	ParentTaskID string     `json:"parent_task_id,omitempty"`
	Agents       []AgentRef `json:"agents,omitempty"`
}

// AssignTaskInput is assign_task's input.
type AssignTaskInput struct {
	TaskID string `json:"task_id" jsonschema:"Id of a sub-task of your goal, in status todo or blocked."`
}

// AssignTaskOutput is assign_task's result.
type AssignTaskOutput struct {
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id"`
	Branch  string `json:"branch"`
}

// ListTasksOutput is list_tasks's result: for a lead its goal and sub-tasks,
// for a worker its own task and its lead.
type ListTasksOutput struct {
	You   AgentRef   `json:"you"`
	Goal  *TaskInfo  `json:"goal,omitempty"`
	Tasks []TaskInfo `json:"tasks"`
	Lead  *AgentRef  `json:"lead,omitempty"`
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
