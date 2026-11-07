package mcp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP server's name in an agent's --mcp-config. Claude Code
// shows its tools as mcp__agent-office__<tool>.
const ServerName = "agent-office"

// SocketEnv names the per-agent MCP socket for `agent-office mcp`.
const SocketEnv = "AGENT_OFFICE_MCP_SOCKET"

// Roles as the engine records them (ADR 0017).
const (
	RoleLead   = "lead"
	RoleWorker = "worker"
)

// Tool names.
const (
	ToolCreateTask    = "create_task"
	ToolAssignTask    = "assign_task"
	ToolListTasks     = "list_tasks"
	ToolSendMessage   = "send_message"
	ToolReportDone    = "report_done"
	ToolRequestAccess = "request_access"
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
	RequestAccess(ctx context.Context, caller string, in RequestAccessInput) (RequestAccessOutput, error)
}

// CreateTaskInput is create_task's input.
type CreateTaskInput struct {
	Title       string `json:"title" jsonschema:"Short title of the sub-task (1-200 characters)."`
	Description string `json:"description,omitempty" jsonschema:"What the worker must do and how to tell it is done (at most 20000 characters)."`
}

// AgentRef is an agent as the tools show it.
type AgentRef struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
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

// UserError marks an error whose message is safe and useful to show the
// calling agent (a refusal or invalid input). Other errors are reported to the
// agent as an internal error and only logged.
type UserError struct{ Err error }

func (e UserError) Error() string { return e.Err.Error() }
func (e UserError) Unwrap() error { return e.Err }

// internalError is what an agent sees for errors that are not a UserError.
const internalError = "internal error in agent-office; the details are in the daemon log"

// NewServer returns the MCP server for one agent. Tools are bound to caller;
// lead-only tools are not even listed for workers (the engine refuses them
// anyway).
func NewServer(team Team, caller, role, version string, log *slog.Logger) *sdk.Server {
	if log == nil {
		log = slog.Default()
	}
	s := sdk.NewServer(&sdk.Implementation{Name: ServerName, Version: version}, &sdk.ServerOptions{
		Instructions: "Team tools of agent-office. Calls are attributed to you by agent-office; " +
			"you cannot act as another agent.",
		Logger: slog.New(slog.DiscardHandler), // the SDK's own logs carry session details; ours are below
	})
	t := toolset{team: team, caller: caller, log: log.With("caller", caller)}
	if role == RoleLead {
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
}

// add registers one tool. The SDK validates the arguments against In's JSON
// schema (unknown or missing fields are refused) before fn runs. Logs carry
// the tool name, outcome and duration, never arguments or results.
func add[In, Out any](s *sdk.Server, t toolset, tool *sdk.Tool, fn func(context.Context, In) (Out, error)) {
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
