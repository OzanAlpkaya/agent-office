package pack

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// SecretsDirEnv overrides the secrets directory.
const SecretsDirEnv = "AGENT_OFFICE_SECRETS_DIR"

// ErrSecret is the kind of secret file errors.
var ErrSecret = errors.New("pack secret")

// DefaultSecretsDir is where the values of pack env vars live, one file per
// name: AGENT_OFFICE_SECRETS_DIR, or $XDG_CONFIG_HOME/agent-office/secrets.
func DefaultSecretsDir() (string, error) {
	if d := os.Getenv(SecretsDirEnv); d != "" {
		return filepath.Abs(d)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("pack: %w", err)
	}
	return filepath.Join(cfg, "agent-office", "secrets"), nil
}

// SecretInfo says whether a pack env var has a value file. It never holds
// the value.
type SecretInfo struct {
	EnvVar
	Present bool `json:"present"`
}

// SecretStatus reports which of the pack's env vars have a file in dir.
func (p *Pack) SecretStatus(dir string) []SecretInfo {
	out := make([]SecretInfo, 0, len(p.Env))
	for _, v := range p.Env {
		_, err := os.Lstat(filepath.Join(dir, v.Name))
		out = append(out, SecretInfo{EnvVar: v, Present: err == nil})
	}
	return out
}

// ReadSecrets reads the values of the pack's env vars from dir with the
// token-file checks (secret.ReadFile, ADR 0015). A missing optional file is
// skipped and its name returned; a missing required one, or any file that
// fails the checks, is an error that says how to create it.
func (p *Pack) ReadSecrets(dir string) (map[string]secret.Secret, []string, error) {
	vals := map[string]secret.Secret{}
	var skipped []string
	for _, v := range p.Env {
		file := filepath.Join(dir, v.Name)
		if _, err := os.Lstat(file); errors.Is(err, fs.ErrNotExist) {
			if !v.Required {
				skipped = append(skipped, v.Name)
				continue
			}
			what := v.Name
			if v.Description != "" {
				what += " (" + v.Description + ")"
			}
			return nil, nil, fmt.Errorf("%w: stack pack %s needs %s; create %s with the value on one line: "+
				"install -d -m 0700 %s && (umask 077; cat > %s) — paste, Enter, Ctrl-D", ErrSecret, p.Name, what, file, dir, file)
		}
		s, err := secret.ReadFile(file, ErrSecret)
		if err != nil {
			return nil, nil, err
		}
		vals[v.Name] = s
	}
	return vals, skipped, nil
}
