package hooks

import "github.com/OzanAlpkaya/agent-office/internal/agent"

// State is the coarse state of an agent shown in the UI.
type State string

// Agent states (see docs/architecture.md).
const (
	Starting          State = "starting"
	Idle              State = "idle"
	Working           State = "working"
	WaitingPermission State = "waiting_permission"
	Exited            State = "exited"
)

// Status is an agent's state plus what it is doing right now.
type Status struct {
	State State `json:"state"`
	// Activity is the tool in use or awaiting permission, if any.
	Activity string `json:"activity,omitempty"`
}

// Next returns the status after ev. It is a pure function so the transition
// table can be tested exhaustively. Events from subagents and internal
// background agents never change the main agent's status, and nothing leaves
// Exited.
func Next(cur Status, ev agent.Event) Status {
	if cur.State == Exited || ev.Subagent {
		return cur
	}
	switch ev.Kind {
	case agent.SessionStarted, agent.TurnFinished, agent.Idle:
		return Status{State: Idle}
	case agent.TurnStarted, agent.ToolFinished, agent.ToolFailed:
		return Status{State: Working}
	case agent.ToolStarted:
		return Status{State: Working, Activity: ev.Tool}
	case agent.PermissionRequested:
		// Notification(permission_prompt) arrives ~6 s after PermissionRequest
		// and carries no tool name; keep the one we already have.
		activity := ev.Tool
		if activity == "" {
			activity = cur.Activity
		}
		return Status{State: WaitingPermission, Activity: activity}
	case agent.SessionEnded:
		return Status{State: Exited}
	default:
		return cur
	}
}
