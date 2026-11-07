package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeToken is a made-up value; real tokens never appear in tests.
const fakeToken = "fake-oauth-token-for-tests-0123456789"

var errKind = errors.New("test: token file")

func tokenDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-office")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeToken(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "oauth-token")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile's mode is subject to umask
		t.Fatal(err)
	}
	return p
}

func TestReadFileAccepts(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		p := writeToken(t, tokenDir(t), "  "+fakeToken+"\n", mode)
		s, err := ReadFile(p, errKind)
		if err != nil {
			t.Fatalf("mode %#o: %v", mode, err)
		}
		if s.Reveal() != fakeToken {
			t.Fatalf("mode %#o: token not trimmed", mode)
		}
	}
}

func TestReadFileRejects(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"group readable": func(t *testing.T) string { return writeToken(t, tokenDir(t), fakeToken, 0o640) },
		"world readable": func(t *testing.T) string { return writeToken(t, tokenDir(t), fakeToken, 0o604) },
		"empty":          func(t *testing.T) string { return writeToken(t, tokenDir(t), " \n", 0o600) },
		"two lines":      func(t *testing.T) string { return writeToken(t, tokenDir(t), fakeToken+"\nsecond", 0o600) },
		"inner space":    func(t *testing.T) string { return writeToken(t, tokenDir(t), "fake token", 0o600) },
		// A stray Escape or arrow key during the paste (seen in Spike F).
		"escape byte": func(t *testing.T) string { return writeToken(t, tokenDir(t), "fake\x1b[Dtoken", 0o600) },
		"non-ASCII":   func(t *testing.T) string { return writeToken(t, tokenDir(t), "fake token", 0o600) },
		"bad UTF-8":   func(t *testing.T) string { return writeToken(t, tokenDir(t), "fake\xfftoken", 0o600) },
		"too large": func(t *testing.T) string {
			return writeToken(t, tokenDir(t), strings.Repeat("a", MaxFileBytes+1), 0o600)
		},
		"missing": func(t *testing.T) string { return filepath.Join(tokenDir(t), "oauth-token") },
		"directory": func(t *testing.T) string {
			p := filepath.Join(tokenDir(t), "oauth-token")
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"symlink": func(t *testing.T) string {
			dir := tokenDir(t)
			real := writeToken(t, dir, fakeToken, 0o600)
			link := filepath.Join(dir, "link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"group-writable dir": func(t *testing.T) string {
			dir := tokenDir(t)
			p := writeToken(t, dir, fakeToken, 0o600)
			if err := os.Chmod(dir, 0o770); err != nil {
				t.Fatal(err)
			}
			return p
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ReadFile(mk(t), errKind)
			if !errors.Is(err, errKind) {
				t.Fatalf("want errKind, got %v", err)
			}
			if strings.Contains(err.Error(), fakeToken) {
				t.Fatal("error message contains the token")
			}
		})
	}
}

func TestSecretRedacts(t *testing.T) {
	s := Secret{v: fakeToken}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "token", s, "nested", struct{ T Secret }{s})
	j, err := json.Marshal(map[string]any{"t": s})
	if err != nil {
		t.Fatal(err)
	}
	outs := []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s), logs.String(), string(j)}
	for _, o := range outs {
		if strings.Contains(o, fakeToken) {
			t.Fatalf("token leaked: %q", o)
		}
	}
}

func TestReadFileDefaultKind(t *testing.T) {
	_, err := ReadFile(filepath.Join(tokenDir(t), "missing"), nil)
	if !errors.Is(err, ErrFile) {
		t.Fatalf("want ErrFile, got %v", err)
	}
}
