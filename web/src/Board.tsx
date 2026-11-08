import { type FormEvent, type JSX, useState } from "react";
import {
  type Agent, type InboxItem, type Plan, type PullRequest, type QARun, type Review, type RoleInfo, type Spec, type Task,
  type TaskStatus, taskStatuses,
} from "./api";
import { InboxEntry, PlanPanel, QAPanel, TaskPlanInfo } from "./Team";
import { currentSpec, inboxFor, latestQA, waitsFor, workerRoles } from "./workflow";
import { type UsageRow, formatMoney, formatTokens, totalTokens } from "./usage";

const columnTitle: Record<TaskStatus, string> = {
  todo: "To do",
  in_progress: "In progress",
  review: "Review",
  done: "Done",
  blocked: "Blocked",
};

interface Props {
  tasks: Task[];
  agents: Agent[];
  canStart: boolean;
  leadRunning: boolean;
  onStartLead: (task: Task) => void;
  onCreate: (title: string, description: string) => Promise<boolean>;
  onStatus: (task: Task, status: TaskStatus) => void;
  onStart: (task: Task) => void;
  onDelete: (task: Task) => void;
  onOpenAgent: (id: string) => void;
  pullRequests: PullRequest[];
  github: boolean; // the daemon runs with --github
  onOpenPR: (task: Task) => void;
  onRequestReview: (pr: PullRequest) => void;
  onSendToWorker: (pr: PullRequest, review: Review) => void;
  onAcceptFlagged: (pr: PullRequest) => void;
  usageByTask?: Map<string, UsageRow>; // token usage per task (ADR 0026)
  usageByGoal?: Map<string, UsageRow>; // per goal: the lead, its workers and their reviewers
  // Phase 12 (ADR 0029-0034).
  roles?: RoleInfo[];
  specs?: Spec[];
  qaRuns?: QARun[];
  inbox?: InboxItem[];
  architectRunning?: boolean;
  onStartRole?: (task: Task, role: string) => void;
  onSaveSpec?: (spec: Spec, plan: Plan) => Promise<boolean>;
  onApproveSpec?: (spec: Spec) => void;
  onResolve?: (task: Task) => void;
  onReadInbox?: (i: InboxItem) => void;
  onReplyInbox?: (i: InboxItem, body: string) => Promise<boolean>;
}

export function Board(p: Props): JSX.Element {
  return (
    <div className="board">
      {taskStatuses.map((status) => (
        <section key={status} className="column">
          <h2>
            {columnTitle[status]} <span className="muted">{p.tasks.filter((t) => t.status === status).length}</span>
          </h2>
          {status === "todo" && <NewTask onCreate={p.onCreate} />}
          {p.tasks
            .filter((t) => t.status === status)
            .map((t) => (
              <TaskCard {...p} key={t.id} task={t} agents={p.agents.filter((a) => a.task_id === t.id)}
                goal={p.tasks.find((g) => g.id === t.parent_task_id)}
                prs={p.pullRequests.filter((pr) => pr.task_id === t.id)} />
            ))}
        </section>
      ))}
    </div>
  );
}

// TaskUsage is a task's tokens and estimated cost; a goal also shows its
// team's (ADR 0026). Nothing when no session has run yet.
function TaskUsage({ own, team }: { own?: UsageRow; team?: UsageRow }): JSX.Element | null {
  const showTeam = team && team.sessions > (own?.sessions ?? 0);
  if (!own && !showTeam) return null;
  const line = (u: UsageRow): string => `${formatTokens(totalTokens(u.tokens))} tokens · ${formatMoney(u.costMicroUSD)}` +
    (u.unpricedTokens > 0 ? " + unpriced" : "");
  return (
    <div className="small muted usage-line" title="estimated from token counts; see the Usage tab">
      {own && <span>{line(own)}</span>}
      {showTeam && <span>{own ? " · " : ""}team {line(team)}</span>}
    </div>
  );
}

function NewTask({ onCreate }: Pick<Props, "onCreate">): JSX.Element {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [open, setOpen] = useState(false);

  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (await onCreate(title, description)) {
      setTitle("");
      setDescription("");
      setOpen(false);
    }
  };

  if (!open) {
    return <button className="new-task" onClick={() => setOpen(true)}>+ New task</button>;
  }
  return (
    <form className="task-form" onSubmit={(e) => void submit(e)}>
      <input autoFocus placeholder="Title" maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} />
      <textarea placeholder="Description (optional)" maxLength={20000} rows={4} value={description}
        onChange={(e) => setDescription(e.target.value)} />
      <div className="row">
        <button className="primary" type="submit" disabled={title.trim() === ""}>Add</button>
        <button type="button" onClick={() => setOpen(false)}>Cancel</button>
      </div>
    </form>
  );
}

interface CardProps extends Props {
  task: Task;
  agents: Agent[];
  goal?: Task;
  prs: PullRequest[];
}

function TaskCard(p: CardProps): JSX.Element {
  const { task, agents, goal, canStart, leadRunning, onStatus, onStart, onStartLead, onDelete, onOpenAgent, prs } = p;
  const running = agents.some((a) => a.state !== "exited" && a.kind !== "reviewer" && a.kind !== "qa");
  const canOpenPR = p.github && task.status === "review" && agents.some((a) => a.kind === "worker") &&
    !prs.some((pr) => pr.state === "open");
  const isGoal = !task.parent_task_id;
  const roles = workerRoles(p.roles ?? []);
  const [role, setRole] = useState(task.role || "worker");
  const spec = isGoal ? currentSpec(p.specs ?? [], task.id) : undefined;
  const qa = isGoal ? latestQA(p.qaRuns ?? [], task.id) : undefined;
  const waiting = waitsFor(task, p.tasks);
  // Unanswered or unread messages to you about this task (ADR 0030).
  const notes = inboxFor(p.inbox ?? [], task).filter((i) => !i.read_at || (!i.answered_at && i.kind === "question")).slice(0, 3);
  return (
    <article className="task" id={`task-${task.id}`} tabIndex={-1}>
      {(goal || task.created_by) && (
        <div className="row small muted">
          {goal && <span title={goal.title}>sub-task of “{goal.title}”</span>}
          {task.created_by && <span className="chip role-lead" title={`Created by agent ${task.created_by}`}>by lead</span>}
        </div>
      )}
      <div className="task-title">{task.title}</div>
      {task.description && <div className="task-desc small muted">{task.description}</div>}
      <TaskPlanInfo task={task} tasks={p.tasks} agents={agents} onResolve={(t) => p.onResolve?.(t)} />
      {spec && <PlanPanel spec={spec} roles={p.roles ?? []} onSave={p.onSaveSpec ?? (async () => false)}
        onApprove={(s) => p.onApproveSpec?.(s)} />}
      {qa && <QAPanel run={qa} onOpenAgent={onOpenAgent} />}
      {notes.length > 0 && (
        <ul className="task-inbox">
          {notes.map((i) => (
            <InboxEntry key={i.id} item={i} onRead={(x) => p.onReadInbox?.(x)}
              onReply={p.onReplyInbox ?? (async () => false)} onOpenAgent={onOpenAgent} />
          ))}
        </ul>
      )}
      {agents.length > 0 && (
        <div className="task-agents">
          {agents.map((a) => (
            <button key={a.id} className={`chip state-${a.state}`} title={`${a.role} · ${a.branch}`}
              onClick={() => onOpenAgent(a.id)}>
              {a.kind !== "worker" ? `${a.role} ` : ""}{a.id.slice(0, 6)}
            </button>
          ))}
        </div>
      )}
      <TaskUsage own={p.usageByTask?.get(task.id)} team={isGoal ? p.usageByGoal?.get(task.id) : undefined} />
      {prs.map((pr) => <PRPanel key={pr.id} pr={pr} {...p} />)}
      <div className="row">
        <select value={task.status} onChange={(e) => onStatus(task, e.target.value as TaskStatus)}>
          {taskStatuses.map((s) => <option key={s} value={s}>{columnTitle[s]}</option>)}
        </select>
        {roles.length > 1 && (
          <select aria-label="Role" value={role} onChange={(e) => setRole(e.target.value)}
            title={roles.find((r) => r.name === role)?.description}>
            {roles.map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
          </select>
        )}
        <button disabled={!canStart || running || waiting.length > 0}
          title={running ? "An agent is already working on this task"
            : waiting.length ? "It starts after the sub-tasks it depends on are reported" : ""}
          onClick={() => (role === "worker" || !p.onStartRole ? onStart(task) : p.onStartRole(task, role))}>
          Start agent
        </button>
        {isGoal && (
          <button disabled={!canStart || running || p.architectRunning}
            title={p.architectRunning ? "An architect is already running"
              : "An architect agrees a plan with you first (read-only); for small goals start the Tech Lead directly"}
            onClick={() => p.onStartRole?.(task, "architect")}>
            Start architect
          </button>
        )}
        {isGoal && (
          <button disabled={!canStart || running || leadRunning}
            title={leadRunning ? "A Tech Lead is already running" : "A Tech Lead executes the goal: it assigns sub-tasks to workers"}
            onClick={() => onStartLead(task)}>
            Start Tech Lead
          </button>
        )}
        {canOpenPR && (
          <button onClick={() => p.onOpenPR(task)} title="Push the worker's branch and open a pull request">Open PR</button>
        )}
        <button className="link" disabled={running} onClick={() => onDelete(task)} title="Delete task">✕</button>
      </div>
    </article>
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
function PRPanel({ pr, onRequestReview, onSendToWorker, onAcceptFlagged, onOpenAgent }: CardProps & { pr: PullRequest }): JSX.Element {
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
                  {r.agent_id && <> <button className="link" onClick={() => onOpenAgent(r.agent_id ?? "")}>{r.agent_id.slice(0, 6)}</button></>}
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
