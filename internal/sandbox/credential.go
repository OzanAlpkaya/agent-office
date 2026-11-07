package sandbox

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// TokenFileEnv overrides the path of the agent CLI's OAuth token file. It holds
// a path, never the token itself.
const TokenFileEnv = "AGENT_OFFICE_OAUTH_TOKEN_FILE"

// maxTokenBytes bounds the token file; a real token is far smaller.
const maxTokenBytes = 4096

// ErrTokenFile marks every problem with the token file. Messages name the file
// and the problem, never its contents.
var ErrTokenFile = errors.New("sandbox: oauth token file")

// Secret holds a credential in memory. It redacts itself in fmt, slog and
// JSON output, so passing it to a logger by mistake does not leak it.
type Secret struct{ v string }

// Reveal returns the credential. Call it only where the value is handed over.
func (s Secret) Reveal() string { return s.v }

func (s Secret) String() string               { return "[redacted]" }
func (s Secret) GoString() string             { return "sandbox.Secret{[redacted]}" }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("[redacted]") }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// DefaultTokenFile returns $AGENT_OFFICE_OAUTH_TOKEN_FILE, or else
// $XDG_CONFIG_HOME/agent-office/oauth-token (~/.config/agent-office/oauth-token).
func DefaultTokenFile() (string, error) {
	if p := os.Getenv(TokenFileEnv); p != "" {
		return filepath.Abs(p)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTokenFile, err)
	}
	return filepath.Join(dir, "agent-office", "oauth-token"), nil
}

// ReadTokenFile reads the OAuth token from path (ADR 0015). The file must be a
// regular file (not a symlink) owned by the current user with no group or
// other permissions (0600 or 0400), in a directory that only its owner can
// write. Surrounding whitespace is trimmed; the token must be non-empty and
// contain no whitespace or control characters.
func ReadTokenFile(path string) (Secret, error) {
	fail := func(format string, a ...any) (Secret, error) {
		return Secret{}, fmt.Errorf("%w %s: "+format, append([]any{ErrTokenFile, path}, a...)...)
	}
	uid := uint32(os.Getuid())

	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fail("%v", err)
	}
	if st, ok := dir.Sys().(*syscall.Stat_t); !dir.IsDir() || !ok || st.Uid != uid || dir.Mode().Perm()&0o022 != 0 {
		return fail("its directory must be owned by you and writable only by you (chmod 700 %s)", filepath.Dir(path))
	}

	// O_NOFOLLOW: a symlink fails here, and the checks below run on the opened
	// file itself, so it cannot be swapped between check and read.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return fail("must be a regular file, not a symlink")
		}
		return fail("%v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fail("%v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return fail("must be a regular file")
	case !ok || st.Uid != uid:
		return fail("must be owned by you")
	case fi.Mode().Perm()&0o077 != 0:
		return fail("mode is %#o; it must be readable only by you (chmod 600 %s)", fi.Mode().Perm(), path)
	}

	b, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil {
		return fail("%v", err)
	}
	if len(b) > maxTokenBytes {
		return fail("larger than %d bytes", maxTokenBytes)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return fail("is empty")
	}
	for _, r := range tok {
		if r <= ' ' || r == 0x7f {
			return fail("must contain only the token on one line")
		}
	}
	return Secret{v: tok}, nil
}
