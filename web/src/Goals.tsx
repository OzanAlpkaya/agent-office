// Goals after review (Phase 17, ADR 0054, 0055): the board's goal picker and
// archive, removing a done goal's exited agents, and the Merge and Push
// confirmations on a goal card. Merge and Push run only when the user
// confirms what the daemon's preview shows; the daemon refuses again if
// anything changed since. Agent-written text (the commit message, reports,
// QA summaries) is rendered as text only.
import { type JSX, useEffect, useState } from "react";
import type { GoalMerge, GoalRecord, MergePreview, QARun, Task } from "./api";
import { type GoalPick, activeGoals, archivedGoals, latestMerge, mergeOffered, shortSha } from "./goals";
import { useApi } from "./ProjectContext";
import { type UsageRow, formatMoney } from "./usage";
import { latestQA } from "./workflow";

const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// GoalToolbar picks what the board shows: every active goal, one goal, or the archive.
export function GoalToolbar({ tasks, pick, onPick, archive, onArchive }: {
  tasks: readonly Task[]; pick: GoalPick; onPick: (p: GoalPick) => void; archive: boolean; onArchive: (on: boolean) => void;
}): JSX.Element {
  const goals = activeGoals(tasks);
  const done = archivedGoals(tasks).length;
  return (
    <div className="goal-toolbar row">
      <select aria-label="Goal" value={archive ? "all" : pick} disabled={archive} onChange={(e) => onPick(e.target.value)}>
        <option value="all">All active goals ({goals.length})</option>
        {goals.map((g) => <option key={g.id} value={g.id}>{g.title}</option>)}
      </select>
      <button className={archive ? "active" : ""} onClick={() => onArchive(!archive)} aria-pressed={archive}
        title="Done goals with their sub-tasks, agents, cost and QA result">
        {archive ? "← Back to the board" : `Archive (${done})`}
      </button>
    </div>
  );
}

// MergePanel is the goal card's Merge button and its confirmation.
export function MergePanel({ goal }: { goal: Task }): JSX.Element {
  const api = useApi();
  const [preview, setPreview] = useState<MergePreview | undefined>();
  const [message, setMessage] = useState("");
  const [state, setState] = useState<"closed" | "loading" | "open" | "merging">("closed");
  const [error, setError] = useState("");
  const open = (): void => {
    setState("loading");
    setError("");
    api.mergePreview(goal.id).then((p) => { setPreview(p); setMessage(p.message); setState("open"); },
      (e: unknown) => { setError(errText(e)); setState("closed"); });
  };
  const confirm = (): void => {
    if (!preview) return;
    setState("merging");
    setError("");
    api.mergeGoal(goal.id, { target_head: preview.target_head, source_head: preview.source_head, message }).then(
      () => { setState("closed"); setPreview(undefined); },
      (e: unknown) => { setError(errText(e)); setState("open"); },
    );
  };
  if (state === "closed" || state === "loading") {
    return (
      <div className="merge-panel small">
        <button className="primary" disabled={state === "loading"} onClick={open}
          title="Squash-merge the work QA passed into the project's target branch, after a confirmation">
          {state === "loading" ? "Checking…" : "Merge…"}
        </button>
        {error && <div className="hint" role="alert">{error}</div>}
      </div>
    );
  }
  const p = preview;
  if (!p) return <></>;
  return (
    <div className="merge-panel merge-confirm small" role="dialog" aria-label="Confirm the merge">
      <p className="strong">Squash-merge this goal?</p>
      <dl>
        <dt>Branch</dt><dd className="mono">{p.branch} <span className="muted">at {shortSha(p.source_head)} (QA passed)</span></dd>
        <dt>Into</dt><dd className="mono">{p.target} <span className="muted">at {p.target_head ? shortSha(p.target_head) : "?"}</span></dd>
        {p.ready && <><dt>Changes</dt><dd>{p.files} file{p.files === 1 ? "" : "s"}{p.checked_out ? `; ${p.target} is checked out, its files are updated` : ""}</dd></>}
      </dl>
      {p.ready && p.untested > 0 && (
        <p className="hint" role="note">
          {p.target} has {p.untested} commit{p.untested === 1 ? "" : "s"} QA did not test with. The merge is clean, but
          the combined result was not tested.
        </p>
      )}
      {!p.ready && <p className="hint" role="alert" data-testid="merge-refused">Cannot merge: {p.reason}</p>}
      {p.ready && (
        <label className="setting">
          <span>Commit message (from the Tech Lead's report; edit as you like)</span>
          <textarea rows={6} maxLength={4000} value={message} aria-label="Commit message"
            onChange={(e) => setMessage(e.target.value)} />
        </label>
      )}
      {error && <p className="hint" role="alert">{error}</p>}
      <div className="row">
        {p.ready && (
          <button className="danger" disabled={state === "merging" || message.trim() === ""} onClick={confirm}>
            {state === "merging" ? "Merging…" : `Merge into ${p.target}`}
          </button>
        )}
        <button onClick={() => { setState("closed"); setPreview(undefined); }}>{p.ready ? "Cancel" : "Close"}</button>
      </div>
    </div>
  );
}

// PushPanel is the Push button of a merged goal and its confirmation.
export function PushPanel({ goal, merge }: { goal: Task; merge: GoalMerge }): JSX.Element {
  const api = useApi();
  const [preview, setPreview] = useState<MergePreview | undefined>();
  const [state, setState] = useState<"closed" | "loading" | "open" | "pushing">("closed");
  const [error, setError] = useState("");
  const open = (): void => {
    setState("loading");
    setError("");
    api.mergePreview(goal.id).then((p) => { setPreview(p); setState("open"); }, (e: unknown) => { setError(errText(e)); setState("closed"); });
  };
  const confirm = (): void => {
    setState("pushing");
    setError("");
    api.pushGoal(goal.id).then(() => setState("closed"), (e: unknown) => { setError(errText(e)); setState("open"); });
  };
  return (
    <div className="merge-panel small">
      <div>
        Merged into <span className="mono">{merge.target}</span> as <span className="mono">{shortSha(merge.commit)}</span>
        {merge.pushed_at ? <> · pushed to <span className="mono">{merge.push_remote}</span></> : " · not pushed"}
      </div>
      {!merge.pushed_at && state !== "open" && state !== "pushing" && (
        <button disabled={state === "loading"} onClick={open} title="Push the target branch to the project's remote, after a confirmation">
          {state === "loading" ? "Checking…" : "Push…"}
        </button>
      )}
      {(state === "open" || state === "pushing") && preview && (
        <div className="merge-confirm" role="dialog" aria-label="Confirm the push">
          <p className="strong">Push {merge.target}?</p>
          <dl>
            <dt>Remote</dt><dd className="mono">{preview.remote}{preview.remote_url ? ` (${preview.remote_url})` : " (not set up)"}</dd>
            <dt>Branch</dt><dd className="mono">{merge.target} at {shortSha(merge.commit)} or later</dd>
            <dt>Message</dt><dd className="quote">{merge.message.split("\n")[0]}</dd>
          </dl>
          <div className="row">
            <button className="danger" disabled={state === "pushing"} onClick={confirm}>
              {state === "pushing" ? "Pushing…" : `Push to ${preview.remote}`}
            </button>
            <button onClick={() => setState("closed")}>Cancel</button>
          </div>
        </div>
      )}
      {error && <div className="hint" role="alert">{error}</div>}
    </div>
  );
}

// Archive lists the done goals; one opens into its record.
export function Archive({ tasks, qaRuns, merges, usageByGoal, onOpenAgent, onReopen }: {
  tasks: readonly Task[]; qaRuns: readonly QARun[]; merges: readonly GoalMerge[]; usageByGoal?: Map<string, UsageRow>;
  onOpenAgent: (id: string) => void; onReopen: (goal: Task) => void;
}): JSX.Element {
  const [open, setOpen] = useState<string | undefined>();
  const goals = archivedGoals(tasks);
  const shown = goals.find((g) => g.id === open);
  if (shown) {
    return <GoalRecordView goal={shown} usage={usageByGoal?.get(shown.id)} onBack={() => setOpen(undefined)}
      onOpenAgent={onOpenAgent} onReopen={onReopen} qaRuns={qaRuns} merges={merges} />;
  }
  if (goals.length === 0) return <div className="empty">No done goals yet. A goal moves here when you mark it done.</div>;
  return (
    <ul className="archive" data-testid="archive">
      {goals.map((g) => {
        const qa = latestQA(qaRuns, g.id);
        const m = latestMerge(merges, g.id);
        const u = usageByGoal?.get(g.id);
        return (
          <li key={g.id}>
            <button className="archive-goal" onClick={() => setOpen(g.id)}>
              <span className="task-title">{g.title}</span>
              <span className="small muted">
                done {new Date(g.updated_at).toLocaleDateString()} · {tasks.filter((t) => t.parent_task_id === g.id).length} sub-tasks
                {u ? ` · ${formatMoney(u.costMicroUSD)}` : ""}
                {qa ? ` · QA ${qa.status}` : ""}
                {m ? ` · merged into ${m.target}${m.pushed_at ? ", pushed" : ""}` : ""}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

const endedLabel = (a: GoalRecord["agents"][number]): string =>
  a.state !== "exited" ? a.state : a.exit_detail || (a.exit_reason ?? "").replace(/_/g, " ") || "exited";

// GoalRecordView is one archived goal: everything that worked on it and what came out.
function GoalRecordView({ goal, usage, onBack, onOpenAgent, onReopen, qaRuns, merges }: {
  goal: Task; usage?: UsageRow; onBack: () => void; onOpenAgent: (id: string) => void; onReopen: (goal: Task) => void;
  qaRuns: readonly QARun[]; merges: readonly GoalMerge[];
}): JSX.Element {
  const merged = latestMerge(merges, goal.id);
  const api = useApi();
  const [rec, setRec] = useState<GoalRecord | undefined>();
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    let live = true;
    api.goalRecord(goal.id).then((r) => { if (live) setRec(r); }, (e: unknown) => { if (live) setError(errText(e)); });
    return () => { live = false; };
  }, [api, goal.id, goal.updated_at, reload]);
  const branchOf = (taskId: string): string =>
    rec?.agents.filter((a) => a.task_id === taskId && a.kind === "worker").map((a) => a.branch).at(-1) ?? "";
  const exited = rec?.agents.filter((a) => !a.removed && a.state === "exited").length ?? 0;
  return (
    <article className="goal-record" data-testid="goal-record">
      <div className="row">
        <button className="link" onClick={onBack}>← Archive</button>
        <button onClick={() => onReopen(goal)} title="Move the goal back to review">Reopen</button>
      </div>
      <h2>{goal.title}</h2>
      {goal.description && <p className="task-desc small muted">{goal.description}</p>}
      {error && <p className="hint" role="alert">{error}</p>}
      {!rec && !error && <p className="muted">Loading…</p>}
      {rec && (
        <>
          <p className="small muted">
            Cost: {usage ? formatMoney(usage.costMicroUSD) + (usage.unpricedTokens > 0 ? " + unpriced" : "") : "no sessions"}
            {" · "}{rec.agents.length} agent{rec.agents.length === 1 ? "" : "s"}
          </p>
          <RemoveExited goal={goal} count={exited} onDone={() => setReload((n) => n + 1)} />
          {mergeOffered(goal, qaRuns, merges) && <MergePanel goal={goal} />}
          {merged && <PushPanel goal={goal} merge={merged} />}
          {rec.report && (
            <section>
              <h3>Tech Lead's report</h3>
              <div className="quote">{rec.report.summary}</div>
              {rec.report.commit_message && <pre className="quote mono">{rec.report.commit_message}</pre>}
            </section>
          )}
          {rec.merges.length > 0 && (
            <section>
              <h3>Merges</h3>
              <ul className="small">
                {rec.merges.map((m) => (
                  <li key={m.id}>
                    <span className="mono">{shortSha(m.commit)}</span> into <span className="mono">{m.target}</span>{" "}
                    {new Date(m.created_at).toLocaleString()}{m.pushed_at ? ` · pushed to ${m.push_remote}` : " · not pushed"}
                  </li>
                ))}
              </ul>
            </section>
          )}
          <section>
            <h3>Sub-tasks</h3>
            {rec.sub_tasks.length === 0 ? <p className="small muted">none</p> : (
              <ul className="small">
                {rec.sub_tasks.map((t) => (
                  <li key={t.id}>{t.title} <span className="muted">· {t.status}</span>
                    {branchOf(t.id) && <> · <span className="mono">{branchOf(t.id)}</span></>}</li>
                ))}
              </ul>
            )}
          </section>
          <section>
            <h3>Agents</h3>
            <table className="small record-agents">
              <thead><tr><th>Agent</th><th>Role</th><th>CLI · model</th><th>How it ended</th></tr></thead>
              <tbody>
                {rec.agents.map((a) => (
                  <tr key={a.id}>
                    <td>{a.removed
                      ? <span title={a.id}>{a.name || a.id.slice(0, 6)} <span className="chip">removed</span></span>
                      : <button className="link" onClick={() => onOpenAgent(a.id)}>{a.name || a.id.slice(0, 6)}</button>}</td>
                    <td>{a.role}</td>
                    <td>{a.cli}{a.model ? ` · ${a.model}` : ""}</td>
                    <td>{endedLabel(a)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
          <section>
            <h3>QA</h3>
            {rec.qa_runs.length === 0 ? <p className="small muted">QA did not run</p> : (
              <ul className="small">
                {rec.qa_runs.map((r) => (
                  <li key={r.id}>
                    Run {r.id}: <span className={`chip qa-${r.status}`}>{r.status}</span> on <span className="mono">{r.branch}</span>
                    {r.bugs.length > 0 && ` · ${r.bugs.length} bug${r.bugs.length === 1 ? "" : "s"}`}
                    {r.summary && <div className="quote">{r.summary}</div>}
                    {r.error && <div className="hint">{r.error}</div>}
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}
    </article>
  );
}

// RemoveExited removes a done goal's exited agents in one action, with the
// normal work-preserving removal, then reloads the record.
function RemoveExited({ goal, count, onDone }: { goal: Task; count: number; onDone: () => void }): JSX.Element | null {
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (count === 0) return null;
  return (
    <div className="small goal-cleanup">
      <button disabled={busy} onClick={() => {
        setBusy(true);
        api.removeGoalAgents(goal.id).then((r) => {
          setBusy(false);
          setError(r.kept.map((k) => `${k.name || k.agent_id.slice(0, 6)} kept: ${k.reason ?? ""}`).join(" · "));
          onDone();
        }, (e: unknown) => { setBusy(false); setError(errText(e)); });
      }} title="Removes the goal's exited agents; uncommitted workspaces and branches with commits are kept">
        {busy ? "Removing…" : `Remove ${count} exited agent${count === 1 ? "" : "s"}`}
      </button>
      {error && <div className="hint" role="alert">{error}</div>}
    </div>
  );
}
