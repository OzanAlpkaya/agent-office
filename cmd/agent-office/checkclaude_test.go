package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
)

// A pass records the version in the data dir and ends the warning; a fail
// names the dialog and records nothing.
func TestCheckClaudeReport(t *testing.T) {
	dd := t.TempDir()
	ci := claudeInfo{path: "/opt/claude/9.9.9", version: "9.9.9"}
	var out bytes.Buffer
	d := agent.Dialog{Name: "workspace trust dialog", Key: "hasTrustDialogAccepted"}
	if code := reportCheck(checkResult{Version: "9.9.9", Dialog: &d}, dd, ci, &out); code == 0 ||
		!strings.Contains(out.String(), "FAIL") || !strings.Contains(out.String(), "workspace trust dialog") ||
		!strings.Contains(out.String(), "hasTrustDialogAccepted") {
		t.Fatalf("fail report %d:\n%s", code, out.String())
	}
	if v := verifiedVersion(dd); v != "" {
		t.Fatalf("a fail recorded %q", v)
	}
	out.Reset()
	if code := reportCheck(checkResult{Version: "9.9.9", Screen: "something else"}, dd, ci, &out); code == 0 ||
		!strings.Contains(out.String(), "something else") {
		t.Fatalf("unknown screen report %d:\n%s", code, out.String())
	}
	out.Reset()
	if code := reportCheck(checkResult{Version: "9.9.9", Passed: true}, dd, ci, &out); code != 0 || !strings.Contains(out.String(), "PASS") {
		t.Fatalf("pass report %d:\n%s", code, out.String())
	}
	if v := verifiedVersion(dd); v != "9.9.9" {
		t.Fatalf("verified %q", v)
	}
	if fi, err := os.Stat(filepath.Join(dd, verifiedFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("record %v %v", fi, err)
	}
	if w := versionWarnings(ci, modePodman, verifiedVersion(dd)); len(w) != 0 {
		t.Fatalf("still warned after a pass: %q", w)
	}
	// A broken record is no verification.
	if err := os.WriteFile(filepath.Join(dd, verifiedFile), []byte(`{"version": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := verifiedVersion(dd); v != "" {
		t.Fatalf("broken record read as %q", v)
	}
}

func TestCheckClaudeFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := checkClaudeCommand([]string{"stray"}, &out, &errOut); code != 2 {
		t.Fatalf("stray argument: %d", code)
	}
	if code := checkClaudeCommand([]string{"--claude-bin", "/nonexistent/claude"}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "claude binary") {
		t.Fatalf("missing binary: %d %s", code, errOut.String())
	}
}
