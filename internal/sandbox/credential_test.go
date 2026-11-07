package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokenFileWrapsErrTokenFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-office")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(filepath.Join(dir, "oauth-token")); !errors.Is(err, ErrTokenFile) {
		t.Fatalf("want ErrTokenFile, got %v", err)
	}
}

func TestDefaultTokenFile(t *testing.T) {
	t.Setenv(TokenFileEnv, "")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if p, err := DefaultTokenFile(); err != nil || p != "/cfg/agent-office/oauth-token" {
		t.Fatalf("default %q %v", p, err)
	}
	t.Setenv(TokenFileEnv, "/elsewhere/token")
	if p, err := DefaultTokenFile(); err != nil || p != "/elsewhere/token" {
		t.Fatalf("override %q %v", p, err)
	}
}
