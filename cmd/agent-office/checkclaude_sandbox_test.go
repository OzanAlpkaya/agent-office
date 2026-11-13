//go:build sandbox

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// fakeClaude is a CLI that answers --version and then either starts its
// session (the SessionStart hook) or shows the trust dialog and waits, as
// Claude Code does when a seed key is not honoured (Spike F).
const fakeClaude = `#!/bin/sh
[ "$1" = --version ] && { echo "9.9.9 (Claude Code)"; exit 0; }
sid=; prev=
for a in "$@"; do [ "$prev" = --session-id ] && sid=$a; prev=$a; done
if [ "$MODE" = dialog ]; then
  printf '\033[2J\033[1;1HAccessing workspace:\033[2;1H/workspace\033[3;1HQuick\033[1Csafety\033[1Ccheck:\033[1CIs this a project you created or one you trust?'
  printf '\033[5;1H\342\235\257 No, exit\033[6;1HYes, I trust this folder\n'
  sleep 120; exit 0
fi
printf '{"hook_event_name":"SessionStart","session_id":"%s","source":"startup"}' "$sid" | /opt/agent-office/agent-office hook >/dev/null 2>&1
echo "ready"; sleep 120
`

func checkFixture(t *testing.T, mode string) checkEnv {
	t.Helper()
	p := sandbox.Podman{}
	if ok, err := p.ImageExists(context.Background(), sandbox.BaseImage()); err != nil || !ok {
		t.Skipf("worker image %s missing: run make worker-image", sandbox.BaseImage())
	}
	self, _ := filepath.Abs("../../bin/agent-office")
	if ok, err := isStatic(self); err != nil || !ok {
		t.Skip("needs a static bin/agent-office: run make build")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := strings.Replace(fakeClaude, `"$MODE"`, `"`+mode+`"`, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return checkEnv{claude: claudeInfo{path: bin, version: "9.9.9"}, self: self, image: sandbox.BaseImage(), podman: p,
		timeout: 20 * time.Second, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestCheckClaudeInAWorker(t *testing.T) {
	res, err := runClaudeCheck(context.Background(), checkFixture(t, "start"))
	if err != nil || !res.Passed || res.Version != "9.9.9" {
		t.Fatalf("a CLI that starts its session: %+v %v", res, err)
	}
	res, err = runClaudeCheck(context.Background(), checkFixture(t, "dialog"))
	if err != nil || res.Passed || res.Dialog == nil || !strings.Contains(res.Dialog.Name, "workspace trust") {
		t.Fatalf("a CLI that stops at the trust dialog: %+v %v", res, err)
	}
}
