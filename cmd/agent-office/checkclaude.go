package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store"
	"github.com/OzanAlpkaya/agent-office/internal/terminal"
)

// `agent-office check-claude` (ADR 0016 amended): Spike F's interactive check
// of the installed Claude Code, without a prompt or a token. One sandboxed
// agent starts the way the daemon starts them (the worker image, the
// pre-seeded config, the settings file) in a throwaway engine with a temp data
// dir, a temp repository and a placeholder token, so nothing can be spent. It
// passes when the session starts (its SessionStart hook arrives, which a
// first-run dialog holds back) and fails naming the dialog on the screen. A
// pass records the version in the data dir, which ends the daemon's
// untested-version warning.

// verifiedFile is the record of the Claude Code versions check-claude passed.
const verifiedFile = "claude-verified.json"

// verifiedRecord is that file's content.
type verifiedRecord struct {
	Version    string    `json:"version"`
	VerifiedAt time.Time `json:"verified_at"`
	Binary     string    `json:"binary"`
	Image      string    `json:"image"`
}

// verifiedVersion is the Claude Code version check-claude last passed in
// dataDir, or "".
func verifiedVersion(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, verifiedFile))
	if err != nil {
		return ""
	}
	var r verifiedRecord
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	if _, err := agent.ParseVersion(r.Version); err != nil {
		return ""
	}
	return r.Version
}

func writeVerified(dataDir string, r verifiedRecord) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dataDir, verifiedFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// checkResult is what one check found.
type checkResult struct {
	Version string
	Passed  bool
	Dialog  *agent.Dialog // the first-run dialog on the screen, on a fail
	Screen  string        // the last lines of the screen, on a fail without a known dialog
	Took    time.Duration
}

// checkEnv is what a check needs from its surroundings; tests replace parts.
type checkEnv struct {
	claude   claudeInfo
	self     string // this binary, static (it is mounted into the worker)
	image    string // the worker image
	podman   sandbox.Podman
	timeout  time.Duration
	log      *slog.Logger
	keepTemp bool // tests: look at the temp dir afterwards
}

func checkClaudeCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-claude", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", envOr("AGENT_OFFICE_DATA_DIR", defaultDataDir()),
		"where a passed version is recorded [AGENT_OFFICE_DATA_DIR]")
	bin := fs.String("claude-bin", envOr("AGENT_OFFICE_CLAUDE_BIN", "claude"), "Claude Code binary [AGENT_OFFICE_CLAUDE_BIN]")
	timeout := fs.Duration("timeout", 45*time.Second, "how long the session may take to start")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent-office check-claude: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "agent-office check-claude:", err)
		return 1
	}
	path, err := exec.LookPath(*bin)
	if err != nil {
		return fail(fmt.Errorf("claude binary: %w", err))
	}
	ci, err := resolveClaude(path)
	if err != nil {
		return fail(err)
	}
	if ci.version == "" {
		return fail(fmt.Errorf("`%s --version` gave no version", ci.path))
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return fail(err)
	}
	dd, err := filepath.Abs(*dataDir)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "checking Claude Code %s (%s) in a sandboxed worker, no prompt and no token ...\n", ci.version, ci.path)
	res, err := runClaudeCheck(context.Background(), checkEnv{claude: ci, self: self, image: sandbox.BaseImage(),
		podman: sandbox.Podman{}, timeout: *timeout, log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return fail(err)
	}
	return reportCheck(res, dd, ci, stdout)
}

// reportCheck prints a result and records a pass.
func reportCheck(res checkResult, dataDir string, ci claudeInfo, stdout io.Writer) int {
	if !res.Passed {
		fmt.Fprintf(stdout, "FAIL: Claude Code %s did not reach its prompt within the timeout.\n", res.Version)
		if res.Dialog != nil {
			fmt.Fprintf(stdout, "  the screen shows the %s\n  the seed key that should skip it: %s\n", res.Dialog.Name, res.Dialog.Key)
		} else {
			fmt.Fprintf(stdout, "  no known first-run dialog on the screen; its last lines:\n%s\n", indent(res.Screen))
		}
		fmt.Fprintln(stdout, "The daemon keeps its untested-version warning. See spikes/sandbox-claude/README.md to look at it by hand.")
		return 1
	}
	fmt.Fprintf(stdout, "PASS: Claude Code %s started in %s with no first-run dialog.\n", res.Version, res.Took.Round(100*time.Millisecond))
	if err := writeVerified(dataDir, verifiedRecord{Version: res.Version, VerifiedAt: time.Now().UTC(), Binary: ci.path,
		Image: sandbox.BaseImage()}); err != nil {
		fmt.Fprintf(stdout, "but the result could not be recorded: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Recorded in %s: the daemon's warning about this version is gone (reload the page).\n",
		filepath.Join(dataDir, verifiedFile))
	return 0
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// runClaudeCheck starts one sandboxed agent without a task in a throwaway
// engine and waits for its session to start.
func runClaudeCheck(ctx context.Context, env checkEnv) (checkResult, error) {
	res := checkResult{Version: env.claude.version}
	if _, err := env.podman.Version(ctx); err != nil {
		return res, fmt.Errorf("the check needs rootless podman: %w", err)
	}
	if ok, err := env.podman.ImageExists(ctx, env.image); err != nil || !ok {
		return res, fmt.Errorf("the worker image %s is missing: run `agent-office worker-image` (or make worker-image) first", env.image)
	}
	if ok, err := isStatic(env.self); err != nil || !ok {
		return res, fmt.Errorf("this binary must be statically linked (it is mounted into the worker): build it with make build")
	}
	tmp, err := os.MkdirTemp("", "ao-check-claude-")
	if err != nil {
		return res, err
	}
	if !env.keepTemp {
		defer removeCheckDir(env.podman, tmp)
	}
	repo := filepath.Join(tmp, "repo")
	if err := initCheckRepo(ctx, repo); err != nil {
		return res, err
	}
	// A placeholder token: the daemon-side API proxy injects it, and nothing
	// it sends could be billed.
	tokenDir := filepath.Join(tmp, "cfg")
	if err := os.Mkdir(tokenDir, 0o700); err != nil {
		return res, err
	}
	tokenFile := filepath.Join(tokenDir, "oauth-token")
	if err := os.WriteFile(tokenFile, []byte("check-claude-placeholder-token\n"), 0o600); err != nil {
		return res, err
	}
	data := filepath.Join(tmp, "data")
	hooksDir := filepath.Join(data, "git-hooks")
	if err := sandbox.WriteGitHooks(hooksDir); err != nil {
		return res, err
	}
	rt, err := runtimeDir()
	if err != nil {
		return res, err
	}
	var id [6]byte // the container instance: hex, its own, so no daemon removes it as an orphan
	_, _ = rand.Read(id[:])
	run := filepath.Join(rt, "check-"+hex.EncodeToString(id[:]))
	if err := os.Mkdir(run, 0o700); err != nil {
		return res, err
	}
	defer os.RemoveAll(run)
	st, err := store.Open(ctx, filepath.Join(data, "agent-office.db"))
	if err != nil {
		return res, err
	}
	defer st.Close()
	allow, _ := sandbox.ParseAllowlist(sandbox.DefaultEgressAllow)
	e, err := engine.New(ctx, engine.Options{
		Repo: repo, WorktreeRoot: filepath.Join(data, "worktrees"), Store: st, Git: git.Git{}, MaxAgents: 1,
		ClaudeBin: env.claude.path, HookCommand: agent.ShellQuote(env.self) + " hook", RuntimeDir: run,
		BaseEnv: os.Environ(), TermSize: terminal.Size{Cols: 120, Rows: 36}, Logger: env.log, AgentOfficeBin: env.self,
		StopTimeout: 3 * time.Second,
		Sandbox: &engine.SandboxOptions{
			Instance: hex.EncodeToString(id[:]), Podman: env.podman, Image: env.image,
			WorkspaceRoot: filepath.Join(data, "workspaces"), AgentsRoot: filepath.Join(data, "agents"),
			GitHooks: hooksDir, AgentOffice: env.self, Claude: env.claude.path, ClaudeVersion: env.claude.version,
			TokenFile: tokenFile, Allow: allow,
		},
	})
	if err != nil {
		return res, fmt.Errorf("start the check's engine: %w", err)
	}
	defer e.Shutdown()
	updates, cancel := e.Subscribe()
	defer cancel()
	start := time.Now()
	a, err := e.Start(ctx, engine.StartRequest{})
	if err != nil {
		return res, fmt.Errorf("start the sandboxed agent: %w", err)
	}
	deadline := time.NewTimer(env.timeout)
	defer deadline.Stop()
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return res, errors.New("the check's engine stopped")
			}
			if u.Type != "agent" || u.Agent == nil || u.Agent.ID != a.ID {
				continue
			}
			switch u.Agent.State {
			case string(hooks.Idle), string(hooks.Working):
				res.Passed, res.Took = true, time.Since(start)
				return res, nil
			case string(hooks.Exited):
				return screenResult(e, a.ID, res), nil
			}
		case <-deadline.C:
			return screenResult(e, a.ID, res), nil
		}
	}
}

// screenResult reads the agent's screen for the dialog it shows.
func screenResult(e *engine.Engine, id string, res checkResult) checkResult {
	var stream []byte
	if t, err := e.Terminal(id); err == nil {
		stream, _ = t.Snapshot()
	}
	if d, ok := agent.DetectDialog(stream); ok {
		res.Dialog = &d
		return res
	}
	lines := strings.Split(agent.ScreenText(stream), "\n")
	var keep []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	res.Screen = strings.Join(keep[max(0, len(keep)-12):], "\n")
	return res
}

func initCheckRepo(ctx context.Context, repo string) error {
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("agent-office check-claude\n"), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "README.md"},
		{"-c", "user.name=agent-office", "-c", "user.email=check@agent-office.invalid", "commit", "-q", "-m", "check"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

// removeCheckDir removes the temp dir; files the worker wrote belong to its
// subordinate user and go through podman unshare.
func removeCheckDir(p sandbox.Podman, dir string) {
	if err := os.RemoveAll(dir); err == nil {
		return
	}
	_ = exec.Command("podman", "unshare", "rm", "-rf", "--", dir).Run()
	_ = os.RemoveAll(dir)
	_ = p
}
