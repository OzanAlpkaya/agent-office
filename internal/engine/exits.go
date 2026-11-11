package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Exit reasons and self-recovery (ADR 0051). When an agent's process ends,
// the engine records why in plain words (out of memory, a signal, an exit
// code), saves a worker's uncommitted work, resumes an agent that died
// unexpectedly once, and tells the goal's Tech Lead (or the user, when
// nobody is above the agent) what happened and what it can do.

// ExitStoppedByLead: the Tech Lead stopped the worker (stop_agent).
const ExitStoppedByLead = "stopped_by_lead"

// exitReportName is the exit report's file in a sandboxed agent's runtime dir.
const exitReportName = "exit.json"

// maxAutoResumes is how often an agent is resumed without anyone asking.
const maxAutoResumes = 1

// exitInfo is how an agent's process ended.
type exitInfo struct {
	code     int
	signal   string // e.g. "KILL"; "" if it exited by itself
	oom      bool   // the out-of-memory killer ended it
	oomKills int64  // OOM kills in its worker over the session (children included)
	memory   string // the worker's memory limit, e.g. "2g"; "" on the host
}

// hostExit reads a host process's end from its wait error.
func hostExit(err error) exitInfo {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			n := int(ws.Signal())
			return exitInfo{code: 128 + n, signal: sandbox.SignalName(n)}
		}
		return exitInfo{code: ee.ExitCode()}
	}
	if err != nil {
		return exitInfo{code: -1}
	}
	return exitInfo{}
}

// sandboxExit reads a worker's end: the podman client's exit code (the
// container's), sandbox-init's exit report and podman's own state. It runs
// before the container and the runtime dir are removed.
func (e *Engine) sandboxExit(res *sandboxRes, waitErr error, log *slog.Logger) exitInfo {
	x := hostExit(waitErr)
	if x.signal == "" {
		x.signal = sandbox.SignalFromCode(x.code) // podman passes a killed init's 128+n on
	}
	x.memory = res.memory
	r, err := sandbox.ReadExitReport(filepath.Join(res.dir, exitReportName))
	if err != nil {
		log.Warn("worker exit report", "err", err)
	}
	if r != nil {
		x.code, x.signal, x.oomKills = r.Code, r.Signal, r.OOMKills
		x.oom = r.Signal == "KILL" && r.OOMKills > 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if st, err := e.opts.Sandbox.Podman.State(ctx, res.container); err == nil && st.OOMKilled {
		x.oom = true
	} else if err != nil {
		log.Debug("inspect ended worker", "err", err)
	}
	if n := res.oomKills.Load(); n > x.oomKills {
		x.oomKills = n // the running watch saw more than the report (no report at all, say)
	}
	return x
}

// explainExit says in plain words why an agent ended, for the UI and the
// lead. Text only: no agent-written content.
func explainExit(reason string, x exitInfo) string {
	switch reason {
	case ExitProcess:
	case ExitResumeFailed:
		return "The resumed CLI exited before it could load the previous conversation."
	case ExitStopped:
		return "Stopped by the user."
	case ExitStoppedByLead:
		return "Stopped by the Tech Lead."
	default:
		return ""
	}
	var s string
	switch {
	case x.oom:
		limit := "its memory limit"
		if x.memory != "" {
			limit = "its " + x.memory + " memory limit"
		}
		s = fmt.Sprintf("Out of memory: the kernel killed the agent because its worker reached %s (exit code %d). "+
			"Raise the limit (worker_memory in the project's settings, or resources.memory in the stack pack) or "+
			"make the task use less memory, for example go test -p 1.", limit, x.code)
	case x.signal != "":
		s = fmt.Sprintf("Killed by signal %s (exit code %d); agent-office did not stop it.", x.signal, x.code)
		if x.signal == "KILL" {
			s += " No out-of-memory kill was reported, so something outside the agent killed it (podman kill, the system)."
		}
	case x.code != 0:
		s = fmt.Sprintf("The agent CLI exited by itself with exit code %d.", x.code)
	default:
		s = "The agent CLI ended by itself (exit code 0), for example after /exit in its terminal."
	}
	if !x.oom && x.oomKills > 0 {
		s += fmt.Sprintf(" Earlier the out-of-memory killer ended %d process(es) in its worker.", x.oomKills)
	}
	return s
}

// failed reports whether an end is a failure: the process ended on its own
// with an error, a signal or an OOM kill. Stops, reports and the daemon's
// own ends are expected; an exit 0 is the CLI ending by itself.
func failed(reason string, x exitInfo) bool {
	return reason == ExitProcess && (x.code != 0 || x.signal != "" || x.oom)
}

// recordExitDetail persists why the agent ended.
func (e *Engine) recordExitDetailLocked(ctx context.Context, a *agentProc, x exitInfo, log *slog.Logger) {
	a.info.ExitSignal, a.info.OOMKilled, a.info.ExitDetail = x.signal, x.oom, explainExit(a.info.ExitReason, x)
	if err := e.q.SetAgentExitDetail(ctx, db.SetAgentExitDetailParams{ExitSignal: x.signal, OomKilled: x.oom,
		ExitDetail: a.info.ExitDetail, ID: a.info.ID}); err != nil {
		log.Error("persist exit detail", "err", err)
	}
}

// wipMessage is the commit message of saved work.
const wipMessage = "wip: uncommitted work saved by agent-office"

// autosave commits an exited worker's uncommitted changes (ADR 0051) and
// returns the new commit, or "" if there was nothing to save. The exit
// import that follows brings it into the main repository (sandbox).
func (e *Engine) autosave(ctx context.Context, a *agentProc, log *slog.Logger) (string, error) {
	e.mu.Lock()
	info := a.info
	e.mu.Unlock()
	if info.Kind != KindWorker || info.WorktreeStatus == WorktreeRemoved {
		return "", nil
	}
	if info.Sandbox == SandboxPodman {
		if e.opts.Sandbox == nil {
			return "", nil
		}
		a.importMu.Lock()
		defer a.importMu.Unlock()
		n, err := e.workspaceDirty(ctx, info)
		if err != nil || n == 0 {
			return "", err
		}
		if err := e.commitWorkLocked(ctx, info); err != nil {
			return "", err
		}
		var out strings.Builder
		if err := e.helper(ctx, info, sandbox.GitIdentity{}, gitCmd([]string{"rev-parse", "HEAD"}), &out, 128); err != nil {
			return "", err
		}
		return strings.TrimSpace(out.String()), nil
	}
	dirty, err := e.git.IsDirty(ctx, info.WorktreePath)
	if err != nil || !dirty {
		return "", err
	}
	name, email, err := e.git.UserIdentity(ctx, e.project.RepoPath)
	if err != nil {
		return "", err
	}
	return e.git.CommitAll(ctx, info.WorktreePath, name, email, wipMessage, "Co-Authored-By: "+sandbox.CoAuthor(agentName(info.ID)))
}

// afterExit runs once an agent's exit is recorded and its work imported:
// the saved-work notice, the automatic resume and the notifications.
func (e *Engine) afterExit(a *agentProc, x exitInfo, saved string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e.mu.Lock()
	info, closing, fu := a.info, e.closing, a.followup
	same := e.agents[info.ID] == a
	e.mu.Unlock()
	if saved != "" {
		body := fmt.Sprintf("When your last session ended, agent-office committed the changes you had not committed as %s (%q) "+
			"on your branch, so nothing was lost. Amend or squash it as you see fit.", short(saved), wipMessage)
		if _, err := e.sendMessage(ctx, "", info.ID, body, false); err != nil {
			log.Warn("saved-work notice", "err", err)
		}
	}
	if closing || !same || info.Kind == KindQA || info.Kind == KindReviewer || info.Kind == KindAssistant {
		return
	}
	switch info.ExitReason {
	case ExitProcess, ExitResumeFailed:
	default:
		return
	}
	who := e.exitLabel(ctx, info)
	if !failed(info.ExitReason, x) {
		// Ended by itself, or a resume that could not load its conversation:
		// a retry would not help, so whoever is above it decides.
		e.tellAbove(ctx, info, fmt.Sprintf("%s ended without reporting: %s%s", who, info.ExitDetail,
			e.recoverySteps(ctx, info)), log)
		return
	}
	n, err := e.q.AddAutoResume(ctx, info.ID)
	if err != nil {
		log.Error("count automatic resume", "err", err)
		return
	}
	if n <= maxAutoResumes {
		prompt := autoResumePrompt(info.ExitDetail, saved, x.oom)
		if _, err := e.relaunchWith(ctx, info.ID, relaunchSpec{prompt: prompt, resume: true, followup: fu}); err == nil {
			log.Info("agent resumed automatically", "exit_detail", info.ExitDetail)
			e.tellAbove(ctx, info, fmt.Sprintf("%s died unexpectedly: %s agent-office resumed its conversation "+
				"automatically (this happens once per agent)%s. Nothing to do unless it fails again.", who, info.ExitDetail,
				savedNote(saved)), log)
			return
		} else if !errors.Is(err, ErrShuttingDown) {
			log.Warn("automatic resume failed", "err", err)
			e.tellAbove(ctx, info, fmt.Sprintf("%s died unexpectedly: %s agent-office could not resume it automatically: %s.%s",
				who, info.ExitDetail, firstLine(err.Error()), e.recoverySteps(ctx, info)), log)
		}
		return
	}
	e.tellAbove(ctx, info, fmt.Sprintf("%s died again: %s It was already resumed automatically once, so agent-office "+
		"leaves it exited%s.%s", who, info.ExitDetail, savedNote(saved), e.recoverySteps(ctx, info)), log)
}

func savedNote(saved string) string {
	if saved == "" {
		return ""
	}
	return "; its uncommitted work was saved as commit " + short(saved)
}

// autoResumePrompt is what an automatically resumed agent reads first.
func autoResumePrompt(detail, saved string, oom bool) string {
	p := "[agent-office] Your previous session ended unexpectedly: " + detail + " agent-office resumed your conversation " +
		"automatically; this happens only once. Same task, workspace and branch."
	if saved != "" {
		p += " Your uncommitted changes were committed as " + short(saved) + " (" + wipMessage + ")."
	}
	if oom {
		p += " Avoid what used the memory: for example run go test -p 1, test fewer packages at once, or stop background processes."
	}
	return p + " Check where you were (git status, git log, your task) and continue your task from there."
}

// exitLabel names an agent and its task for a notification.
func (e *Engine) exitLabel(ctx context.Context, info AgentInfo) string {
	s := fmt.Sprintf("%s %s", info.Role, teamLabel(info.Name, info.ID))
	if info.TaskID != "" {
		if t, err := e.getTask(ctx, info.TaskID); err == nil {
			s += fmt.Sprintf(" on task %s %q", t.ID, t.Title)
		}
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// recoverySteps names what the lead can do about an exited worker.
func (e *Engine) recoverySteps(ctx context.Context, info AgentInfo) string {
	if info.Kind != KindWorker || info.TaskID == "" {
		return " Resume it from its card when you want it to continue."
	}
	return fmt.Sprintf(" Decide now, yourself: resume_agent(agent_id=%q, message=...) to let it continue (say what "+
		"to do differently), or reclaim_task(task_id=%q) and then assign_task to start a new worker from its branch. "+
		"Ask the user (message_user) only if the fix needs a setting the user owns, such as the worker memory limit.",
		info.ID, info.TaskID)
}

// tellAbove tells whoever is above an agent about it (ADR 0030): a worker's
// running Tech Lead, else the user's inbox.
func (e *Engine) tellAbove(ctx context.Context, info AgentInfo, body string, log *slog.Logger) {
	var lead *agentProc
	goal := ""
	if info.Kind == KindWorker && info.TaskID != "" {
		if t, err := e.getTask(ctx, info.TaskID); err == nil && t.ParentTaskID.Valid {
			goal = t.ParentTaskID.String
			e.mu.Lock()
			lead = e.leadOfGoalLocked(goal)
			e.mu.Unlock()
		}
	}
	if lead != nil {
		if _, err := e.sendMessage(ctx, "", lead.info.ID, truncateRunes(body, MaxMessageLen), false); err == nil {
			return
		} else {
			log.Warn("tell the lead about an exit", "err", err)
		}
	}
	if goal == "" && info.TaskID != "" {
		if t, err := e.getTask(ctx, info.TaskID); err == nil {
			goal = goalOfTask(t)
		}
	}
	if _, err := e.addInbox(ctx, "", info.TaskID, goal, "", InboxInfo, body); err != nil {
		log.Warn("tell the user about an exit", "err", err)
	}
}

// commitWorkLocked commits everything uncommitted in a sandboxed agent's
// repository as one WIP commit (the user's identity plus the agent's
// trailer), through the git helper. The caller holds a.importMu.
func (e *Engine) commitWorkLocked(ctx context.Context, info AgentInfo) error {
	co := sandbox.CoAuthor(agentName(info.ID))
	name, email, err := e.git.UserIdentity(ctx, e.project.RepoPath)
	if err != nil {
		return err
	}
	ident := sandbox.GitIdentity{Name: name, Email: email, CoAuthor: co}
	// No shell: the trailer is an argument. The agent's own hooks do not run.
	for _, cmd := range [][]string{
		{"git", "-c", "core.fsmonitor=false", "add", "--all"},
		{"git", "-c", "core.hooksPath=/dev/null", "commit", "--quiet", "--no-verify",
			"-m", wipMessage, "--trailer", "Co-Authored-By: " + co},
	} {
		if err := e.helper(ctx, info, ident, cmd, io.Discard, 1<<16); err != nil {
			return fmt.Errorf("save work: %w", err)
		}
	}
	return nil
}
