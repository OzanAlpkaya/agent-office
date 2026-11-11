// Package secret holds credentials in memory without leaking them into logs,
// and reads them from user-only token files (ADR 0015, 0020).
package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MaxFileBytes bounds a token file; real tokens are far smaller.
const MaxFileBytes = 4096

// Secret holds a credential in memory. It redacts itself in fmt, slog and
// JSON output, so passing it to a logger by mistake does not leak it.
type Secret struct{ v string }

// New wraps a credential value.
func New(v string) Secret { return Secret{v: v} }

// Reveal returns the credential. Call it only where the value is handed over.
func (s Secret) Reveal() string { return s.v }

func (s Secret) String() string               { return "[redacted]" }
func (s Secret) GoString() string             { return "secret.Secret{[redacted]}" }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("[redacted]") }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// ErrFile is the default kind of ReadFile errors.
var ErrFile = errors.New("secret: token file")

// ReadFile reads a token from path. The file must be a regular file (not a
// symlink) owned by the current user with no group or other permissions
// (0600 or 0400), in a directory that only its owner can write. Surrounding
// whitespace is trimmed; the token must be non-empty and consist of printable
// ASCII only. Errors wrap kind (ErrFile if nil) and name the file and the
// problem, never its contents.
func ReadFile(path string, kind error) (Secret, error) {
	if kind == nil {
		kind = ErrFile
	}
	fail := func(format string, a ...any) (Secret, error) {
		return Secret{}, fmt.Errorf("%w %s: "+format, append([]any{kind, path}, a...)...)
	}
	b, err := readOwned(path, kind, MaxFileBytes)
	if err != nil {
		return Secret{}, err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return fail("is empty")
	}
	// Bytes, not runes: anything outside printable ASCII (a stray Escape or
	// arrow key from pasting, a non-breaking space, invalid UTF-8) is rejected.
	// Such a byte cannot be part of the token, so naming it reveals nothing.
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x21 || c > 0x7e {
			return fail("must contain only the token on one line; byte %#02x at offset %d is not printable ASCII (re-create the file)", c, i)
		}
	}
	return Secret{v: tok}, nil
}

// ReadJSONFile reads a credential file in JSON (e.g. Codex's auth.json, ADR
// 0049) with the same checks as ReadFile, at most limit bytes, into v. A
// decoding error names the file, never its contents.
func ReadJSONFile(path string, kind error, limit int, v any) error {
	if kind == nil {
		kind = ErrFile
	}
	b, err := readOwned(path, kind, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w %s: not valid JSON", kind, path)
	}
	return nil
}

// readOwned reads a user-only file in a directory only its owner can write.
func readOwned(path string, kind error, limit int) ([]byte, error) {
	fail := func(format string, a ...any) ([]byte, error) {
		return nil, fmt.Errorf("%w %s: "+format, append([]any{kind, path}, a...)...)
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

	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return fail("%v", err)
	}
	if len(b) > limit {
		return fail("larger than %d bytes", limit)
	}
	return b, nil
}
