// Goals after review (Phase 17, ADR 0054, 0055; Phase 18): the archive,
// removing a done goal's exited agents, the Merge and Push confirmations (a
// successful Merge offers to mark the goal done), and branch clean-up. Merge,
// Push and deleting branches run only when the user confirms what the
// daemon's preview shows; the daemon refuses again if anything changed since.
// Agent-written text (the commit message, reports, QA summaries) is rendered
// as text only.
import { type JSX, useEffect, useState } from "react";
import type { BranchDeleteResult, BranchInfo, BranchPreview, GoalMerge, GoalRecord, MergePreview, QARun, Task } from "./api";
import { archivedGoals, closeWarning, latestMerge, mergeOffered, shortSha } from "./goals";
import { branchGroups, deleteRequest } from "./branches";
import { Icon } from "./icons";
import { useApi } from "./ProjectContext";
import { type UsageRow, formatMoney } from "./usage";
import { latestQA } from "./workflow";

const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// MergePanel is a goal's Merge button and its confirmation. offered says
// whether the button shows; after a merge the panel stays to offer marking
// the goal done (busy: its agents that would be stopped).
export function MergePanel({ goal, offered = true, busy = 0, onMarkDone }: {
  goal: Task; offered?: boolean; busy?: number; onMarkDone?: () => void;
}): JSX.Element | null {
  const api = useApi();
  const [preview, setPreview] = useState<MergePreview | undefined>();
  const [message, setMessage] = useState("");
  const [state, setState] = useState<"closed" | "loading" | "open" | "merging" | "merged">("closed");
  const [merged, setMerged] = useState<GoalMerge>();
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
      (m) => { setMerged(m); setState("merged"); setPreview(undefined); },
      (e: unknown) => { setError(errText(e)); setState("open"); },
    );
  };
  if (state === "merged" && merged) {
    const done = goal.status === "done";
    return (
      <div className="merge-confirm merge-done" role="dialog" aria-label="Merged">
        <p className="row"><span className="tone-ok chip status-chip">Merged</span>
          <span>into <span className="mono">{merged.target}</span> as <span className="mono">{shortSha(merged.commit)}</span>.</span></p>
        {done ? (
          <p className="muted small" data-testid="merge-goal-done">The goal is done. Its branches can be cleaned up below.</p>
        ) : onMarkDone && (
          <>
            <p className="small">Mark the goal done? It moves to the archive{busy > 0 ? "" : "; its sub-tasks are marked done too"}.</p>
            {busy > 0 && <p className="hint small" role="note">{closeWarning(busy)}</p>}
          </>
        )}
        <div className="row">
          {!done && onMarkDone && (
            <button className={busy > 0 ? "danger" : "primary"} onClick={onMarkDone}>
              <Icon name="check" size={16} />{busy > 0 ? "Mark goal done and stop them" : "Mark goal done"}
            </button>
          )}
          <button onClick={() => { setState("closed"); setMerged(undefined); }}>{done ? "Close" : "Not now"}</button>
        </div>
      </div>
    );
  }
  if (state === "closed" || state === "loading") {
    if (!offered && !error) return null;
    return (
      <div className="merge-panel">
        {offered && (
          <button className="primary" disabled={state === "loading"} onClick={open}
            title="Squash-merge the work QA passed into the project's target branch, after a confirmation">
            <Icon name="merge" size={16} />{state === "loading" ? "Checking…" : "Merge…"}
          </button>
        )}
        {error && <div className="hint small" role="alert">{error}</div>}
      </div>
    );
  }
  const p = preview;
  if (!p) return null;
  return (
    <div className="merge-panel merge-confirm" role="dialog" aria-label="Confirm the merge">
      <p className="strong">Squash-merge this goal?</p>
      <dl>
        <dt>Branch</dt><dd className="mono">{p.branch} <span className="muted">at {shortSha(p.source_head)} (QA passed)</span></dd>
        <dt>Into</dt><dd className="mono">{p.target} <span className="muted">at {p.target_head ? shortSha(p.target_head) : "?"}</span></dd>
        {p.ready && <><dt>Changes</dt><dd>{p.files} file{p.files === 1 ? "" : "s"}{p.checked_out ? `; ${p.target} is checked out, its files are updated` : ""}</dd></>}
      </dl>
      {p.ready && p.sensitive.length > 0 && (
        <div className="hint small" role="note" data-testid="merge-sensitive">
          Read these first: they run code on CI or in hooks.
          <ul>{p.sensitive.map((f) => <li key={f} className="mono">{f}</li>)}</ul>
        </div>
      )}
      {p.ready && p.paths.length > 0 && (
        <details className="small">
          <summary className="muted">Changed files</summary>
          <ul>{p.paths.map((f) => <li key={f} className="mono">{f}</li>)}</ul>
          {p.files > p.paths.length && <p className="muted">and {p.files - p.paths.length} more</p>}
        </details>
      )}
      {p.ready && p.untested > 0 && (
        <p className="hint small" role="note">
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
            <dt>Pushes</dt><dd className="mono">{shortSha(merge.commit)} to {merge.target} (the merge only; later local commits stay local)</dd>
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

const classLabel: Record<BranchInfo["class"], string> = {
  merged: "merged", squashed: "in the target (squashed)", unmerged: "not merged",
};

// BranchCleanup deletes a done goal's agent-office branches, or with no goal
// every old one of the project (Phase 18). Merged and squashed branches are
// selected; unmerged ones are listed apart, unselected, and need a second
// confirmation that names them. Deleted heads are recorded for restoring.
export function BranchCleanup({ goal, onDone }: { goal?: Task; onDone?: () => void }): JSX.Element {
  const api = useApi();
  const [preview, setPreview] = useState<BranchPreview>();
  const [state, setState] = useState<"closed" | "loading" | "open" | "confirm-unmerged" | "deleting">("closed");
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [result, setResult] = useState<BranchDeleteResult>();
  const [error, setError] = useState("");
  const load = (): void => {
    setState("loading");
    setError("");
    setResult(undefined);
    (goal ? api.goalBranches(goal.id) : api.projectBranches()).then((p) => {
      setPreview(p);
      setChosen(new Set(p.branches.filter((b) => b.class !== "unmerged" && !b.in_use).map((b) => b.name)));
      setState("open");
    }, (e: unknown) => { setError(errText(e)); setState("closed"); });
  };
  const run = (): void => {
    if (!preview) return;
    setState("deleting");
    const req = deleteRequest(preview, chosen);
    (goal ? api.deleteGoalBranches(goal.id, req) : api.deleteProjectBranches(req)).then((r) => {
      setResult(r);
      setPreview(undefined);
      setState("closed");
      onDone?.();
    }, (e: unknown) => { setError(errText(e)); setState("open"); });
  };
  const toggle = (name: string): void => setChosen((s) => {
    const n = new Set(s);
    if (n.has(name)) n.delete(name);
    else n.add(name);
    return n;
  });
  const what = goal ? "this goal's branches" : "old agent-office branches";
  if (state === "closed" || state === "loading") {
    return (
      <div className="branch-cleanup">
        <button disabled={state === "loading"} onClick={load} title={`Delete ${what} whose work is in the target branch`}>
          <Icon name="branch" size={16} />{state === "loading" ? "Checking…" : "Clean up branches…"}
        </button>
        {result && (
          <p className="small" role="status" data-testid="branch-result">
            {result.deleted.length === 0 ? "No branch deleted." : `Deleted ${result.deleted.length} branch${result.deleted.length === 1 ? "" : "es"}.`}
            {result.refused.map((r) => <span key={r.name} className="hint"> {r.name}: {r.reason}.</span>)}
          </p>
        )}
        {error && <p className="hint small" role="alert">{error}</p>}
      </div>
    );
  }
  const p = preview;
  if (!p) return <></>;
  const g = branchGroups(p.branches);
  const unmergedChosen = g.unmerged.filter((b) => chosen.has(b.name));
  const item = (b: BranchInfo): JSX.Element => (
    <li key={b.name} className={`branch-item class-${b.class}`} data-branch={b.name}>
      <label className="row">
        <input type="checkbox" checked={chosen.has(b.name)} disabled={Boolean(b.in_use) || state !== "open"}
          aria-label={`Delete ${b.name}`} onChange={() => toggle(b.name)} />
        <span className="mono">{b.name}</span>
        <span className="muted small">{shortSha(b.head)}</span>
        <span className={`chip small tone-${b.class === "unmerged" ? "warn" : "ok"}`}>{classLabel[b.class]}</span>
        {b.class === "unmerged" && b.ahead > 0 && <span className="small hint">{b.ahead} commit{b.ahead === 1 ? "" : "s"} only here</span>}
        {b.agent_name && <span className="small muted">{b.agent_name}</span>}
        {b.in_use && <span className="small muted">· kept: {b.in_use}</span>}
      </label>
    </li>
  );
  return (
    <div className="branch-cleanup merge-confirm" role="dialog" aria-label="Clean up branches">
      {p.branches.length === 0 ? <p className="muted small">No agent-office branch left{goal ? " for this goal" : ""}.</p> : (
        <>
          <p className="small muted">Compared with <span className="mono">{p.target}</span> at {shortSha(p.target_head)}. Deleted heads are kept in the
            archive, so a branch can be restored with <span className="mono">git branch &lt;name&gt; &lt;sha&gt;</span>.</p>
          {g.safe.length > 0 && <><h4>Work in {p.target} ({g.safe.length})</h4><ul className="branch-list">{g.safe.map(item)}</ul></>}
          {g.unmerged.length > 0 && (
            <>
              <h4 className="hint">Not merged ({g.unmerged.length})</h4>
              <p className="small muted">Their commits are not in {p.target}. Select one only if you are sure you do not need it.</p>
              <ul className="branch-list">{g.unmerged.map(item)}</ul>
            </>
          )}
          {g.inUse.length > 0 && <><h4 className="muted">In use ({g.inUse.length})</h4><ul className="branch-list">{g.inUse.map(item)}</ul></>}
        </>
      )}
      {state === "confirm-unmerged" && (
        <div className="callout danger" role="alert" data-testid="confirm-unmerged">
          <p>Delete {unmergedChosen.length} unmerged branch{unmergedChosen.length === 1 ? "" : "es"}? Their commits are in no other branch:</p>
          <ul>{unmergedChosen.map((b) => <li key={b.name} className="mono">{b.name} ({shortSha(b.head)})</li>)}</ul>
        </div>
      )}
      {error && <p className="hint small" role="alert">{error}</p>}
      <div className="row">
        {state === "confirm-unmerged" ? (
          <>
            <button className="danger" onClick={run}>Yes, delete {chosen.size} including the unmerged</button>
            <button onClick={() => setState("open")}>Back</button>
          </>
        ) : (
          <>
            <button className={unmergedChosen.length > 0 ? "danger" : "primary"} disabled={chosen.size === 0 || state === "deleting"}
              onClick={() => (unmergedChosen.length > 0 ? setState("confirm-unmerged") : run())}>
              {state === "deleting" ? "Deleting…" : `Delete ${chosen.size} branch${chosen.size === 1 ? "" : "es"}`}
            </button>
            <button onClick={() => { setState("closed"); setPreview(undefined); }}>Cancel</button>
          </>
        )}
      </div>
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
              <span className="archive-meta">
                <span className="muted small">done {new Date(g.updated_at).toLocaleDateString()}</span>
                <span className="muted small">· {tasks.filter((t) => t.parent_task_id === g.id).length} sub-tasks</span>
                {u && <span className="muted small">· {formatMoney(u.costMicroUSD)}</span>}
                {qa && <span className={`chip small tone-${qa.status === "passed" ? "ok" : qa.status === "running" ? "accent" : "danger"}`}>QA {qa.status}</span>}
                {m && <span className="chip small tone-violet">{m.pushed_at ? "merged, pushed" : `merged into ${m.target}`}</span>}
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
  }, [api, goal.id, goal.updated_at, reload, merged?.id, merged?.pushed_at]);
  const branchOf = (taskId: string): string =>
    rec?.agents.filter((a) => a.task_id === taskId && a.kind === "worker").map((a) => a.branch).at(-1) ?? "";
  const exited = rec?.agents.filter((a) => !a.removed && a.state === "exited").length ?? 0;
  const deleted = rec?.deleted_branches ?? [];
  return (
    <article className="goal-record" data-testid="goal-record">
      <div className="row">
        <button className="ghost" onClick={onBack}><Icon name="back" size={16} />Archive</button>
        <span className="spacer" />
        <button onClick={() => onReopen(goal)} title="Move the goal back to review">Reopen</button>
      </div>
      <h2>{goal.title}</h2>
      {goal.description && <p className="task-desc muted">{goal.description}</p>}
      {error && <p className="hint" role="alert">{error}</p>}
      {!rec && !error && <p className="muted">Loading…</p>}
      {rec && (
        <>
          <p className="small muted">
            Cost: {usage ? formatMoney(usage.costMicroUSD) + (usage.unpricedTokens > 0 ? " + unpriced" : "") : "no sessions"}
            {" · "}{rec.agents.length} agent{rec.agents.length === 1 ? "" : "s"}
          </p>
          <div className="row">
            <RemoveExited goal={goal} count={exited} onDone={() => setReload((n) => n + 1)} />
            <BranchCleanup goal={goal} onDone={() => setReload((n) => n + 1)} />
          </div>
          {mergeOffered(goal, qaRuns, merges) && <MergePanel goal={goal} />}
          {merged && <PushPanel goal={goal} merge={merged} />}
          {rec.report && (
            <section className="record-section">
              <h3>Tech Lead's report</h3>
              <div className="quote">{rec.report.summary}</div>
              {rec.report.commit_message && <pre className="quote mono commit-message">{rec.report.commit_message}</pre>}
            </section>
          )}
          {rec.merges.length > 0 && (
            <section className="record-section">
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
          <section className="record-section">
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
          <section className="record-section">
            <h3>Agents</h3>
            <table className="table small record-agents">
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
          <section className="record-section">
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
          {deleted.length > 0 && (
            <section className="record-section" data-testid="deleted-branches">
              <h3>Deleted branches</h3>
              <ul className="small">
                {deleted.map((d) => (
                  <li key={d.id}>
                    <span className="mono">{d.name}</span> at <span className="mono">{shortSha(d.head)}</span>{" "}
                    <span className="muted">({classLabel[d.class]}, {new Date(d.deleted_at).toLocaleString()})</span>
                    <div className="muted mono" title="Run this in the repository to restore it">git branch {d.name} {d.head}</div>
                  </li>
                ))}
              </ul>
            </section>
          )}
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
