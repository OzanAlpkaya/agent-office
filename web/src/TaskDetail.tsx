// TaskDetail is a card's modal (Phase 18): everything about a task and every
// action on it. Agent-written text (descriptions from the lead, plans,
// reports, QA summaries, reviews) is rendered as text only.
import { type JSX, useEffect, useState } from "react";
import { type Agent, type GoalRecord, type PullRequest, type Review, type Task, type TaskStatus, taskStatuses } from "./api";
import { AgentName } from "./AgentName";
import { type BoardProps, columnTitle } from "./Board";
import { type DetailAction, cardStatus, detailActions, supersededTitle } from "./cards";
import { BranchCleanup, MergePanel, PushPanel } from "./Goals";
import { busyOf, closeWarning, goalAgents, latestMerge, mergeOffered, pushOffered, subTasksOf } from "./goals";
import { Modal } from "./Modal";
import { useApi } from "./ProjectContext";
import { InboxEntry, PlanPanel, QAPanel, TaskPlanInfo } from "./Team";
import { type UsageRow, formatMoney, formatTokens, totalTokens } from "./usage";
import { currentSpec, goalBusy, inboxFor, latestQA, workerRoles } from "./workflow";

const stateLabel: Record<Agent["state"], string> = {
  starting: "starting", idle: "idle", working: "working", waiting_permission: "waiting for permission", exited: "exited",
};

export function TaskDetail(p: BoardProps & { task: Task; onClose: () => void }): JSX.Element {
  const { task, onOpenAgent } = p;
  const api = useApi();
  const isGoal = !task.parent_task_id;
  const agents = p.agents.filter((a) => a.task_id === task.id);
  const team = isGoal ? goalAgents(task, p.agents, p.tasks) : agents;
  const goal = p.tasks.find((g) => g.id === task.parent_task_id);
  const subs = isGoal ? subTasksOf(p.tasks, task.id) : [];
  const prs = p.pullRequests.filter((pr) => pr.task_id === task.id);
  const running = agents.some((a) => a.state !== "exited" && a.kind !== "reviewer" && a.kind !== "qa");
  const roles = workerRoles(p.roles ?? []);
  const [role, setRole] = useState(task.role || "worker");
  const spec = isGoal ? currentSpec(p.specs ?? [], task.id) : undefined;
  const qa = isGoal ? latestQA(p.qaRuns ?? [], task.id) : undefined;
  const notes = inboxFor(p.inbox ?? [], task).filter((i) => !i.read_at || (!i.answered_at && i.kind === "question")).slice(0, 5);
  const merges = p.merges ?? [];
  const merged = isGoal ? latestMerge(merges, task.id) : undefined;
  const busy = isGoal ? busyOf(task, p.agents, p.tasks) : 0;
  const otherBusy = isGoal ? goalBusy(task, p.agents, p.tasks) : undefined;
  const [closing, setClosing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const status = cardStatus(task, {
    tasks: p.tasks, agents: p.agents, qaRuns: p.qaRuns ?? [], merges, specs: p.specs ?? [], inbox: p.inbox ?? [],
    waitingAgents: p.waitingAgents ?? new Set(),
  });

  // The Tech Lead's report comes with the goal's record (ADR 0054), once it reported.
  const [record, setRecord] = useState<GoalRecord>();
  const reported = isGoal && (task.status === "review" || task.status === "done");
  useEffect(() => {
    if (!reported) return;
    let live = true;
    api.goalRecord(task.id).then((r) => { if (live) setRecord(r); }, () => { if (live) setRecord(undefined); });
    return () => { live = false; };
  }, [api, task.id, task.status, task.updated_at, reported, merged?.id]);

  const setStatus = (s: TaskStatus): void => {
    if (isGoal && s === "done" && task.status !== "done" && busy > 0) {
      setClosing(true);
      return;
    }
    p.onStatus(task, s);
  };
  const markDone = (): void => p.onStatus(task, "done");

  // Only the actions that fit the task's state (Phase 18 review): nothing greyed out.
  const actions = detailActions(task, {
    tasks: p.tasks, agents: p.agents, leadRunning: p.leadRunning, architectRunning: p.architectRunning ?? false,
    github: p.github, openPR: prs.some((pr) => pr.state === "open"),
  });
  const has = (a: DetailAction): boolean => actions.includes(a);
  const footer = (
    <>
      <label className="inline-field">
        <span className="muted small">Status</span>
        <select aria-label="Status" value={task.status} onChange={(e) => setStatus(e.target.value as TaskStatus)}>
          {taskStatuses.map((s) => <option key={s} value={s}>{columnTitle[s]}</option>)}
        </select>
      </label>
      {has("start-agent") && roles.length > 1 && (
        <select aria-label="Role" value={role} onChange={(e) => setRole(e.target.value)} title={roles.find((r) => r.name === role)?.description}>
          {roles.map((r) => <option key={r.name} value={r.name}>{r.name}{r.cli === "codex" ? " · codex" : ""}</option>)}
        </select>
      )}
      {has("start-agent") && (
        <button className={isGoal ? "" : "primary"} onClick={() => (role === "worker" || !p.onStartRole ? p.onStart(task) : p.onStartRole(task, role))}>
          Start agent
        </button>
      )}
      {has("start-architect") && (
        <button title="An architect agrees a plan with you first (read-only); for small goals start the Tech Lead directly"
          onClick={() => p.onStartRole?.(task, "architect")}>
          Start architect
        </button>
      )}
      {has("start-lead") && (
        <button className="primary" title="A Tech Lead executes the goal: it assigns sub-tasks to workers" onClick={() => p.onStartLead(task)}>
          Start Tech Lead
        </button>
      )}
      {has("open-pr") && <button onClick={() => p.onOpenPR(task)} title="Push the worker's branch and open a pull request">Open PR</button>}
      {has("mark-done") && (
        <button className={mergeOffered(task, p.qaRuns ?? [], merges) ? "" : "primary"} onClick={() => setStatus("done")}>
          Mark done
        </button>
      )}
      <span className="spacer" />
      {has("delete") && (confirmDelete ? (
        <>
          <span className="small">Delete this task?</span>
          <button className="danger" onClick={() => { setConfirmDelete(false); p.onClose(); p.onDelete(task); }}>Delete</button>
          <button onClick={() => setConfirmDelete(false)}>Keep</button>
        </>
      ) : (
        <button className="ghost" onClick={() => setConfirmDelete(true)} title="Delete task">Delete…</button>
      ))}
    </>
  );

  return (
    <Modal label={task.title} onClose={p.onClose} footer={footer} className="task-detail"
      title={<><span className="detail-title">{task.title}</span>
        <span className={`chip status-chip tone-${status.tone}`} title={status.title} data-testid="detail-status">{status.label}</span></>}>
      <div className="detail-meta row small muted">
        <span>{isGoal ? "Goal" : "Sub-task"}</span>
        {goal && <span>of <span className="strong">“{goal.title}”</span></span>}
        {task.created_by && <span title={`Created by agent ${task.created_by}`}>· created by <AgentName id={task.created_by} /></span>}
        <span>· {columnTitle[task.status]}</span>
      </div>
      {task.superseded_by && (
        <p className="callout small superseded" role="note" data-testid="superseded">
          {supersededTitle(task, p.tasks)}.{" "}
          {p.tasks.some((t) => t.id === task.superseded_by) && p.onDetail && (
            <button className="link" onClick={() => p.onDetail?.(task.superseded_by)}>Open it</button>
          )}
        </p>
      )}
      {otherBusy && !running && (
        <p className="callout warn small goal-busy" role="note">
          {otherBusy.text} <button className="link" onClick={() => onOpenAgent(otherBusy.agentId)}>Open it</button>
        </p>
      )}
      {closing && (
        <div className="callout warn close-confirm" role="dialog" aria-label="Mark the goal done">
          <p>{closeWarning(busy)}</p>
          <div className="row">
            <button className="danger" onClick={() => { setClosing(false); markDone(); }}>Mark done and stop them</button>
            <button onClick={() => setClosing(false)}>Cancel</button>
          </div>
        </div>
      )}
      <section className="modal-section">
        <h3>Description</h3>
        {task.description ? <div className="quote task-desc">{task.description}</div> : <p className="muted small">No description.</p>}
        <TaskPlanInfo task={task} tasks={p.tasks} agents={agents} onResolve={(t) => p.onResolve?.(t)} open />
      </section>
      {spec && (
        <section className="modal-section">
          <h3>Plan</h3>
          <PlanPanel spec={spec} roles={p.roles ?? []} onSave={p.onSaveSpec ?? (async () => false)} onApprove={(s) => p.onApproveSpec?.(s)} />
        </section>
      )}
      {notes.length > 0 && (
        <section className="modal-section">
          <h3>Messages to you</h3>
          <ul className="task-inbox">
            {notes.map((i) => (
              <InboxEntry key={i.id} item={i} onRead={(x) => p.onReadInbox?.(x)} onReply={p.onReplyInbox ?? (async () => false)}
                onOpenAgent={onOpenAgent} />
            ))}
          </ul>
        </section>
      )}
      {qa && (
        <section className="modal-section">
          <h3>QA</h3>
          <QAPanel run={qa} onOpenAgent={onOpenAgent} open />
        </section>
      )}
      {isGoal && (mergeOffered(task, p.qaRuns ?? [], merges) || merged) && (
        <section className="modal-section">
          <h3>Merge</h3>
          <MergePanel goal={task} offered={mergeOffered(task, p.qaRuns ?? [], merges)} busy={busy} onMarkDone={markDone} />
          {merged && (pushOffered(merges, task.id) || merged.pushed_at) && <PushPanel goal={task} merge={merged} />}
        </section>
      )}
      {isGoal && task.status === "done" && (
        <section className="modal-section">
          <h3>Branches</h3>
          <BranchCleanup goal={task} />
        </section>
      )}
      {record?.report && (
        <section className="modal-section">
          <h3>Tech Lead's report</h3>
          <div className="quote">{record.report.summary}</div>
          {record.report.commit_message && <pre className="quote mono commit-message">{record.report.commit_message}</pre>}
        </section>
      )}
      <section className="modal-section">
        <h3>Agents</h3>
        {team.length === 0 ? <p className="muted small">No agent has worked on it yet.</p> : (
          <ul className="detail-agents">
            {team.map((a) => (
              <li key={a.id}>
                <button className="detail-agent" onClick={() => onOpenAgent(a.id)} data-agent={a.id}
                  title={`${a.role} · ${a.name} · ${a.id} · ${a.branch}`}>
                  <span className={`dot state-${a.state}`} />
                  <span className="strong">{a.name || a.id.slice(0, 6)}</span>
                  <span className="muted">{a.role}</span>
                  {a.task_id !== task.id && <span className="muted small">· {p.tasks.find((t) => t.id === a.task_id)?.title}</span>}
                  <span className={`small state-${a.state}`}>{stateLabel[a.state]}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
      {subs.length > 0 && (
        <section className="modal-section">
          <h3>Sub-tasks</h3>
          <ul className="detail-subs small">
            {subs.map((s) => (
              <li key={s.id}>
                <button className="link" onClick={() => p.onDetail?.(s.id)}>{s.title}</button>
                <span className="muted"> · {columnTitle[s.status]}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {prs.length > 0 && (
        <section className="modal-section">
          <h3>Pull requests</h3>
          {prs.map((pr) => <PRPanel key={pr.id} pr={pr} {...p} />)}
        </section>
      )}
      <section className="modal-section">
        <h3>Cost</h3>
        <TaskUsage own={p.usageByTask?.get(task.id)} team={isGoal ? p.usageByGoal?.get(task.id) : undefined} />
      </section>
    </Modal>
  );
}

// TaskUsage is a task's tokens and estimated cost; a goal also shows its
// team's (ADR 0026).
function TaskUsage({ own, team }: { own?: UsageRow; team?: UsageRow }): JSX.Element {
  const showTeam = team && team.sessions > (own?.sessions ?? 0);
  if (!own && !showTeam) return <p className="muted small">No sessions yet.</p>;
  const line = (u: UsageRow): string => `${formatTokens(totalTokens(u.tokens))} tokens · ${formatMoney(u.costMicroUSD)}` +
    (u.unpricedTokens > 0 ? " + unpriced" : "");
  return (
    <div className="small usage-line" title="estimated from token counts; see the Usage tab">
      {own && <span>{line(own)}</span>}
      {showTeam && <span className="muted">{own ? " · " : ""}team {line(team)}</span>}
    </div>
  );
}

const prStateLabel: Record<PullRequest["state"], string> = {
  pending: "not opened", open: "open", merged: "merged", closed: "closed",
};
const checksLabel: Record<string, string> = {
  none: "no checks", pending: "checks running", success: "checks passed", failure: "checks failed",
  unavailable: "checks unavailable",
};
const reviewerLabel: Record<Review["reviewer"], string> = {
  internal: "reviewer agent", coderabbit: "CodeRabbit", human: "person",
};
const verdictLabel: Record<string, string> = {
  approve: "approves", changes_requested: "requests changes", comment: "comments",
};
const followupLabel: Record<string, string> = {
  started: "sent to the worker (relaunched)",
  skipped: "not sent: round limit", refused: "could not be sent",
};

// PRPanel shows a task's pull request, its checks and reviews (ADR 0020-0022).
// Review text is written by agents, bots or people: rendered as text only.
function PRPanel({ pr, onRequestReview, onSendToWorker, onAcceptFlagged, onOpenAgent }: BoardProps & { pr: PullRequest }): JSX.Element {
  const busy = pr.reviews.some((r) => r.status === "queued" || r.status === "running");
  return (
    <div className="pr small">
      <div className="row">
        {pr.url && pr.number
          ? <a href={pr.url} target="_blank" rel="noreferrer noopener">PR #{pr.number}</a>
          : <span>Pull request</span>}
        <span className={`chip pr-${pr.state}`}>{prStateLabel[pr.state]}</span>
        {pr.checks && <span className={`chip checks-${pr.checks}`} title={pr.failed_checks.join(", ")}>{checksLabel[pr.checks]}</span>}
        {pr.rounds > 0 && <span className="muted">{pr.rounds} follow-up round{pr.rounds === 1 ? "" : "s"}</span>}
      </div>
      {pr.error && <div className="hint">{pr.error}</div>}
      {pr.flagged.length > 0 && (
        <div className="hint">
          {pr.flagged.map((f) => <div key={f.commit} className="mono" title={f.problem}>{f.commit.slice(0, 8)}: {f.problem}</div>)}
          <button className="danger" onClick={() => onAcceptFlagged(pr)}
            title="Push these commits even though they lack your identity or the agent trailer">
            Accept and push
          </button>
        </div>
      )}
      {pr.reviews.length > 0 && (
        <details className="events">
          <summary>{reviewSummary(pr.reviews[0])}</summary>
          <ul>
            {pr.reviews.map((r) => (
              <li key={r.id}>
                <div className="muted">
                  {reviewerLabel[r.reviewer]}
                  {r.agent_id && (
                    <> <button className="link" onClick={() => onOpenAgent(r.agent_id ?? "")}><AgentName id={r.agent_id} /></button></>
                  )}
                  {" · "}{r.status === "done" ? verdictLabel[r.verdict ?? ""] ?? r.verdict : r.status.replace("_", " ")}
                  {" · "}<span className="mono">{r.head.slice(0, 8)}</span>
                  {r.url && <> · <a href={r.url} target="_blank" rel="noreferrer noopener">on GitHub</a></>}
                  {r.followup && <> · {followupLabel[r.followup]}</>}
                </div>
                {r.error && <div className="hint">{r.error}</div>}
                {r.summary && <div className="quote">{r.summary}</div>}
                {r.comments.map((c, i) => (
                  <div key={i} className="quote"><span className="mono">{c.path}:{c.line}</span> {c.body}</div>
                ))}
                {r.reply && (
                  <div className="reply">
                    <span className="muted">Worker: no change needed</span>
                    {r.reply_url
                      ? <> · <a href={r.reply_url} target="_blank" rel="noreferrer noopener">on GitHub</a></>
                      : <span className="hint"> · not posted on GitHub (see the daemon log)</span>}
                    <div className="quote">{r.reply}</div>
                  </div>
                )}
                {r.status === "done" && pr.state === "open" && r.followup !== "started" && r.followup !== "messaged" &&
                  r.head === pr.head && (r.verdict !== "approve" || r.comments.length > 0) && (
                  <button onClick={() => onSendToWorker(pr, r)} title="Give this review to the worker as follow-up work">
                    Send to worker
                  </button>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
      {pr.state === "open" && !busy && (
        <button onClick={() => onRequestReview(pr)}>Request review</button>
      )}
    </div>
  );
}

function reviewSummary(r: Review): string {
  const who = reviewerLabel[r.reviewer];
  if (r.status === "queued") return `Review queued (${who}, waiting for a free slot)`;
  if (r.status === "running") return `Review running (${who})`;
  if (r.status !== "done") return `Review ${r.status.replace("_", " ")} (${who})`;
  return `${who} ${verdictLabel[r.verdict ?? ""] ?? "reviewed"}`;
}
