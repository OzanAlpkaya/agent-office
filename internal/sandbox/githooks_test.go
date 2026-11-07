package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir with the worker's git environment (identity, co-author
// and the mounted hooks via command-scope config), as WorkerArgs sets it.
func gitIn(t *testing.T, dir, hooks, coAuthor string, args ...string) string {
	t.Helper()
	id := GitIdentity{Name: "Ozan", Email: "ozan@example.com", CoAuthor: coAuthor}
	env, err := id.env()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(env, "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0="+hooks)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func trailers(t *testing.T, dir, hooks string) []string {
	t.Helper()
	out := gitIn(t, dir, hooks, "", "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestCommitMsgHookAddsTrailerOnce(t *testing.T) {
	hooks := filepath.Join(t.TempDir(), "git-hooks")
	if err := WriteGitHooks(hooks); err != nil {
		t.Fatal(err)
	}
	if err := WriteGitHooks(hooks); err != nil { // rewritten at every start
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(hooks, "commit-msg")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("hook mode: %v %v", fi.Mode(), err)
	}
	repo := t.TempDir()
	co := CoAuthor("worker-1")
	gitIn(t, repo, hooks, co, "init", "-q", "-b", "main")
	// The agent tries to point hooks elsewhere in the repository config.
	gitIn(t, repo, hooks, co, "config", "core.hooksPath", "/nonexistent")

	gitIn(t, repo, hooks, co, "commit", "-q", "--allow-empty", "-m", "feat: one")
	if got := trailers(t, repo, hooks); len(got) != 1 || got[0] != co {
		t.Fatalf("trailers %q", got)
	}
	gitIn(t, repo, hooks, co, "commit", "-q", "--amend", "--allow-empty", "-m", "feat: one (amended)")
	if got := trailers(t, repo, hooks); len(got) != 1 {
		t.Fatalf("after amend %q", got)
	}
	gitIn(t, repo, hooks, co, "commit", "-q", "--allow-empty", "-m", "feat: two\n\nCo-Authored-By: "+co)
	if got := trailers(t, repo, hooks); len(got) != 1 {
		t.Fatalf("already present %q", got)
	}
	gitIn(t, repo, hooks, "", "commit", "-q", "--allow-empty", "-m", "chore: no co-author env")
	if got := trailers(t, repo, hooks); len(got) != 0 {
		t.Fatalf("without AGENT_OFFICE_CO_AUTHOR %q", got)
	}
	if a := gitIn(t, repo, hooks, "", "log", "-1", "--format=%an <%ae> / %cn <%ce>"); a != "Ozan <ozan@example.com> / Ozan <ozan@example.com>" {
		t.Fatalf("identity %q", a)
	}
}

func TestCoAuthor(t *testing.T) {
	if got := CoAuthor("worker-1"); got != "worker-1 (agent-office) <noreply@agent-office.invalid>" ||
		!coAuthorRe.MatchString(got) {
		t.Fatalf("CoAuthor = %q", got)
	}
}
