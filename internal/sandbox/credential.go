package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// TokenFileEnv overrides the path of the agent CLI's OAuth token file. It holds
// a path, never the token itself.
const TokenFileEnv = "AGENT_OFFICE_OAUTH_TOKEN_FILE"

// ErrTokenFile marks every problem with the token file. Messages name the file
// and the problem, never its contents.
var ErrTokenFile = errors.New("sandbox: oauth token file")

// Secret is a credential that redacts itself in logs (package secret).
type Secret = secret.Secret

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

// ReadTokenFile reads the OAuth token from path (ADR 0015) with the checks of
// secret.ReadFile; errors wrap ErrTokenFile.
func ReadTokenFile(path string) (Secret, error) { return secret.ReadFile(path, ErrTokenFile) }
