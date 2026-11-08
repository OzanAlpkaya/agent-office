//go:build e2e

package e2e

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the daemon's database, read-only

	"github.com/OzanAlpkaya/agent-office/internal/usage"
)

// sessionUsage is the part of GET /api/usage the e2e checks.
type sessionUsage struct {
	SessionID       string       `json:"session_id"`
	AgentID         string       `json:"agent_id"`
	Source          string       `json:"source"`
	Messages        int          `json:"messages"`
	Turns           int          `json:"turns"`
	Tokens          usage.Tokens `json:"tokens"`
	CostMicroUSD    int64        `json:"cost_micro_usd"`
	UnpricedTokens  int64        `json:"unpriced_tokens"`
	CLICostMicroUSD *int64       `json:"cli_cost_micro_usd"`
	Models          []string     `json:"models"`
}

func usageOf(t *testing.T, d *daemon) map[string]sessionUsage {
	t.Helper()
	var rep struct {
		Sessions []sessionUsage `json:"sessions"`
	}
	d.json("GET", "/api/usage", nil, 200, &rep)
	out := map[string]sessionUsage{}
	for _, s := range rep.Sessions {
		out[s.SessionID] = s
	}
	return out
}

// hostTranscript finds a host session's transcript in Claude Code's config dir.
func hostTranscript(t *testing.T, sessionID string) string {
	t.Helper()
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".claude")
	}
	m, _ := filepath.Glob(filepath.Join(cfg, "projects", "*", sessionID+".jsonl"))
	if len(m) != 1 {
		t.Fatalf("transcript of session %s: %v", sessionID, m)
	}
	return m[0]
}

// checkHostUsage (Phase 8 DoD): each host session's usage in agent-office
// equals the usage in its session files (main transcript and subagents),
// read here independently. Claude Code's own cost figure is logged next to
// agent-office's estimate.
func checkHostUsage(t *testing.T, d *daemon, sessions []string) {
	t.Helper()
	for _, sid := range sessions {
		s, err := usage.SessionTranscript(hostTranscript(t, sid), sid)
		if err != nil {
			t.Fatal(err)
		}
		var want usage.Tokens
		var n int
		var cli *int64
		var got sessionUsage
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
			r, err := s.ReadNew()
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range r.Messages {
				want = want.Add(m.Tokens) // ReadNew returns only messages new since the last read
				n++
			}
			if r.CLICostMicroUSD != nil {
				cli = r.CLICostMicroUSD
			}
			got = usageOf(t, d)[sid]
			if n > 0 && got.Messages == n && got.Tokens == want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s: agent-office %d messages %+v, transcript %d messages %+v", sid, got.Messages, got.Tokens, n, want)
			}
		}
		if got.Source != "transcript" || got.CostMicroUSD <= 0 || got.UnpricedTokens != 0 {
			t.Errorf("session %s usage %+v", sid, got)
		}
		cliText := "none yet"
		if cli != nil {
			cliText = usd(*cli)
		}
		t.Logf("host session %s: %d messages, %d tokens, models %v, estimate %s, Claude Code's cost-state %s",
			sid[:8], n, want.Total(), got.Models, usd(got.CostMicroUSD), cliText)
	}
}

func usd(micro int64) string { return fmt.Sprintf("$%d.%06d", micro/1_000_000, micro%1_000_000) }

// checkSandboxUsage (Phase 8 DoD): every message in a sandboxed session's
// transcript was metered on its API socket with the same token counts. The
// transcript belongs to the sandbox's subordinate uid; the test reads it
// through podman unshare. Requests Claude Code does not write to the
// transcript are metered too, so the meter may hold more messages. A resumed
// session's transcript repeats its parent's messages (Phase 9), which were
// metered for the parent: name it as parent.
func checkSandboxUsage(t *testing.T, dataDir, agentID, sessionID string, parent ...string) {
	t.Helper()
	cfg := filepath.Join(dataDir, "agents", agentID, "config")
	found, err := exec.Command("podman", "unshare", "find", cfg, "-name", sessionID+".jsonl", "-path", "*/projects/*").Output()
	paths := strings.Fields(string(found))
	if err != nil || len(paths) != 1 {
		t.Fatalf("sandboxed transcript of %s: %v %q", sessionID, err, found)
	}
	raw, err := exec.Command("podman", "unshare", "cat", paths[0]).Output()
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := exec.Command("podman", "unshare", "sh", "-c",
		`cat "$1"/*.jsonl 2>/dev/null; true`, "sh", filepath.Join(filepath.Dir(paths[0]), sessionID, "subagents")).Output()
	tr, err := usage.ReadTranscript(bytes.NewReader(append(raw, subs...)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "agent-office.db")+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metered, own := map[string]usage.Tokens{}, 0
	for _, sid := range append([]string{sessionID}, parent...) {
		rows, err := db.Query(`SELECT message_id, input_tokens, output_tokens, cache_read, cache_write_5m, cache_write_1h
			FROM usage_messages WHERE session_id = ? AND source = 'api'`, sid)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var tk usage.Tokens
			if err := rows.Scan(&id, &tk.Input, &tk.Output, &tk.CacheRead, &tk.CacheWrite5m, &tk.CacheWrite1h); err != nil {
				t.Fatal(err)
			}
			metered[id] = tk
			if sid == sessionID {
				own++
			}
		}
		rows.Close()
	}
	if own == 0 {
		t.Errorf("session %s: nothing metered on its own API socket", sessionID)
	}
	if len(tr.Messages) == 0 {
		t.Fatal("the sandboxed transcript holds no assistant messages")
	}
	var matched int
	for _, m := range tr.Messages {
		got, ok := metered[m.ID]
		switch {
		case !ok:
			t.Errorf("message %s is in the transcript but was not metered", m.ID)
		case got != m.Tokens:
			t.Errorf("message %s: metered %+v, transcript %+v", m.ID, got, m.Tokens)
		default:
			matched++
		}
	}
	t.Logf("sandboxed session %s: %d transcript messages matched the API socket's meter exactly; %d metered in all",
		sessionID[:8], matched, len(metered))
}

// text is the terminal output so far without escape sequences.
func (tm *term) text() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return ansi.ReplaceAllString(tm.buf.String(), "")
}

// checkForkedUsage (Phase 9, ADR 0035): a resumed host session's transcript
// repeats its parent's messages; agent-office counts each message once, so
// both sessions together have exactly the distinct messages of both files.
func checkForkedUsage(t *testing.T, d *daemon, parent, fork string) {
	t.Helper()
	ids := map[string]bool{}
	forkMsgs := 0
	for _, sid := range []string{parent, fork} {
		f, err := os.Open(hostTranscript(t, sid))
		if err != nil {
			t.Fatal(err)
		}
		r, err := usage.ReadTranscript(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range r.Messages {
			ids[m.ID] = true
		}
		if sid == fork {
			forkMsgs = len(r.Messages)
		}
	}
	var p, f sessionUsage
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
		u := usageOf(t, d)
		p, f = u[parent], u[fork]
		if p.Messages+f.Messages == len(ids) && f.Messages > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forked usage: parent %d + fork %d messages, transcripts %d distinct (fork file %d)",
				p.Messages, f.Messages, len(ids), forkMsgs)
		}
	}
	if f.Messages >= forkMsgs {
		t.Errorf("the fork's copied messages were counted again: %d of %d", f.Messages, forkMsgs)
	}
	t.Logf("forked session %s: %d new messages (its transcript holds %d), parent %d", fork[:8], f.Messages, forkMsgs, p.Messages)
}
