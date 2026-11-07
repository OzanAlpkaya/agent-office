package engine

import (
	"context"
	"fmt"

	"github.com/OzanAlpkaya/agent-office/internal/github"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// followupRound is the review a relaunched worker was given (ADR 0022). Until
// the worker answers it, report_done needs a commit on top of head, and
// report_no_change answers it without one.
type followupRound struct {
	prID     int64
	reviewID int64
	number   int    // the pull request
	head     string // the branch head the review was about
}

// needsNewCommit refuses a report in a follow-up round whose branch is still
// at the reviewed head. A nil round allows everything.
func (fu *followupRound) needsNewCommit(head string) error {
	if fu == nil || head != fu.head {
		return nil
	}
	return invalid("your branch has no new commit since the review of pull request #%d: commit your change on this "+
		"branch, then call report_done again. If the review needs no change to the code, call report_no_change "+
		"with the reason instead", fu.number)
}

// reportNoChange answers a follow-up round without a commit: the worker says
// why the review needs no change. The reason is stored on the review, posted
// on the pull request (neutralized, as the agent's words) and the task goes
// back to review; no new review starts, since the head did not move. Only a
// worker relaunched for a review may call it, and only with a clean workspace
// still at the reviewed head.
func (e *Engine) reportNoChange(ctx context.Context, caller, reason string) (mcp.ReportNoChangeOutput, error) {
	reason, err := normalizeText(reason, MaxSummaryLen, "reason")
	if err != nil {
		return mcp.ReportNoChangeOutput{}, err
	}
	e.mu.Lock()
	a, err := e.callerLocked(caller)
	var info AgentInfo
	var fu *followupRound
	if err == nil {
		info, fu = a.info, a.followup
		switch {
		case info.Role != RoleWorker:
			err = forbidden("only workers report on a review follow-up")
		case fu == nil:
			err = invalid("report_no_change answers a review you were relaunched to address, and there is none " +
				"open for you; report your work with report_done")
		case a.stopAfterTurn:
			err = invalid("you already reported; end your turn")
		case a.reporting:
			err = invalid("a report call is already running")
		default:
			a.reporting = true
		}
	}
	e.mu.Unlock()
	if err != nil {
		return mcp.ReportNoChangeOutput{}, err
	}
	defer func() {
		e.mu.Lock()
		a.reporting = false
		e.mu.Unlock()
	}()
	if e.gh == nil {
		return mcp.ReportNoChangeOutput{}, ErrNoGitHub
	}
	task, err := e.getTask(ctx, info.TaskID)
	if err != nil {
		return mcp.ReportNoChangeOutput{}, fmt.Errorf("your task: %w", err)
	}
	if task.Status != StatusInProgress && task.Status != StatusBlocked {
		return mcp.ReportNoChangeOutput{}, invalid("your task is %s, not in progress", task.Status)
	}

	// Nothing changed: no uncommitted work, and the branch at the reviewed head.
	var head string
	if info.Sandbox == SandboxPodman {
		n, err := e.workspaceDirty(ctx, info)
		if err != nil {
			return mcp.ReportNoChangeOutput{}, err
		}
		if n > 0 {
			return mcp.ReportNoChangeOutput{}, invalid("%d uncommitted change(s) in your repository: commit them and "+
				"call report_done, or discard them if the review needs no change", n)
		}
		imp, err := e.importAgent(ctx, info.ID, TriggerReportDone)
		if err != nil {
			e.log.Warn("report_no_change import failed", "agent_id", info.ID, "err", err)
			return mcp.ReportNoChangeOutput{}, invalid("agent-office could not import your branch; it is shown to a " +
				"human. Leave your commits as they are and end your turn")
		}
		head = imp.Head
	} else {
		dirty, err := e.git.IsDirty(ctx, info.WorktreePath)
		if err != nil {
			return mcp.ReportNoChangeOutput{}, err
		}
		if dirty {
			return mcp.ReportNoChangeOutput{}, invalid("your worktree has uncommitted changes: commit them and call " +
				"report_done, or discard them if the review needs no change")
		}
		if head, err = e.git.RevParse(ctx, e.project.RepoPath, info.Branch); err != nil {
			return mcp.ReportNoChangeOutput{}, err
		}
	}
	if head != fu.head {
		return mcp.ReportNoChangeOutput{}, invalid("your branch has new commits since the review: report them with report_done")
	}

	rv, err := e.q.SetReviewReply(ctx, db.SetReviewReplyParams{Reply: reason, ReplyUrl: "", UpdatedAt: now(), ID: fu.reviewID})
	if err != nil {
		return mcp.ReportNoChangeOutput{}, fmt.Errorf("record reply: %w", err)
	}
	if _, err := e.setTaskStatus(ctx, task, StatusReview); err != nil {
		return mcp.ReportNoChangeOutput{}, err
	}
	out := mcp.ReportNoChangeOutput{TaskID: task.ID, Status: StatusReview}
	posted, perr := e.opts.GitHub.Client.PostComment(ctx, fu.number, noChangeComment(agentName(info.ID), rv, fu, reason))
	if perr != nil {
		e.log.Warn("post no-change reply", "agent_id", info.ID, "pr", fu.number, "review_id", rv.ID, "err", perr)
	} else if _, err := e.q.SetReviewReply(ctx, db.SetReviewReplyParams{Reply: reason, ReplyUrl: posted.URL,
		UpdatedAt: now(), ID: rv.ID}); err != nil {
		e.log.Warn("record reply url", "review_id", rv.ID, "err", err) // posted once; never posted again
	} else {
		out.Posted, out.URL = true, posted.URL
	}

	e.mu.Lock()
	a.followup = nil // this round is answered
	lead := e.leadOfGoalLocked(task.ParentTaskID.String)
	team := task.ParentTaskID.Valid
	if team {
		a.stopAfterTurn = true
	}
	e.mu.Unlock()
	if lead != nil {
		body := fmt.Sprintf("Report from worker %s on sub-task %q (%s): the review of pull request #%d needs no change; "+
			"moved to review.\n\nReason: %s", agentName(info.ID), task.Title, task.ID, fu.number, reason)
		if _, err := e.sendMessage(ctx, info.ID, lead.info.ID, truncateRunes(body, MaxMessageLen), false); err != nil {
			e.log.Warn("notify lead", "agent_id", info.ID, "lead", lead.info.ID, "err", err)
		}
	}
	e.publishPRByID(ctx, fu.prID)
	out.Next = "Done. End your turn now."
	if !out.Posted {
		out.Next = "Recorded; agent-office could not post it on the pull request, and a human sees it in agent-office. " +
			"End your turn now."
	}
	if team {
		out.Next += " agent-office ends your session when this turn ends."
	}
	e.log.Info("follow-up answered without a change", "agent_id", info.ID, "task_id", task.ID, "pr", fu.number,
		"review_id", rv.ID, "posted", out.Posted)
	return out, nil
}

// noChangeComment is the pull request comment for a worker's "no change
// needed": the reason is the agent's text, neutralized and quoted.
func noChangeComment(agent string, rv db.Review, fu *followupRound, reason string) string {
	what := "the review"
	if rv.Url != "" {
		what = "[the review](" + rv.Url + ")"
	}
	return fmt.Sprintf("**agent-office:** worker %s looked at %s of `%s` and made no change. Its reason:\n\n%s\n\n"+
		"_Written by the agent. agent-office never merges; the decision is yours._",
		agent, what, short(fu.head), github.Quote(github.Neutralize(reason)))
}
