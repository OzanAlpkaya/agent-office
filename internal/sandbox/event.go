package sandbox

import (
	"strings"
	"time"
	"unicode"
)

// EventKind names something a sandbox proxy refused or failed to do. Events
// are shown in the UI next to the agent, so a blocked request is visible
// instead of failing silently (ADR 0016).
type EventKind string

const (
	EgressDenied EventKind = "egress_denied" // CONNECT to a host or port not on the allowlist
	EgressFailed EventKind = "egress_failed" // allowed, but the upstream could not be reached
	APIRejected  EventKind = "api_rejected"  // method/path not forwarded by the API socket
	APIAuth      EventKind = "api_auth"      // unexpected credentials on the API socket
	APIError     EventKind = "api_error"     // upstream error while proxying

	// Import of the agent's branch into the main repository (ADR 0014).
	ImportDiverged EventKind = "import_diverged" // rewritten history went to a new -rewrite-N branch
	CommitFlagged  EventKind = "commit_flagged"  // an imported commit lacks the expected author or trailer
	ImportFailed   EventKind = "import_failed"
)

// Event is one refusal or failure. Detail holds only the target (host:port or
// "METHOD /path"), never headers, bodies or query strings.
type Event struct {
	Kind   EventKind `json:"kind"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// maxDetail bounds an event detail; it comes from agent-controlled input.
const maxDetail = 200

// sanitize makes agent-controlled text safe to log and display: printable
// characters only, bounded length.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
	if len(s) > maxDetail {
		s = s[:maxDetail] + "…"
	}
	return s
}
