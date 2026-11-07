import { type FormEvent, type JSX, useState } from "react";
import { type Agent, type Task, type TaskStatus, taskStatuses } from "./api";

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
  onCreate: (title: string, description: string) => Promise<boolean>;
  onStatus: (task: Task, status: TaskStatus) => void;
  onStart: (task: Task) => void;
  onDelete: (task: Task) => void;
  onOpenAgent: (id: string) => void;
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
              <TaskCard {...p} key={t.id} task={t} agents={p.agents.filter((a) => a.task_id === t.id)} />
            ))}
        </section>
      ))}
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
}

function TaskCard({ task, agents, canStart, onStatus, onStart, onDelete, onOpenAgent }: CardProps): JSX.Element {
  const running = agents.some((a) => a.state !== "exited");
  return (
    <article className="task">
      <div className="task-title">{task.title}</div>
      {task.description && <div className="task-desc small muted">{task.description}</div>}
      {agents.length > 0 && (
        <div className="task-agents">
          {agents.map((a) => (
            <button key={a.id} className={`chip state-${a.state}`} title={a.branch} onClick={() => onOpenAgent(a.id)}>
              {a.id.slice(0, 6)}
            </button>
          ))}
        </div>
      )}
      <div className="row">
        <select value={task.status} onChange={(e) => onStatus(task, e.target.value as TaskStatus)}>
          {taskStatuses.map((s) => <option key={s} value={s}>{columnTitle[s]}</option>)}
        </select>
        <button disabled={!canStart || running} title={running ? "An agent is already working on this task" : ""}
          onClick={() => onStart(task)}>
          Start agent
        </button>
        <button className="link" disabled={running} onClick={() => onDelete(task)} title="Delete task">✕</button>
      </div>
    </article>
  );
}
