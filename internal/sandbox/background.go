package sandbox

import (
	"net"
	"strings"
)

// Claude Code's own background requests (Phase 17). A sandboxed Claude Code
// starts with CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, DISABLE_AUTOUPDATER and
// CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL, which removed every
// such request measured with 2.1.322 (the plugin marketplace's git clone from
// github.com and a downloads.claude.ai check at start). What a later version
// may still send on its own, the user reported or Claude Code is known to
// send, is listed here: still refused as before, but recorded as expected
// background traffic instead of a warning. Anything else stays a warning.

// backgroundHosts are hosts only the CLI itself contacts; no task needs them.
var backgroundHosts = map[string]bool{
	"downloads.claude.ai": true, // Claude Code's own download and version host
}

// backgroundAPI are Anthropic API routes the CLI calls on its own; the API
// socket forwards only POST /v1/messages.
var backgroundAPI = map[string]bool{
	"POST /v1/messages/count_tokens": true, // token counting for its context display
}

// BackgroundEgress reports whether a refused CONNECT target (host:port) is
// the CLI's own background traffic.
func BackgroundEgress(target string) bool {
	host, port, err := net.SplitHostPort(target)
	return err == nil && port == "443" && backgroundHosts[strings.TrimSuffix(strings.ToLower(host), ".")]
}

// BackgroundAPI reports whether a refused API request is the CLI's own
// background traffic.
func BackgroundAPI(method, path string) bool {
	return backgroundAPI[method+" "+path]
}
