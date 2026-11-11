// The board (Phase 18): a toolbar (active goals or the archive, the goal
// picker, New task), five columns of compact cards, and the card's modal with
// everything else (TaskDetail).
import { type FormEvent, type JSX, useState } from "react";
import {
  type Agent, type GoalMerge, type InboxItem, type Plan, type PullRequest, type QARun, type Review, type RoleInfo, type Spec, type Task,
  type TaskStatus, taskStatuses,
} from "./api";
import { Archive } from "./Goals";
import { type GoalPick, activeGoals, archivedGoals, boardTasks, validPick } from "./goals";
import { cardAgent, cardStatus, type CardContext } from "./cards";
import { Icon } from "./icons";
import { Modal } from "./Modal";
import { TaskDetail } from "./TaskDetail";
import { type UsageRow, formatMoney } from "./usage";

export const columnTitle: Record<TaskStatus, string> = {
  todo: "To do",
  in_progress: "In progress",
  review: "Review",
  done: "Done",
  blocked: "Blocked",
};

export interface BoardProps {
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
  // Phase 17 (ADR 0054, 0055).
  merges?: GoalMerge[];
  projectId?: string; // the goal picker is remembered per project in this browser
  // Phase 18: the card whose modal is open (App opens one for "Review plan"),
  // and what waits on the user (the Approvals list), for the cards' chips.
  detail?: string;
  onDetail?: (id: string | undefined) => void;
  waitingAgents?: ReadonlySet<string>;
}

// The goal picker's choice, per browser and project: a convenience, so
// storage that is missing or blocked only means "all active goals".
const pickKey = (projectId: string): string => `agent-office.goal.${projectId}`;
function loadPick(projectId?: string): GoalPick {
  if (!projectId) return "all";
  try {
    return window.localStorage.getItem(pickKey(projectId)) ?? "all";
  } catch {
    return "all";
  }
}
function savePick(projectId: string | undefined, pick: GoalPick): void {
  if (!projectId) return;
  try {
    window.localStorage.setItem(pickKey(projectId), pick);
  } catch {
    // not remembered; the board still works
  }
}

export function Board(p: BoardProps): JSX.Element {
  const [picked, setPicked] = useState<GoalPick>(() => loadPick(p.projectId));
  const [archive, setArchive] = useState(false);
  const [creating, setCreating] = useState(false);
  const [ownDetail, setOwnDetail] = useState<string>();
  const detail = p.onDetail ? p.detail : ownDetail;
  const setDetail = p.onDetail ?? setOwnDetail;
  const pick = validPick(p.tasks, picked);
  const shown = boardTasks(p.tasks, pick);
  const goals = activeGoals(p.tasks);
  const done = archivedGoals(p.tasks).length;
  const onPick = (v: GoalPick): void => {
    setPicked(v);
    savePick(p.projectId, v);
  };
  const ctx: CardContext = {
    tasks: p.tasks, agents: p.agents, qaRuns: p.qaRuns ?? [], merges: p.merges ?? [], specs: p.specs ?? [], inbox: p.inbox ?? [],
    waitingAgents: p.waitingAgents ?? new Set(),
  };
  const open = p.tasks.find((t) => t.id === detail);
  return (
    <div className="board-view">
      <div className="board-toolbar">
        <div className="segmented" role="group" aria-label="Goals">
          <button aria-pressed={!archive} onClick={() => setArchive(false)}>Active</button>
          <button aria-pressed={archive} onClick={() => setArchive(true)}
            title="Done goals with their sub-tasks, agents, cost and QA result">Archive ({done})</button>
        </div>
        {!archive && (
          <select aria-label="Goal" value={pick} onChange={(e) => onPick(e.target.value)}>
            <option value="all">All active goals ({goals.length})</option>
            {goals.map((g) => <option key={g.id} value={g.id}>{g.title}</option>)}
          </select>
        )}
        <span className="spacer" />
        {!archive && (
          <button className="primary" onClick={() => setCreating(true)}><Icon name="plus" size={16} />New task</button>
        )}
      </div>
      {archive ? (
        <Archive tasks={p.tasks} qaRuns={p.qaRuns ?? []} merges={p.merges ?? []} usageByGoal={p.usageByGoal}
          onOpenAgent={p.onOpenAgent} onReopen={(g) => p.onStatus(g, "review")} />
      ) : (
        <div className="board">
          {taskStatuses.map((status) => {
            const cards = shown.filter((t) => t.status === status);
            return (
              <section key={status} className="column" data-status={status} aria-label={columnTitle[status]}>
                <h2>{columnTitle[status]} <span className="count">{cards.length}</span></h2>
                {cards.map((t) => (
                  <TaskCard key={t.id} task={t} ctx={ctx} onOpen={() => setDetail(t.id)}
                    usage={t.parent_task_id ? p.usageByTask?.get(t.id) : p.usageByGoal?.get(t.id) ?? p.usageByTask?.get(t.id)} />
                ))}
                {cards.length === 0 && <p className="column-empty">Nothing here</p>}
              </section>
            );
          })}
        </div>
      )}
      {open && <TaskDetail {...p} task={open} onClose={() => setDetail(undefined)} />}
      {creating && <NewTask onCreate={p.onCreate} onClose={() => setCreating(false)} />}
    </div>
  );
}

// TaskCard is a compact card: title, status chip, the agent on it and the
// cost. The whole card opens its modal.
function TaskCard({ task, ctx, usage, onOpen }: {
  task: Task; ctx: CardContext; usage?: UsageRow; onOpen: () => void;
}): JSX.Element {
  const status = cardStatus(task, ctx);
  const { agent, more } = cardAgent(task, ctx);
  const goal = !task.parent_task_id;
  return (
    <button className={`task ${goal ? "goal" : "sub-task"}`} id={`task-${task.id}`} data-status={status.key}
      aria-haspopup="dialog" onClick={onOpen}>
      <span className="task-title">{task.title}</span>
      <span className="task-meta">
        <span className={`chip status-chip tone-${status.tone}`} title={status.title} data-testid="card-status">{status.label}</span>
        {agent && (
          <span className={`task-agent state-${agent.state}`} title={`${agent.role} · ${agent.name} · ${agent.state}`} data-agent={agent.id}>
            <span className="dot" />
            <span className="task-agent-name">{agent.name || agent.id.slice(0, 6)}</span>
            {more > 0 && <span className="muted">+{more}</span>}
          </span>
        )}
        {usage && usage.costMicroUSD > 0 && (
          <span className="task-cost usage-line" title="estimated cost; see the Usage tab">{formatMoney(usage.costMicroUSD)}</span>
        )}
      </span>
    </button>
  );
}

function NewTask({ onCreate, onClose }: { onCreate: BoardProps["onCreate"]; onClose: () => void }): JSX.Element {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (await onCreate(title, description)) onClose();
  };
  return (
    <Modal title="New task" label="New task" onClose={onClose} className="narrow">
      <form className="task-form" onSubmit={(e) => void submit(e)}>
        <label className="setting"><span>Title</span>
          <input autoFocus placeholder="What should the team do?" maxLength={200} value={title} aria-label="Title"
            onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label className="setting"><span>Description (optional)</span>
          <textarea placeholder="Context, constraints, what done looks like" maxLength={20000} rows={6} value={description}
            aria-label="Description" onChange={(e) => setDescription(e.target.value)} />
        </label>
        <div className="row end">
          <button type="button" onClick={onClose}>Cancel</button>
          <button className="primary" type="submit" disabled={title.trim() === ""}>Add task</button>
        </div>
      </form>
    </Modal>
  );
}
