package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// SecretsSocket is where sandbox-init fetches a worker's stack pack secrets
// (ADR 0023, ADR 0015's socket delivery). It is mounted only when the agent
// gets secrets.
const SecretsSocket = runDir + "/secrets.sock"

// secretsSocketEnv tells sandbox-init where SecretsSocket is; it is not
// passed on to the agent CLI.
const secretsSocketEnv = "AGENT_OFFICE_SECRETS_SOCKET"

// maxSecretsBytes bounds the reply sandbox-init accepts: at most 32 values of
// at most 4 KiB each, plus JSON.
const maxSecretsBytes = 160 << 10

type secretsReply struct {
	Env map[string]string `json:"env"`
}

// ServeSecretsOnce answers the first connection on l with vals and closes l,
// so the values are handed out exactly once per container start: to
// sandbox-init, which connects before it starts the agent CLI. It returns
// when that is done or l is closed.
func ServeSecretsOnce(l net.Listener, vals map[string]secret.Secret) error {
	defer l.Close()
	c, err := l.Accept()
	if err != nil {
		return err
	}
	l.Close()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	env := make(map[string]string, len(vals))
	for k, v := range vals {
		env[k] = v.Reveal()
	}
	return json.NewEncoder(c).Encode(secretsReply{Env: env})
}

// fetchSecrets reads the values from the secrets socket and returns them as
// sorted NAME=value entries. Names must be valid and not reserved, values a
// single line; anything else fails, and the agent does not start.
func fetchSecrets(sock string) ([]string, error) {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sandbox-init: secrets: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	b, err := io.ReadAll(io.LimitReader(c, maxSecretsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("sandbox-init: secrets: %w", err)
	}
	if len(b) > maxSecretsBytes {
		return nil, fmt.Errorf("sandbox-init: secrets: reply larger than %d bytes", maxSecretsBytes)
	}
	var r secretsReply
	if err := json.Unmarshal(b, &r); err != nil || r.Env == nil {
		return nil, fmt.Errorf("sandbox-init: secrets: malformed reply")
	}
	out := make([]string, 0, len(r.Env))
	for k, v := range r.Env {
		if !envName.MatchString(k) || ReservedEnv(k) || strings.ContainsAny(v, "\n\r\x00") {
			return nil, fmt.Errorf("sandbox-init: secrets: refusing variable %q", k)
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}

// withEnv returns env with the entries in set added, replacing entries of
// the same names.
func withEnv(env, set []string) []string {
	names := map[string]bool{}
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		names[k] = true
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !names[k] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}
