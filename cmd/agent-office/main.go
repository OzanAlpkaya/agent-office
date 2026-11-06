// Command agent-office is the local daemon that runs a team of AI coding agents on a
// git repository. It is a placeholder until Phase 1; see docs/plan.md.
package main

import (
	"log/slog"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	logger.Info("agent-office", "version", version, "status", "not implemented yet")
}
