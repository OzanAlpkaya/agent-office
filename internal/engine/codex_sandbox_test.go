//go:build sandbox

package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/hooks"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/secret"
)

// fakeCodexScript stands in for the Codex CLI inside the worker, the way
// Spike G saw it: it picks its own session id (or keeps the one it resumes),
// sends hooks in Codex's shape through the real forwarder (SessionStart only
// with the first prompt, as Codex does), talks to the API
// socket over TLS with its placeholder login, and runs a turn for every line
// typed into it.
const fakeCodexScript = `#!/bin/sh
sid=$(cat /proc/sys/kernel/random/uuid); src=startup; prev=; prompt=; after=
for a in "$@"; do
  if [ -n "$after" ]; then prompt=$a; fi
  [ "$a" = "--" ] && after=1
  [ "$prev" = resume ] && sid=$a && src=resume
  prev=$a
done
send() { printf '{"hook_event_name":"%s","session_id":"%s","source":"%s","model":"gpt-6.1-sol"}' "$1" "${2:-$sid}" "$src" | /opt/agent-office/agent-office hook >/dev/null 2>&1; }
bearer=$(sed -n 's/.*"access_token": "\([^"]*\)".*/\1/p' "$CODEX_HOME/auth.json")
api() { curl -s -o /dev/null -w '%{http_code}' --cacert "$CODEX_CA_CERTIFICATE" -H "Authorization: Bearer $bearer" "$@"; }
started=
turn() { [ -z "$started" ] && send SessionStart && started=1; send UserPromptSubmit; echo "responses=$(api -X POST -H 'content-type: application/json' -d '{}' https://127.0.0.1:8080/backend-api/codex/responses)"; send Stop; }
echo "args=$*"
grep -q 'requires_openai_auth = true' "$CODEX_HOME/config.toml" && echo config=ok
grep -q 'not-a-credential' "$CODEX_HOME/auth.json" && echo login=placeholder
echo "token=$OPENAI_API_KEY$CODEX_API_KEY$CLAUDE_CODE_OAUTH_TOKEN"
echo "models=$(api https://127.0.0.1:8080/backend-api/codex/models)"
echo "plugins=$(api https://127.0.0.1:8080/backend-api/ps/plugins/list)"
echo "session=$sid source=$src"
# Like Codex 0.167.1: SessionStart comes with the first prompt, not at start.
[ -n "$prompt" ] && turn
while IFS= read -r line; do
  case "$line" in
    commit*) cd /workspace && echo codex > codex.txt && git add codex.txt && git commit -qm "feat: codex work" && echo "committed" ;;
    mcp\ *) tool=${line#mcp }; tool=${tool%% *}; args=${line#mcp $tool }
      { printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fake","version":"1"}}}' \
          '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
          "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}"
        sleep 8; } | /opt/agent-office/agent-office mcp | grep '"id":2' | sed 's/^/mcp-reply=/' ;;
    foreign*) send UserPromptSubmit 00000000-0000-4000-8000-000000000000; echo foreign-sent ;;
    exit*) send SessionEnd; exit 0 ;;
    *) echo "got: $line"; turn ;;
  esac
done
`

// codexFixture is sandboxFixture with Codex set up: a fake Codex install, a
// login on the host and a fake ChatGPT backend that checks the real token.
type codexFixture struct {
	*fixture
	mu       sync.Mutex
	upstream []string // "METHOD path auth=<real|other>"
}

func newCodexFixture(t *testing.T, clis ...map[string]string) *codexFixture {
	t.Helper()
	f := sandboxFixture(t, false)
	cf := &codexFixture{fixture: f}
	dir := t.TempDir()
	for name, body := range map[string]string{"codex": fakeCodexScript, "codex-code-mode-host": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const real = "real-chatgpt-access-token-for-engine-tests"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := "other"
		if r.Header.Get("Authorization") == "Bearer "+real && r.Header.Get("Chatgpt-Account-Id") == "acct-test" {
			who = "real"
		}
		cf.mu.Lock()
		cf.upstream = append(cf.upstream, r.Method+" "+r.URL.Path+" auth="+who)
		cf.mu.Unlock()
		if r.URL.Path == "/backend-api/codex/responses" {
			w.Header()["Content-Type"] = nil // like ChatGPT's backend: no Content-Type on the stream
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_`+time.Now().Format("150405.000000000")+
				`","model":"gpt-6.1-sol","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":400},"output_tokens":50}}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(up.Close)
	upURL, _ := url.Parse(up.URL)
	ca, err := sandbox.NewLocalCA()
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Sandbox.Codex = &CodexOptions{Dir: dir, Version: "0.167.1", CA: ca, upstream: upURL,
		Login: func() (sandbox.CodexLogin, error) {
			return sandbox.CodexLogin{Token: secret.New(real), Account: "acct-test", Plan: "plus"}, nil
		}}
	use := map[string]string{"worker": role.CLICodex}
	if len(clis) > 0 {
		use = clis[0]
	}
	roles, err := role.MustBuiltins().WithCLIs(use)
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Roles = roles
	return cf
}

func (cf *codexFixture) calls() []string {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	return append([]string(nil), cf.upstream...)
}

// TestSandboxedCodexWorker: a Codex worker in a real Podman worker (ADR
// 0049). The container holds only the placeholder login; the API socket, over
// TLS with the daemon's CA, puts the real token on and refuses other routes;
// the first SessionStart names Codex's session, other sessions' hooks are
// refused; typed messages start turns; usage is metered; the team MCP tools
// work through the bridge; a resume continues Codex's own session id.
func TestSandboxedCodexWorker(t *testing.T) {
	cf := newCodexFixture(t)
	e := cf.engine(t)
	log := record(t, e)
	ctx := context.Background()
	goal, _ := e.CreateTask(ctx, TaskInput{Title: "Ship tags"})
	lead, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 60*time.Second, agentState(lead.ID, hooks.Idle))
	tm := e.Team()
	sub, err := tm.CreateTask(ctx, lead.ID, mcp.CreateTaskInput{Title: "Add tag column"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := tm.AssignTask(ctx, lead.ID, mcp.AssignTaskInput{TaskID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	w := as.AgentID
	log.wait(t, 60*time.Second, agentState(w, hooks.Idle))
	info, _ := e.Get(w)
	if info.CLI != role.CLICodex || info.Model != "" {
		t.Fatalf("worker %+v", info)
	}
	if l, _ := e.Get(lead.ID); l.CLI != role.CLIClaude {
		t.Fatalf("the lead runs on %q", l.CLI)
	}
	out := screen(t, e, w, "responses=")
	for _, want := range []string{"config=ok", "login=placeholder", "token=\n", "models=200", "plugins=403", "responses=200",
		"--dangerously-bypass-approvals-and-sandbox", "--no-daemon", "-- "} {
		if !strings.Contains(out, want) {
			t.Fatalf("worker screen lacks %q:\n%s", want, out)
		}
	}
	calls := cf.calls()
	if len(calls) < 2 || calls[0] != "GET /backend-api/codex/models auth=real" || !strings.Contains(strings.Join(calls, "\n"),
		"POST /backend-api/codex/responses auth=real") {
		t.Fatalf("upstream calls %v", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "plugins") || strings.HasSuffix(c, "auth=other") {
			t.Fatalf("a refused or unauthenticated call reached the upstream: %v", calls)
		}
	}
	codexSID := strings.Fields(out[strings.LastIndex(out, "session=")+len("session="):])[0]

	// A hook of another session on the worker's socket changes nothing.
	typeLine(t, e, w, "foreign")
	screen(t, e, w, "foreign-sent")
	time.Sleep(300 * time.Millisecond)
	if got, _ := e.Get(w); got.State != string(hooks.Idle) {
		t.Fatalf("a foreign session moved the worker to %s", got.State)
	}
	// A typed line is a turn: working, then idle again; its usage is metered.
	from := log.mark()
	typeLine(t, e, w, "please go on")
	log.waitFrom(t, 30*time.Second, from, agentState(w, hooks.Working))
	log.waitFrom(t, 30*time.Second, from, agentState(w, hooks.Idle))
	u := log.wait(t, 30*time.Second, func(u Update) bool { return u.Type == "usage" && u.Usage.AgentID == w && u.Usage.Messages >= 2 })
	if u.Usage.CLI != role.CLICodex || u.Usage.Tokens.CacheRead < 800 || u.Usage.UnpricedTokens == 0 {
		t.Fatalf("usage %+v", u.Usage)
	}

	// The team tools through the in-container bridge, and a report.
	typeLine(t, e, w, "commit")
	screen(t, e, w, "committed")
	typeLine(t, e, w, `mcp report_done {"summary":"codex did it"}`)
	imp := waitImport(t, log, w, TriggerReportDone)
	if imp.Commits != 1 {
		t.Fatalf("import %+v", imp)
	}
	typeLine(t, e, w, "wrap up") // the report's turn ends: the worker exits (ADR 0017)
	log.wait(t, 60*time.Second, func(u Update) bool { return u.Type == "agent" && u.Agent.ID == w && u.Agent.ExitedAt != nil })
	if msgs, _ := e.Messages(ctx, lead.ID); len(msgs) != 1 || !strings.Contains(msgs[0].Body, "codex did it") {
		t.Fatalf("lead messages %+v", msgs)
	}

	// A resume continues Codex's own session id.
	r, err := e.Resume(ctx, w, "")
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 60*time.Second, agentState(w, hooks.Idle))
	out = screen(t, e, w, "session="+codexSID+" source=resume")
	if !strings.Contains(out, "args=resume "+codexSID+" ") {
		t.Fatalf("resume command:\n%s", out)
	}
	if r.ResumedFrom == "" || r.SessionID == info.SessionID {
		t.Fatalf("resumed %+v", r)
	}
	// Nothing of the real token in the container's view.
	inside, err := exec.Command("podman", "exec", sandbox.ContainerName(w), "sh", "-c",
		`cat /proc/[0-9]*/environ /config/*.json /config/*.toml 2>/dev/null; true`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(inside), "real-chatgpt-access-token") {
		t.Fatal("the real Codex token is inside the container")
	}
	// Resumed without a prompt, it sent no hook before exiting: still not a
	// failed resume, since its TUI came up (found in the Phase 15 review).
	exitAgent(t, e, log, w)
	if got, _ := e.Get(w); got.ExitReason == ExitResumeFailed {
		t.Fatalf("a Codex resume that came up ended as %s", got.ExitReason)
	}
}

// TestSandboxedCodexReviewer: a Codex reviewer reviews a Claude Code
// worker's pull request (Phase 15 DoD) through the review loop (ADR 0021):
// the review is posted on (fake) GitHub from the Codex agent's
// submit_review over the in-container bridge.
func TestSandboxedCodexReviewer(t *testing.T) {
	cf := newCodexFixture(t, map[string]string{"reviewer": role.CLICodex})
	f := cf.fixture
	_, srv, _ := withGitHub(t, f)
	e := f.engine(t)
	log := record(t, e)
	ctx := context.Background()
	task, _ := e.CreateTask(ctx, TaskInput{Title: "Add tags"})
	w, err := e.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if w.CLI != role.CLIClaude {
		t.Fatalf("the worker runs on %q", w.CLI)
	}
	log.wait(t, 60*time.Second, agentState(w.ID, hooks.Idle))
	typeLine(t, e, w.ID, "commit")
	screen(t, e, w.ID, "committed 1")
	typeLine(t, e, w.ID, `mcp report_done {"summary":"tags added"}`)
	pr := log.wait(t, 90*time.Second, func(u Update) bool { return u.Type == "pull_request" && u.PullRequest.State == PROpen }).PullRequest
	typeLine(t, e, w.ID, "exit")
	r := log.wait(t, 90*time.Second, func(u Update) bool {
		return u.Type == "agent" && u.Agent.Kind == KindReviewer && u.Agent.State == string(hooks.Idle)
	}).Agent
	if r.CLI != role.CLICodex {
		t.Fatalf("the reviewer runs on %q", r.CLI)
	}
	screen(t, e, r.ID, "responses=200") // its review prompt was its first turn
	typeLine(t, e, r.ID, `mcp submit_review {"verdict":"comment","summary":"looks fine, from Codex","comments":[]}`)
	if out := screen(t, e, r.ID, "mcp-reply="); !strings.Contains(out, `"posted":true`) {
		t.Fatalf("submit_review through the bridge:\n%s", out)
	}
	got := srv.ReviewsOf(1)
	if len(got) != 1 || got[0].CommitID != pr.Head || !strings.Contains(got[0].Body, "looks fine, from Codex") {
		t.Fatalf("posted reviews %+v", got)
	}
}

// TestSandboxedCodexRestore: after a daemon restart, a Codex worker the stop
// interrupted is restored by resuming its own Codex session (ADR 0035, 0049).
func TestSandboxedCodexRestore(t *testing.T) {
	cf := newCodexFixture(t)
	f := cf.fixture
	f.opts.RestoreAgents = true
	ctx := context.Background()
	e1, err := New(ctx, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	log := record(t, e1)
	task, _ := e1.CreateTask(ctx, TaskInput{Title: "Restore me"})
	w, err := e1.Start(ctx, StartRequest{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	log.wait(t, 60*time.Second, agentState(w.ID, hooks.Idle))
	out := screen(t, e1, w.ID, "responses=")
	codexSID := strings.Fields(out[strings.LastIndex(out, "session=")+len("session="):])[0]
	e1.Shutdown()

	e2 := f.engine(t)
	eventually(t, "the Codex worker's restore", func() bool {
		a, _ := e2.Get(w.ID)
		return a.State == string(hooks.Idle) && a.ResumedFrom == w.SessionID
	})
	out = screen(t, e2, w.ID, "session="+codexSID+" source=resume")
	if !strings.Contains(out, "args=resume "+codexSID+" ") {
		t.Fatalf("restore command:\n%s", out)
	}
	// Its first prompt's hooks carry the resumed Codex session: accepted.
	log2 := record(t, e2)
	from := log2.mark()
	typeLine(t, e2, w.ID, "go on")
	log2.waitFrom(t, 30*time.Second, from, agentState(w.ID, hooks.Working))
	log2.waitFrom(t, 30*time.Second, from, agentState(w.ID, hooks.Idle))
	exitAgent(t, e2, log2, w.ID)
}
