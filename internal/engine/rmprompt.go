package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/agent"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/rmcheck"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// Claude Code's own "dangerous rm" prompts in the sandbox (Phase 20, ADR
// 0065). With bypass permissions on, Claude Code still asks before an rm it
// cannot check, and nobody watches a worker's terminal. Its PermissionRequest
// hook arrives first, and the daemon answers it: allow when every target is
// inside the workspace and not .git (internal/rmcheck); else the goal's Tech
// Lead decides with decide_command while the hook is held; else the prompt
// waits for the user, as before. Deterministic code decides; the lead only
// for its own workers, and only within the time the prompt stays open.

// commandWait is how long the Tech Lead has: Claude Code blocks the prompt
// by itself after 2 minutes, and the hook forwarder waits 115 s. A variable
// for tests.
var commandWait = 100 * time.Second

const (
	commandAsksPerWindow = 6
	commandAskWindow     = 10 * time.Minute
	maxCommandShown      = 500
	maxCommandReason     = 1000
	permissionAutoNote   = "permission_auto"
	permissionLeadNote   = "permission_lead"
)

// commandRequest is a held PermissionRequest the Tech Lead decides.
type commandRequest struct {
	id       int64
	agentID  string
	leadID   string
	taskID   string
	goalID   string
	command  string // the worker's text, one line, bounded
	cwd      string
	decision chan commandDecision // buffered 1; closed never
}

type commandDecision struct {
	approve bool
	reason  string
	by      string // a lead's agent id, or "" when the request ended without a decision
}

// sandboxPermission answers a sandboxed Claude Code agent's PermissionRequest
// for Bash in bypass mode. handled is false when the normal hook path should
// run (the prompt is left to the user).
func (e *Engine) sandboxPermission(ctx context.Context, id string, payload []byte, ev agent.Event, log *slog.Logger) (reply []byte, handled bool) {
	in, ok := agent.ClaudePermissionInput(payload)
	if !ok || in.Mode != "bypassPermissions" {
		return nil, false
	}
	e.mu.Lock()
	a, found := e.agents[id]
	if !found || a.info.CLI == role.CLICodex || a.info.Sandbox != SandboxPodman || a.sandbox == nil || !a.running() ||
		e.checkSessionLocked(ctx, a, &ev, log) != nil {
		e.mu.Unlock()
		return nil, false
	}
	hostWS := a.info.WorktreePath
	e.mu.Unlock()

	r := rmcheck.Check(in.Command, in.Cwd, sandbox.WorkspaceDir, hostLstat(hostWS))
	if r.Auto {
		e.mu.Lock()
		e.applyLocked(ctx, a, agent.Event{Kind: agent.Other, SessionID: a.info.SessionID, Hook: ev.Hook, Tool: ev.Tool,
			Detail: permissionAutoNote, Subagent: ev.Subagent}, log)
		e.mu.Unlock()
		log.Info("dangerous rm prompt approved: every target is inside the workspace", "targets", len(r.Targets))
		return permissionReply(true, ""), true
	}
	if !rmcheck.MentionsRm(in.Command) {
		return nil, false // not Claude Code's rm check: the user answers it
	}
	if len([]rune(oneLine(in.Command, maxCommandShown+1))) > maxCommandShown {
		// The lead must see all of what it approves (security review): a longer
		// command is left to the user, who sees it in the terminal.
		log.Info("dangerous rm prompt left to the user: the command is too long to show the Tech Lead")
		return nil, false
	}
	req, lead := e.holdForLead(ctx, a, in, ev, log)
	if req == nil {
		log.Info("dangerous rm prompt left to the user", "why", r.Why)
		return nil, false
	}
	defer e.releaseCommand(req)
	who, cmdLine := e.labelOf(req.agentID), req.command
	body := fmt.Sprintf("Command request #%d from worker %s: Claude Code asks before it runs this command, because it "+
		"cannot check what rm removes (%s). The command, written by the worker: %s (in %s). Decide within %d seconds: "+
		"call decide_command(request_id=%d, approve=true or false, reason=\"...\"). Approve only when the rm removes nothing "+
		"the sub-task needs; the worker runs in its sandbox, and the user sees your decision and reason.",
		req.id, who, r.Why, jsonString(cmdLine), jsonString(req.cwd), int(commandWait/time.Second), req.id)
	if _, err := e.sendMessage(ctx, "", lead, body, false); err != nil {
		log.Warn("ask the lead about a command", "request_id", req.id, "err", err)
		return nil, false
	}
	log.Info("dangerous rm prompt sent to the Tech Lead", "request_id", req.id, "lead", lead, "why", r.Why)

	var d commandDecision
	select {
	case d = <-req.decision:
	case <-time.After(commandWait):
	case <-ctx.Done():
	}
	// Closed under the lock: a decision sent before this still counts, and
	// decide_command sees the request gone from now on (code review).
	e.mu.Lock()
	delete(e.commands, req.id)
	if d == (commandDecision{}) {
		select {
		case d = <-req.decision:
		default:
		}
	}
	e.mu.Unlock()
	if ctx.Err() != nil && d.by == "" {
		return nil, true // the daemon stops or the hook gave up: the prompt stays for the user
	}
	nctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg, note string
	switch {
	case d.by == "" && d.reason != "":
		return nil, true // ended without a decision (answered in the terminal): nothing to reply
	case d.by == "":
		msg = "agent-office: not approved in time. Use literal paths inside /workspace for rm, or ask your Tech Lead."
		note = fmt.Sprintf("Nobody decided worker %s's command in time; it was refused: %s", who, jsonString(cmdLine))
	case d.approve:
		note = fmt.Sprintf("Tech Lead %s allowed worker %s's command %s. Its reason: %s", e.labelOf(d.by), who,
			jsonString(cmdLine), jsonString(d.reason))
	default:
		msg = "agent-office: the Tech Lead refused this command: " + d.reason
		note = fmt.Sprintf("Tech Lead %s refused worker %s's command %s. Its reason: %s", e.labelOf(d.by), who,
			jsonString(cmdLine), jsonString(d.reason))
	}
	if _, err := e.addInbox(nctx, "", req.taskID, req.goalID, "", InboxInfo, note); err != nil {
		log.Warn("command decision notice", "err", err)
	}
	log.Info("command request decided", "request_id", req.id, "approved", d.approve, "by", d.by)
	return permissionReply(d.approve && d.by != "", msg), true
}

// holdForLead records a pending command request for the running Tech Lead of
// a's goal, or returns nil: not a worker of a goal, no lead running, or a
// request of this worker already waiting.
func (e *Engine) holdForLead(ctx context.Context, a *agentProc, in agent.PermissionInput, ev agent.Event,
	log *slog.Logger) (*commandRequest, string) {
	if a.info.Kind != KindWorker || a.info.TaskID == "" {
		return nil, ""
	}
	t, err := e.getTask(ctx, a.info.TaskID)
	if err != nil || !t.ParentTaskID.Valid {
		return nil, ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lead := e.leadOfGoalLocked(t.ParentTaskID.String)
	if lead == nil || e.agents[a.info.ID] != a || !a.running() {
		return nil, ""
	}
	for _, r := range e.commands {
		if r.agentID == a.info.ID {
			return nil, ""
		}
	}
	// At most commandAsksPerWindow requests per worker per window (security
	// review): a worker cannot flood its lead with crafted commands.
	cutoff := now().Add(-commandAskWindow)
	recent := a.commandAsks[:0]
	for _, t := range a.commandAsks {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	a.commandAsks = recent
	if len(recent) >= commandAsksPerWindow {
		log.Warn("dangerous rm prompt left to the user: too many requests to the Tech Lead", "requests", len(recent))
		return nil, ""
	}
	a.commandAsks = append(a.commandAsks, now())
	e.commandSeq++
	req := &commandRequest{id: e.commandSeq, agentID: a.info.ID, leadID: lead.info.ID, taskID: t.ID, goalID: t.ParentTaskID.String,
		command: oneLine(in.Command, maxCommandShown), cwd: oneLine(in.Cwd, 200), decision: make(chan commandDecision, 1)}
	e.commands[req.id] = req
	// The worker waits for its lead, not for the user: no "needs you", and a
	// permission_prompt notification while it waits changes nothing.
	e.applyLocked(ctx, a, agent.Event{Kind: agent.Other, SessionID: a.info.SessionID, Hook: ev.Hook, Tool: ev.Tool,
		Detail: permissionLeadNote, Subagent: ev.Subagent}, log)
	a.status.Activity = "Bash: the Tech Lead decides"
	e.publishAgentLocked(a)
	return req, lead.info.ID
}

// releaseCommand forgets a request once its hook is answered.
func (e *Engine) releaseCommand(req *commandRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.commands, req.id)
	if a, ok := e.agents[req.agentID]; ok && a.status.Activity == "Bash: the Tech Lead decides" {
		a.status.Activity = "Bash"
		e.publishAgentLocked(a)
	}
}

// waitingForLeadLocked reports whether agent id's prompt is held for its lead.
func (e *Engine) waitingForLeadLocked(id string) bool {
	for _, r := range e.commands {
		if r.agentID == id {
			return true
		}
	}
	return false
}

// endCommandsLocked ends agent id's held request without a decision: the
// prompt was answered in its terminal, or its turn went on.
func (e *Engine) endCommandsLocked(id string) {
	for _, r := range e.commands {
		if r.agentID == id {
			select {
			case r.decision <- commandDecision{reason: "answered in the terminal"}:
			default:
			}
		}
	}
}

func (t team) DecideCommand(ctx context.Context, caller string, in mcp.DecideCommandInput) (mcp.DecideCommandOutput, error) {
	out, err := t.e.decideCommand(ctx, caller, in)
	return out, userErr(err)
}

// decideCommand is the Tech Lead's decision on one of its workers' held commands.
func (e *Engine) decideCommand(ctx context.Context, caller string, in mcp.DecideCommandInput) (mcp.DecideCommandOutput, error) {
	if _, _, err := e.asLead(ctx, caller, mcp.ToolDecideCommand); err != nil {
		return mcp.DecideCommandOutput{}, err
	}
	reason, err := normalizeText(in.Reason, maxCommandReason, "reason")
	if err != nil {
		return mcp.DecideCommandOutput{}, err
	}
	e.mu.Lock()
	req, ok := e.commands[in.RequestID]
	var err2 error
	switch {
	case !ok:
		err2 = invalid("command request #%d is not waiting: it was decided, answered in the worker's terminal, or timed out",
			in.RequestID)
	case req.leadID != caller:
		err2 = forbidden("command request #%d is not one of your workers'", in.RequestID)
	default:
		select {
		case req.decision <- commandDecision{approve: in.Approve, reason: reason, by: caller}:
		default:
			err2 = invalid("command request #%d was already decided", in.RequestID)
		}
	}
	e.mu.Unlock()
	if err2 != nil {
		return mcp.DecideCommandOutput{}, err2
	}
	status, next := "denied", "The worker is told your reason and goes on without the command."
	if in.Approve {
		status, next = "approved", "The command runs in the worker's sandbox."
	}
	return mcp.DecideCommandOutput{RequestID: in.RequestID, Status: status, Next: next}, nil
}

// permissionReply is a PermissionRequest hook's answer (measured with Claude
// Code 2.1.323, Spike F): allow runs the command, deny refuses it and the
// model reads the message.
func permissionReply(allow bool, message string) []byte {
	decision := map[string]any{"behavior": "allow"}
	if !allow {
		decision = map[string]any{"behavior": "deny", "message": message}
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": decision}})
	return b
}

// hostLstat looks at a container path on the host side of the workspace
// mount (a read-only Lstat; nothing in the agent's clone runs).
func hostLstat(hostWS string) rmcheck.Lstat {
	return func(p string) (fs.FileMode, error) {
		if hostWS == "" {
			return 0, fs.ErrPermission
		}
		rel := strings.TrimPrefix(p, sandbox.WorkspaceDir)
		if rel == p {
			return 0, fs.ErrPermission // not under the workspace mount
		}
		fi, err := os.Lstat(filepath.Join(hostWS, filepath.FromSlash(rel)))
		if err != nil {
			return 0, err
		}
		return fi.Mode(), nil
	}
}

// oneLine keeps agent text on one line and bounded, for a message.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)), " ")
	return truncateRunes(s, max)
}
