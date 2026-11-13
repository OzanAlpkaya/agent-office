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

	// UserInterrupted is raised by the daemon, not by a hook: the user sent a
	// lone Esc or Ctrl-C through the UI. Claude Code fires no hook when a turn
	// is interrupted or a permission prompt is dismissed (measured in Phase 1),
	// so without this the state would stay "working" or "waiting_permission".
	UserInterrupted EventKind = "user_interrupted"
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
	// Background is set on a Bash ToolStarted whose command runs in the
	// background (Claude Code's run_in_background, ADR 0064). One boolean of
	// the tool input; nothing else of it is read.
	Background bool
	// TranscriptPath is the session transcript's path (Claude Code's
	// transcript_path). Host mode reads token usage from it (ADR 0026). A
	// local path: never logged or stored.
	TranscriptPath string
}
