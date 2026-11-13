// Team workflow UI (Phase 12, ADR 0029-0034): the inbox, project memory, the
// architect's plan, QA runs and a sub-task's plan and conflicts. Every text
// that agents wrote is rendered as text, never as HTML.
import { type FormEvent, type JSX, useState } from "react";
import type { Agent, InboxItem, MemoryDraft, MemoryEntry, Plan, PlanSubTask, QARun, RoleInfo, Spec, Task } from "./api";
import { AgentName } from "./AgentName";
import { parseScope, scopeLabel } from "./memory";
import {
  type SubTaskForm, fromSubTaskForm, groupInbox, needsAnswer, newSubTask, pendingChanges, proposedSpec, subTaskForm, toForm,
  waitsFor, withSubTask, withoutSubTask, withdrawn,
} from "./workflow";

const timeOf = (iso: string): string => new Date(iso).toLocaleTimeString();

const kindLabel: Record<InboxItem["kind"], string> = {
  question: "asks", blocked: "is blocked", info: "says", briefing: "briefing", plan: "plan",
};

// Who names an inbox item's sender: its role and name (ADR 0043).
function Who({ item: i }: { item: InboxItem }): JSX.Element {
  if (!i.agent_id) return <>agent-office</>;
  return <>{i.role ?? "agent"} <AgentName id={i.agent_id} /></>;
}

// ReplyBox is a one-message form.
function ReplyBox({ label, onSend }: { label: string; onSend: (body: string) => Promise<boolean> }): JSX.Element {
  const [body, setBody] = useState("");
  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (await onSend(body)) setBody("");
  };
  return (
    <form className="reply-box row" onSubmit={(e) => void submit(e)} onClick={(e) => e.stopPropagation()}>
      <input aria-label={label} placeholder={label} maxLength={4000} value={body} onChange={(e) => setBody(e.target.value)} />
      <button type="submit" disabled={body.trim() === ""}>Send</button>
    </form>
  );
}

// InboxEntry is one message to the user, with a reply box when it asks something.
export function InboxEntry({ item: i, taskTitle, onRead, onReply, onOpenAgent, plan, onReviewPlan, onApprovePlan }: {
  item: InboxItem;
  taskTitle?: string;
  onRead: (i: InboxItem) => void;
  onReply: (i: InboxItem, body: string) => Promise<boolean>;
  onOpenAgent: (id: string) => void;
  plan?: Spec; // for a plan notice: the goal's plan version waiting for approval, if any
  onReviewPlan?: (goalId: string) => void;
  onApprovePlan?: (spec: Spec, item: InboxItem) => void;
}): JSX.Element {
  return (
    <li className={`inbox-item kind-${i.kind} ${i.read_at ? "read" : "unread"}`}>
      <div className="small muted row">
        <span>{timeOf(i.created_at)}</span>
        {i.agent_id
          ? <button className="link" onClick={() => onOpenAgent(i.agent_id ?? "")}><Who item={i} /></button>
          : <span><Who item={i} /></span>}
        <span>{kindLabel[i.kind]}</span>
        {taskTitle && <span>· “{taskTitle}”</span>}
        {!i.read_at && <button className="link" onClick={() => onRead(i)}>mark read</button>}
      </div>
      <div className="quote">{i.body}</div>
      {i.reply && <div className="small"><span className="muted">You answered:</span> <span className="quote">{i.reply}</span></div>}
      {i.agent_id && !i.reply && (needsAnswer(i) || i.kind === "briefing") && (
        <ReplyBox label={i.kind === "briefing" ? "Ask the assistant" : "Answer"} onSend={(b) => onReply(i, b)} />
      )}
      {i.kind === "plan" && i.goal_id && (onReviewPlan || (plan && onApprovePlan)) && (
        <div className="row">
          {onReviewPlan && (
            <button onClick={() => onReviewPlan(i.goal_id ?? "")} title="Open the goal's card with its plan">
              Review plan
            </button>
          )}
          {plan && onApprovePlan && (
            <button className="primary" onClick={() => onApprovePlan(plan, i)}
              title="Commit the spec, create the sub-tasks and start a Tech Lead">
              Approve plan v{plan.version}
            </button>
          )}
          {!plan && <span className="muted small">No plan is waiting for approval any more.</span>}
        </div>
      )}
    </li>
  );
}

export function InboxPanel(p: {
  items: InboxItem[];
  tasks: Task[];
  specs: Spec[];
  onRead: (i: InboxItem) => void;
  onReply: (i: InboxItem, body: string) => Promise<boolean>;
  onOpenAgent: (id: string) => void;
  onReviewPlan: (goalId: string) => void;
  onApprovePlan: (spec: Spec, item: InboxItem) => void;
}): JSX.Element {
  const title = (id?: string): string | undefined => p.tasks.find((t) => t.id === id)?.title;
  const view = groupInbox(p.items);
  const entry = (i: InboxItem): JSX.Element => (
    <InboxEntry key={i.id} item={i} taskTitle={i.task_id !== i.goal_id ? title(i.task_id) : undefined} onRead={p.onRead}
      onReply={p.onReply} onOpenAgent={p.onOpenAgent} onReviewPlan={p.onReviewPlan} onApprovePlan={p.onApprovePlan}
      plan={i.kind === "plan" && i.goal_id ? proposedSpec(p.specs, i.goal_id) : undefined} />
  );
  // earlier is a group's answered and read items, collapsed (Phase 16).
  const earlier = (items: InboxItem[]): JSX.Element | null => items.length === 0 ? null : (
    <details className="inbox-earlier">
      <summary>{items.length} earlier</summary>
      <ul>{items.map(entry)}</ul>
    </details>
  );
  return (
    <section className="side-panel inbox" aria-label="Inbox">
      <h2>Inbox</h2>
      <p className="small muted">
        Messages to you from the architect, the assistant, a Tech Lead without an architect, and workers without a lead,
        grouped by goal and agent; answered and read items are folded away. Their text is the agents&apos; own. Your
        answers reach the agent as your words.
      </p>
      {p.items.length === 0 && <p className="muted">Nothing yet.</p>}
      {view.groups.map((g) => (
        <div key={g.goalId || "none"} className="inbox-group" data-goal={g.goalId}>
          <h3>
            {g.goalId ? <>Goal “{title(g.goalId) ?? g.goalId}”</> : "Not about a goal"}
            {g.open > 0 && <span className="badge small">{g.open}</span>}
          </h3>
          {g.agents.map((a) => (
            <div key={a.agentId || "agent-office"} className="inbox-agent" data-agent={a.agentId}>
              <h4>
                {a.agentId
                  ? <button className="link" onClick={() => p.onOpenAgent(a.agentId)}>
                    {(a.open[0] ?? a.earlier[0])?.role ?? "agent"} <AgentName id={a.agentId} />
                  </button>
                  : "agent-office"}
              </h4>
              {a.open.length > 0 && <ul>{a.open.map(entry)}</ul>}
              {earlier(a.earlier)}
            </div>
          ))}
        </div>
      ))}
      {view.briefings.length > 0 && (
        <div className="inbox-briefings" aria-label="Briefings">
          <h3>Briefings</h3>
          <ul>{view.briefings.slice(0, 1).map(entry)}</ul>
          {earlier(view.briefings.slice(1))}
        </div>
      )}
    </section>
  );
}

const memoryKinds: MemoryEntry["kind"][] = ["decision", "convention", "lesson"];

export function MemoryPanel(p: {
  entries: MemoryEntry[];
  onAdd: (d: MemoryDraft) => Promise<boolean>;
  onEdit: (m: MemoryEntry, patch: MemoryDraft) => Promise<boolean>;
  onAccept: (m: MemoryEntry) => void;
  onDelete: (m: MemoryEntry) => void;
}): JSX.Element {
  const [editing, setEditing] = useState<number | "new">();
  return (
    <section className="side-panel memory" aria-label="Project memory">
      <h2>Project memory</h2>
      <p className="small muted">
        Accepted notes are added to the first prompt of the agents they apply to: every agent, or only the roles and
        files you name (an agent&apos;s files are its sub-task&apos;s). Agents can list every note with list_memory. The
        assistant proposes notes; none reaches an agent until you accept it.
      </p>
      <div className="row">
        {editing === "new"
          ? null
          : <button onClick={() => setEditing("new")}>+ Add a note</button>}
        <a className="button-link" href="/api/memory/export" download="memory.md">Export as Markdown</a>
      </div>
      {editing === "new" && (
        <MemoryForm onCancel={() => setEditing(undefined)}
          onSave={async (d) => { const ok = await p.onAdd(d); if (ok) setEditing(undefined); return ok; }} />
      )}
      <ul>
        {p.entries.map((m) => (
          <li key={m.id} className={`memory-entry ${m.status}`}>
            {editing === m.id ? (
              <MemoryForm entry={m} onCancel={() => setEditing(undefined)}
                onSave={async (d) => {
                  const ok = await p.onEdit(m, d);
                  if (ok) setEditing(undefined);
                  return ok;
                }} />
            ) : (
              <>
                <div className="row">
                  <span className="chip">{m.kind}</span>
                  <span className="strong">{m.title}</span>
                  {m.status === "proposed" && <span className="chip hint">proposed by {m.source ? <AgentName id={m.source} /> : "?"}</span>}
                </div>
                <div className="quote small">{m.body}</div>
                <div className="small muted" data-testid="memory-scope">For {scopeLabel(m)}</div>
                <div className="row">
                  {m.status === "proposed" && <button onClick={() => p.onAccept(m)}>Accept</button>}
                  <button onClick={() => setEditing(m.id)}>Edit</button>
                  <button className="danger" onClick={() => p.onDelete(m)}>Delete</button>
                </div>
              </>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function MemoryForm({ entry, onSave, onCancel }: {
  entry?: MemoryEntry;
  onSave: (d: MemoryDraft) => Promise<boolean>;
  onCancel: () => void;
}): JSX.Element {
  const [kind, setKind] = useState<MemoryEntry["kind"]>(entry?.kind ?? "convention");
  const [title, setTitle] = useState(entry?.title ?? "");
  const [body, setBody] = useState(entry?.body ?? "");
  const [roles, setRoles] = useState((entry?.roles ?? []).join(", "));
  const [paths, setPaths] = useState((entry?.paths ?? []).join(", "));
  const save = (): void => {
    // The parent shows errors; a refused save keeps the form open.
    void onSave({ kind, title, body, roles: parseScope(roles), paths: parseScope(paths) });
  };
  return (
    <form className="task-form" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <select aria-label="Kind" value={kind} onChange={(e) => setKind(e.target.value as MemoryEntry["kind"])}>
        {memoryKinds.map((k) => <option key={k} value={k}>{k}</option>)}
      </select>
      <input aria-label="Title" placeholder="Title" maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} />
      <textarea aria-label="Note" placeholder="What the agents should know" maxLength={2000} rows={3} value={body}
        onChange={(e) => setBody(e.target.value)} />
      <input aria-label="Only for roles" placeholder="Only for roles, e.g. worker, qa (empty: every role)" value={roles}
        onChange={(e) => setRoles(e.target.value)} />
      <input aria-label="Only for files" placeholder="Only for files, e.g. internal/store/, web/** (empty: every task)"
        value={paths} onChange={(e) => setPaths(e.target.value)} />
      <div className="row">
        <button className="primary" type="submit" disabled={!title.trim() || !body.trim()}>Save</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

const specLabel: Record<Spec["status"], string> = {
  proposed: "waiting for your approval", approved: "approved", superseded: "superseded",
};

// PlanPanel shows a goal's plan (Phase 19): Approve in one click, a
// "request changes" box the Architect answers with a new version, and light
// edits, one sub-task or the summary at a time, each saved as a new version.
export function PlanPanel({ spec, specs = [], roles, onSave, onApprove, onRequestChanges }: {
  spec: Spec;
  specs?: readonly Spec[]; // every version of the goal, to tell whether a change request is answered
  roles: RoleInfo[];
  onSave: (spec: Spec, plan: Plan) => Promise<boolean>;
  onApprove: (spec: Spec) => void;
  onRequestChanges?: (spec: Spec, changes: string) => Promise<boolean>;
}): JSX.Element {
  const [editing, setEditing] = useState<"summary" | number | "new">();
  const [removing, setRemoving] = useState<string>();
  const p = spec.plan;
  const live = spec.status !== "superseded";
  const pending = pendingChanges(spec, specs);
  const save = async (plan: Plan): Promise<boolean> => {
    const ok = await onSave(spec, plan);
    if (ok) {
      setEditing(undefined);
      setRemoving(undefined);
    }
    return ok;
  };
  const roleNames = roles.filter((r) => r.behaviour === "worker").map((r) => r.name);
  return (
    <details className={`plan small spec-${spec.status}`} open={spec.status === "proposed"} onClick={(e) => e.stopPropagation()}>
      <summary>
        Plan v{spec.version} · {withdrawn(spec) ? "changes requested" : specLabel[spec.status]} · {p.sub_tasks.length} sub-task{p.sub_tasks.length === 1 ? "" : "s"}
        {spec.author ? <> · by architect <AgentName id={spec.author} /></> : " · edited by you"}
      </summary>
      {spec.status === "proposed" && (
        <div className="row plan-actions">
          <button className="primary" onClick={() => onApprove(spec)}
            title="Commit the spec, create the sub-tasks and start a Tech Lead">Approve plan</button>
          <span className="muted">or ask for changes below; small fixes: Edit on the line.</span>
        </div>
      )}
      {editing === "summary" ? (
        <SummaryEditor plan={p} onSave={save} onCancel={() => setEditing(undefined)} />
      ) : (
        <div className="plan-summary">
          <div className="quote">{p.summary}</div>
          {p.design && <details><summary>Design</summary><div className="quote pre">{p.design}</div></details>}
          {p.risks && <div><span className="muted">Risks:</span> <span className="quote">{p.risks}</span></div>}
          {live && <button className="link" onClick={() => setEditing("summary")} aria-label="Edit the summary">Edit summary</button>}
        </div>
      )}
      <ol className="plan-tasks">
        {p.sub_tasks.map((s, i) => (
          <li key={s.key} data-key={s.key}>
            {editing === i ? (
              <SubTaskEditor sub={s} roles={roleNames} keys={p.sub_tasks.map((x) => x.key)}
                onSave={(st) => save(withSubTask(p, i, st))} onCancel={() => setEditing(undefined)} />
            ) : (
              <>
                <span className="mono">{s.key}</span> <span className="strong">{s.title}</span>
                {s.role && <span className="chip"> {s.role}</span>}
                {s.depends_on?.length ? <span className="muted"> · after {s.depends_on.join(", ")}</span> : null}
                {live && (
                  <span className="plan-task-actions">
                    <button className="link" onClick={() => setEditing(i)} aria-label={`Edit ${s.key}`}>Edit</button>
                    {removing === s.key ? (
                      <>
                        <span className="small">Remove “{s.title}”?</span>
                        <button className="link danger" onClick={() => void save(withoutSubTask(p, s.key))}>Remove</button>
                        <button className="link" onClick={() => setRemoving(undefined)}>Keep</button>
                      </>
                    ) : (
                      p.sub_tasks.length > 1 &&
                        <button className="link" onClick={() => setRemoving(s.key)} aria-label={`Remove ${s.key}`}>Remove</button>
                    )}
                  </span>
                )}
                {s.files?.length ? <div className="mono muted">{s.files.join(", ")}</div> : null}
                {s.acceptance?.length ? <ul>{s.acceptance.map((a, j) => <li key={j}>{a}</li>)}</ul> : null}
              </>
            )}
          </li>
        ))}
      </ol>
      {live && (editing === "new" ? (
        <SubTaskEditor sub={fromSubTaskForm(newSubTask(toForm(p)))} roles={roleNames} keys={p.sub_tasks.map((x) => x.key)}
          onSave={(st) => save(withSubTask(p, -1, st))} onCancel={() => setEditing(undefined)} />
      ) : (
        <button className="link" onClick={() => setEditing("new")}>+ Sub-task</button>
      ))}
      {spec.status === "approved" && spec.path && (
        <div className="muted mono">{spec.path} on {spec.branch} ({spec.commit?.slice(0, 8)})</div>
      )}
      {pending && (
        <p className="callout small change-pending" data-testid="change-pending">
          {withdrawn(spec)
            ? <>Changes requested: the Architect is revising this version; you approve the next one. </>
            : <>Changes requested, waiting for the Architect: </>}
          <span className="quote">{pending}</span>
        </p>
      )}
      {live && onRequestChanges && <ChangeRequest spec={spec} onSend={onRequestChanges} />}
    </details>
  );
}

// ChangeRequest is the "request changes" box: the user writes what to change
// and the Architect revises the plan (Phase 19).
function ChangeRequest({ spec, onSend }: { spec: Spec; onSend: (spec: Spec, changes: string) => Promise<boolean> }): JSX.Element {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <form className="change-request" aria-label="Request changes" onSubmit={(e) => {
      e.preventDefault();
      setBusy(true);
      void onSend(spec, text.trim()).then((ok) => {
        setBusy(false);
        if (ok) setText("");
      });
    }}>
      <label className="setting">
        <span>Request changes</span>
        <textarea rows={2} maxLength={4000} value={text} aria-label="What to change"
          placeholder="What should the Architect change? e.g. split the API sub-task, drop the docs" onChange={(e) => setText(e.target.value)} />
      </label>
      <div className="row">
        <button type="submit" disabled={text.trim() === "" || busy}>{busy ? "Sending…" : "Send to the Architect"}</button>
        <span className="muted small">It revises the plan; you approve the new version.</span>
      </div>
    </form>
  );
}

function SummaryEditor({ plan, onSave, onCancel }: {
  plan: Plan; onSave: (p: Plan) => Promise<boolean>; onCancel: () => void;
}): JSX.Element {
  const [summary, setSummary] = useState(plan.summary);
  const [design, setDesign] = useState(plan.design);
  const [risks, setRisks] = useState(plan.risks ?? "");
  return (
    <form className="plan-editor task-form" aria-label="Edit the summary" onSubmit={(e) => {
      e.preventDefault();
      const next: Plan = { ...plan, summary: summary.trim(), design: design.trim() };
      if (risks.trim()) next.risks = risks.trim();
      else delete next.risks;
      void onSave(next);
    }}>
      <label>Summary<textarea rows={2} maxLength={2000} value={summary} onChange={(e) => setSummary(e.target.value)} /></label>
      <details><summary className="muted">Design and risks</summary>
        <label>Design<textarea rows={5} maxLength={20000} value={design} onChange={(e) => setDesign(e.target.value)} /></label>
        <label>Risks<textarea rows={2} maxLength={4000} value={risks} onChange={(e) => setRisks(e.target.value)} /></label>
      </details>
      <div className="row">
        <button className="primary" type="submit">Save as a new version</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

// SubTaskEditor edits one sub-task: title, description and acceptance
// criteria, with the key, role, dependencies and files under "More".
function SubTaskEditor({ sub, roles, keys, onSave, onCancel }: {
  sub: PlanSubTask; roles: string[]; keys: string[]; onSave: (s: PlanSubTask) => Promise<boolean>; onCancel: () => void;
}): JSX.Element {
  const [f, setF] = useState<SubTaskForm>(() => subTaskForm(sub));
  const set = (patch: Partial<SubTaskForm>): void => setF({ ...f, ...patch });
  return (
    <form className="plan-editor task-form sub-task-editor" aria-label={`Edit ${sub.key}`} onSubmit={(e) => {
      e.preventDefault();
      void onSave(fromSubTaskForm(f));
    }}>
      <input aria-label="Title" placeholder="Title" value={f.title} autoFocus onChange={(e) => set({ title: e.target.value })} />
      <textarea aria-label="Description" rows={3} placeholder="What to do and how to check it" value={f.description}
        onChange={(e) => set({ description: e.target.value })} />
      <textarea aria-label="Acceptance criteria" rows={2} placeholder="Acceptance criteria, one per line"
        value={f.acceptance} onChange={(e) => set({ acceptance: e.target.value })} />
      <details><summary className="muted">More: key, role, dependencies, files</summary>
        <div className="row">
          <input aria-label="Key" className="mono spec-key" value={f.key} onChange={(e) => set({ key: e.target.value })} />
          <select aria-label="Role" value={f.role} onChange={(e) => set({ role: e.target.value })}>
            <option value="">worker</option>
            {roles.filter((r) => r !== "worker").map((r) => <option key={r} value={r}>{r}</option>)}
          </select>
        </div>
        <input aria-label="Depends on" placeholder={`Depends on (keys: ${keys.filter((k) => k !== sub.key).join(", ") || "none"})`}
          value={f.dependsOn} onChange={(e) => set({ dependsOn: e.target.value })} />
        <input aria-label="Files" className="mono" placeholder="Files (paths, dirs/, globs; comma separated)" value={f.files}
          onChange={(e) => set({ files: e.target.value })} />
      </details>
      <div className="row">
        <button className="primary" type="submit" disabled={f.title.trim() === ""}>Save as a new version</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

const qaLabel: Record<QARun["status"], string> = {
  running: "QA running", passed: "QA passed", failed: "QA failed", error: "QA ended without a result",
};

export function QAPanel({ run, onOpenAgent, open }: { run: QARun; onOpenAgent: (id: string) => void; open?: boolean }): JSX.Element {
  return (
    <details className={`qa small qa-${run.status}`} open={open} onClick={(e) => e.stopPropagation()}>
      <summary>
        <span className={`chip qa-${run.status}`}>{qaLabel[run.status]}</span> run {run.id} · {run.merged.length} branch
        {run.merged.length === 1 ? "" : "es"} merged{run.bugs.length > 0 && ` · ${run.bugs.length} bug${run.bugs.length === 1 ? "" : "s"}`}
      </summary>
      <div className="mono muted">integration branch {run.branch}{run.head && ` @ ${run.head.slice(0, 8)}`}</div>
      {run.agent_id && (
        <button className="link" onClick={() => onOpenAgent(run.agent_id ?? "")}>QA agent <AgentName id={run.agent_id} /></button>
      )}
      {run.summary && <div className="quote">{run.summary}</div>}
      {run.error && <div className="hint">{run.error}</div>}
      {run.bugs.length > 0 && (
        <ul>
          {run.bugs.map((b, i) => (
            <li key={i}><span className="strong">{b.title}</span>{b.bug_task_id && <span className="muted"> → {b.bug_task_id}</span>}
              <div className="quote">{b.description}</div></li>
          ))}
        </ul>
      )}
    </details>
  );
}

// TaskPlanInfo is a sub-task's dependencies, files, acceptance criteria and
// conflicts (ADR 0032).
export function TaskPlanInfo({ task, tasks, agents, onResolve, open }: {
  task: Task;
  tasks: Task[];
  agents: Agent[];
  onResolve: (t: Task) => void;
  open?: boolean; // the acceptance criteria start open (the card's modal)
}): JSX.Element | null {
  const deps = task.depends_on ?? [];
  const waiting = waitsFor(task, tasks);
  const conflicts = task.conflicts ?? [];
  const title = (id: string): string => tasks.find((t) => t.id === id)?.title ?? id;
  const hasWorker = agents.some((a) => a.task_id === task.id && a.kind === "worker");
  if (!deps.length && !task.files?.length && !task.acceptance?.length && !conflicts.length && !task.role) return null;
  return (
    <div className="task-plan small">
      {task.role && <span className="chip" title="The worker role that builds it">{task.role}</span>}
      {deps.length > 0 && (
        <div className={waiting.length ? "hint" : "muted"}>
          {waiting.length
            ? <>Waits for {waiting.map((t) => `“${t.title}”`).join(", ")}</>
            : <>After {deps.map((d) => `“${title(d)}”`).join(", ")}</>}
        </div>
      )}
      {task.files && task.files.length > 0 && <div className="mono muted" title="Files it touches">{task.files.join(", ")}</div>}
      {task.acceptance && task.acceptance.length > 0 && (
        <details open={open}><summary className="muted">{task.acceptance.length} acceptance criteri{task.acceptance.length === 1 ? "on" : "a"}</summary>
          <ul>{task.acceptance.map((a, i) => <li key={i}>{a}</li>)}</ul></details>
      )}
      {conflicts.map((c, i) => (
        <div key={i} className="conflict hint" role="alert">
          ⚠ Conflicts with {c.with ? <>“{title(c.with)}” (<span className="mono">{c.branch}</span>)</> : <>the base branch <span className="mono">{c.branch}</span></>}
          {" "}in <span className="mono">{c.files.join(", ")}</span>
          {!c.with && hasWorker && (
            <button onClick={(e) => { e.stopPropagation(); onResolve(task); }}
              title="Relaunch the worker to merge the base branch and resolve the conflict">Resolve conflict</button>
          )}
        </div>
      ))}
    </div>
  );
}
