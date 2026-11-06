package agent

// EventKind is a CLI-agnostic agent lifecycle event. Adapters translate their
// CLI's hook payloads into these kinds (ADR 0003).
type EventKind string

// Event kinds.
const (
	SessionStarted      EventKind = "session_started"
	TurnStarted         EventKind = "turn_started"
	ToolStarted         EventKind = "tool_started"
	ToolFinished        EventKind = "tool_finished"
	ToolFailed          EventKind = "tool_failed"
	PermissionRequested EventKind = "permission_requested"
	TurnFinished        EventKind = "turn_finished"
	Idle                EventKind = "idle"
	SessionEnded        EventKind = "session_ended"
	Other               EventKind = "other"
)

// Event is the reduced, non-sensitive form of a hook payload. It never carries
// prompts, tool inputs, tool output or assistant text.
type Event struct {
	Kind      EventKind
	SessionID string
	// Hook is the CLI's own event name (e.g. "PreToolUse"), for logs.
	Hook string
	// Tool is the tool name for tool and permission events.
	Tool string
	// Detail is an enum-like discriminator: SessionStart source, SessionEnd
	// reason, Notification type. Never free text.
	Detail string
	// Subagent is true when the event comes from a subagent or an internal
	// background agent. Such events must not change the main agent's state.
	Subagent bool
	// StopHookActive is set on TurnFinished when the turn already continued
	// because of a Stop hook (loop guard for message injection).
	StopHookActive bool
}
