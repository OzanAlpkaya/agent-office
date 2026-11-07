package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// commitMsgHook adds the agent's Co-Authored-By trailer to every commit made
// in the sandbox (ADR 0014). The agent can bypass it (--no-verify, -c), so
// the daemon still checks every imported commit; the hook makes the common
// case right.
const commitMsgHook = `#!/bin/sh
# agent-office (ADR 0014): add this agent's Co-Authored-By trailer to the commit message.
[ -n "$AGENT_OFFICE_CO_AUTHOR" ] || exit 0
exec git interpret-trailers --in-place --if-exists addIfDifferent \
  --trailer "Co-Authored-By: $AGENT_OFFICE_CO_AUTHOR" "$1"
`

// WriteGitHooks writes the hooks directory that workers mount read-only at
// GitHooksDir. It is rewritten at every daemon start, so it always matches
// the binary.
func WriteGitHooks(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sandbox: git hooks: %w", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // readable by the container user
		return fmt.Errorf("sandbox: git hooks: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".commit-msg-*")
	if err != nil {
		return fmt.Errorf("sandbox: git hooks: %w", err)
	}
	_, werr := tmp.WriteString(commitMsgHook)
	cerr := tmp.Close()
	if err := firstErr(werr, cerr, os.Chmod(tmp.Name(), 0o755), os.Rename(tmp.Name(), filepath.Join(dir, "commit-msg"))); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("sandbox: git hooks: %w", err)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// CoAuthor is the trailer value naming an agent: "<name> (agent-office)
// <noreply@agent-office.invalid>". .invalid is reserved (RFC 2606).
func CoAuthor(agentName string) string {
	return agentName + " (agent-office) <noreply@agent-office.invalid>"
}

var (
	identityName  = regexp.MustCompile(`^[^<>\n\x00]{1,100}$`)
	identityEmail = regexp.MustCompile(`^[^<>\s@]{1,64}@[^<>\s@]{1,190}$`)
	coAuthorRe    = regexp.MustCompile(`^[^<>\n\x00]{1,100} <[^<>\s@]{1,64}@[^<>\s@]{1,190}>$`)
)

// GitIdentity is the author and committer of the agent's commits: the user's
// own user.name and user.email (decided by Ozan, 2026-11-07).
type GitIdentity struct {
	Name, Email string
	CoAuthor    string // trailer value, see CoAuthor; empty for helpers that do not commit
}

func (id GitIdentity) env() ([]string, error) {
	if !identityName.MatchString(id.Name) || !identityEmail.MatchString(id.Email) {
		return nil, fmt.Errorf("sandbox: invalid git identity %q <%s>", id.Name, id.Email)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + id.Name, "GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name, "GIT_COMMITTER_EMAIL=" + id.Email,
	}
	if id.CoAuthor != "" {
		if !coAuthorRe.MatchString(id.CoAuthor) {
			return nil, fmt.Errorf("sandbox: invalid co-author %q", id.CoAuthor)
		}
		env = append(env, "AGENT_OFFICE_CO_AUTHOR="+id.CoAuthor)
	}
	return env, nil
}
