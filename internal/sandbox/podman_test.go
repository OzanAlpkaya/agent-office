package sandbox

import (
	"slices"
	"strings"
	"testing"
)

func workerSpec() WorkerSpec {
	return WorkerSpec{
		AgentID: "abcdef123456", Image: "localhost/agent-office-worker:dev",
		Workspace: "/data/workspaces/p/abcdef123456", ObjectsDir: "/home/u/repo/.git/objects",
		ConfigDir: "/data/agents/abcdef123456/config", HookSocket: "/run/user/1000/ao/a/hook.sock",
		EgressSocket: "/run/user/1000/ao/a/egress.sock", APISocket: "/run/user/1000/ao/a/api.sock",
		AgentOffice: "/usr/bin/agent-office", Claude: "/home/u/.local/share/claude/versions/2.1.319",
		Settings: "/data/agents/abcdef123456/settings.json", GitHooks: "/data/git-hooks",
		Env:     []string{"GIT_AUTHOR_NAME=Ozan", "GIT_AUTHOR_EMAIL=ozan@example.com"},
		Command: []string{ClaudeBin, "--settings", SettingsFile, "--dangerously-skip-permissions"},
	}
}

// flagValues returns every value given to flag (as "--flag v" or "--flag=v").
func flagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		} else if v, ok := strings.CutPrefix(a, flag+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestWorkerArgsIsolation(t *testing.T) {
	args, err := WorkerArgs(workerSpec())
	if err != nil {
		t.Fatal(err)
	}
	img := slices.Index(args, "localhost/agent-office-worker:dev")
	if img < 0 {
		t.Fatal("image missing")
	}
	opts := args[:img]
	for _, want := range [][2]string{
		{"--network", "none"}, {"--user", ContainerUser}, {"--security-opt", "no-new-privileges"},
		{"--pids-limit", "256"}, {"--memory", "2g"}, {"--memory-swap", "2g"}, {"--log-driver", "none"},
		{"--name", "ao-abcdef123456"}, {"--label", LabelAgent + "=abcdef123456"}, {"--label", LabelRole + "=worker"},
	} {
		if !slices.Contains(flagValues(opts, want[0]), want[1]) {
			t.Errorf("missing %s %s", want[0], want[1])
		}
	}
	for _, flag := range []string{"--read-only", "--cap-drop=all", "--rm", "--init", "--pull=never", "-it", "--userns=host", "--detach-keys="} {
		if !slices.Contains(opts, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	joined := strings.Join(opts, " ")
	for _, bad := range []string{"keep-id", "--privileged", "--network host", "--net=host", "--cap-add", "--secret", ".ssh", "docker.sock"} {
		if strings.Contains(joined, bad) {
			t.Errorf("unexpected %q in %s", bad, joined)
		}
	}
	// Exactly these mounts, with these modes.
	wantVols := []string{
		"/data/workspaces/p/abcdef123456:/workspace",
		"/home/u/repo/.git/objects:/home/u/repo/.git/objects:ro",
		"/data/agents/abcdef123456/config:/config",
		"/run/user/1000/ao/a/hook.sock:" + HookSocket,
		"/run/user/1000/ao/a/egress.sock:" + EgressSocket,
		"/run/user/1000/ao/a/api.sock:" + APISocket,
		"/usr/bin/agent-office:" + AgentOfficeBin + ":ro",
		"/home/u/.local/share/claude/versions/2.1.319:" + ClaudeBin + ":ro",
		"/data/agents/abcdef123456/settings.json:" + SettingsFile + ":ro",
		"/data/git-hooks:" + GitHooksDir + ":ro",
	}
	if got := flagValues(opts, "-v"); !slices.Equal(got, wantVols) {
		t.Fatalf("mounts:\n got %q\nwant %q", got, wantVols)
	}
	env := flagValues(opts, "-e")
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN=" + Placeholder, "ANTHROPIC_BASE_URL=http://" + APIAddr,
		"HTTPS_PROXY=http://" + EgressAddr, "NO_PROXY=127.0.0.1,localhost", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CONFIG_DIR=" + ConfigDir, "AGENT_OFFICE_SOCKET=" + HookSocket, "GIT_AUTHOR_NAME=Ozan"} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %s", want)
		}
	}
	// The command runs under sandbox-init, after "--".
	tail := args[img+1:]
	if !slices.Equal(tail[:3], []string{AgentOfficeBin, "sandbox-init", "--"}) || tail[3] != ClaudeBin {
		t.Fatalf("command %q", tail)
	}
}

func TestWorkerArgsRejects(t *testing.T) {
	cases := map[string]func(s *WorkerSpec){
		"bad agent id":     func(s *WorkerSpec) { s.AgentID = "../x" },
		"relative mount":   func(s *WorkerSpec) { s.Workspace = "workspaces/x" },
		"unclean mount":    func(s *WorkerSpec) { s.Workspace = "/data/../etc" },
		"colon in path":    func(s *WorkerSpec) { s.ConfigDir = "/data/a:b" },
		"comma in path":    func(s *WorkerSpec) { s.Settings = "/data/a,b" },
		"empty mount":      func(s *WorkerSpec) { s.GitHooks = "" },
		"no command":       func(s *WorkerSpec) { s.Command = nil },
		"bad memory":       func(s *WorkerSpec) { s.Memory = "lots" },
		"credential env":   func(s *WorkerSpec) { s.Env = []string{"ANTHROPIC_API_KEY=x"} },
		"token-ish env":    func(s *WorkerSpec) { s.Env = []string{"MY_SERVICE_TOKEN=x"} },
		"ssh agent":        func(s *WorkerSpec) { s.Env = []string{"SSH_AUTH_SOCK=/run/ssh"} },
		"override builtin": func(s *WorkerSpec) { s.Env = []string{"CLAUDE_CODE_OAUTH_TOKEN=real"} },
		"override proxy":   func(s *WorkerSpec) { s.Env = []string{"HTTPS_PROXY=http://evil"} },
		"newline in value": func(s *WorkerSpec) { s.Env = []string{"GIT_AUTHOR_NAME=a\nb"} },
		"malformed env":    func(s *WorkerSpec) { s.Env = []string{"NOEQUALS"} },
		"option-like env":  func(s *WorkerSpec) { s.Env = []string{"-e=x"} },
	}
	for name, mutate := range cases {
		s := workerSpec()
		mutate(&s)
		if _, err := WorkerArgs(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHelperArgs(t *testing.T) {
	args, err := HelperArgs(HelperSpec{AgentID: "abcdef123456", Image: "img",
		Workspace: "/data/ws", ObjectsDir: "/repo/.git/objects", Command: []string{"git", "status"}})
	if err != nil {
		t.Fatal(err)
	}
	img := slices.Index(args, "img")
	opts := args[:img]
	if got := flagValues(opts, "-v"); !slices.Equal(got, []string{"/data/ws:/workspace", "/repo/.git/objects:/repo/.git/objects:ro"}) {
		t.Fatalf("helper mounts %q", got)
	}
	for _, flag := range []string{"--read-only", "--cap-drop=all", "--userns=host"} {
		if !slices.Contains(opts, flag) {
			t.Errorf("helper missing %s", flag)
		}
	}
	if !slices.Contains(flagValues(opts, "--network"), "none") || !slices.Contains(flagValues(opts, "--label"), LabelRole+"=helper") {
		t.Fatal("helper network/label")
	}
	if slices.Contains(opts, "-it") || strings.Contains(strings.Join(opts, " "), ".sock") {
		t.Fatal("helper must have no tty and no sockets")
	}
	if !slices.Equal(args[img+1:], []string{"git", "status"}) {
		t.Fatalf("helper command %q", args[img+1:])
	}
}
