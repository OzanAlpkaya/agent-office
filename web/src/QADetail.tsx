// QA round cards and their modal (Phase 19): what one QA run merged, found and
// cost. QA's summary and bugs are agent-written: rendered as text only.
import type { JSX } from "react";
import type { Agent } from "./api";
import { AgentName } from "./AgentName";
import { Modal } from "./Modal";
import { type QACard, openBugs } from "./qaCards";
import { type UsageRow, formatMoney } from "./usage";

export function QARoundCard({ card, agent, usage, onOpen }: {
  card: QACard; agent?: Agent; usage?: UsageRow; onOpen: () => void;
}): JSX.Element {
  const open = openBugs(card);
  return (
    <button className="task qa-card" id={`qa-${card.run.id}`} data-status={`qa-${card.run.status}`} aria-haspopup="dialog"
      onClick={onOpen}>
      <span className="task-title">QA round {card.round} <span className="muted">· {card.goal.title}</span></span>
      <span className="task-meta">
        <span className={`chip status-chip tone-${card.tone}`} data-testid="card-status"
          title={card.run.status === "failed" ? `${open} of ${card.bugTasks.length} bug sub-task(s) still open` : undefined}>
          {card.label}
        </span>
        {agent && (
          <span className={`task-agent state-${agent.state}`} data-agent={agent.id}>
            <span className="dot" /><span className="task-agent-name">{agent.name || agent.id.slice(0, 6)}</span>
          </span>
        )}
        {usage && usage.costMicroUSD > 0 && (
          <span className="task-cost usage-line" title="this QA run's estimated cost">{formatMoney(usage.costMicroUSD)}</span>
        )}
      </span>
    </button>
  );
}

export function QADetail({ card, usage, onClose, onOpenAgent, onOpenTask }: {
  card: QACard; usage?: UsageRow; onClose: () => void; onOpenAgent: (id: string) => void; onOpenTask: (id: string) => void;
}): JSX.Element {
  const r = card.run;
  return (
    <Modal label={`QA round ${card.round}`} onClose={onClose} className="task-detail qa-detail"
      title={<><span className="detail-title">QA round {card.round}</span>
        <span className={`chip status-chip tone-${card.tone}`} data-testid="detail-status">{card.label}</span></>}>
      <div className="detail-meta row small muted">
        <span>Goal <span className="strong">“{card.goal.title}”</span></span>
        <span>· run {r.id}</span>
        {usage && usage.costMicroUSD > 0 && <span>· {formatMoney(usage.costMicroUSD)}</span>}
        {r.agent_id && (
          <span>· <button className="link" onClick={() => onOpenAgent(r.agent_id ?? "")}>QA agent <AgentName id={r.agent_id} /></button></span>
        )}
      </div>
      {r.summary && (
        <section className="modal-section"><h3>Result</h3><div className="quote">{r.summary}</div></section>
      )}
      {r.error && <p className="hint">{r.error}</p>}
      <section className="modal-section">
        <h3>Bugs</h3>
        {r.bugs.length === 0 ? <p className="muted small">None reported.</p> : (
          <ul className="qa-bugs">
            {r.bugs.map((b, i) => {
              const t = card.bugTasks.find((x) => x.id === b.bug_task_id);
              return (
                <li key={i}>
                  <span className="strong">{b.title}</span>
                  {t ? (
                    <> · <button className="link" onClick={() => onOpenTask(t.id)} data-testid="bug-task">sub-task “{t.title}” ({t.status.replace("_", " ")})</button></>
                  ) : <span className="muted"> · not filed</span>}
                  {b.description && <div className="quote">{b.description}</div>}
                </li>
              );
            })}
          </ul>
        )}
      </section>
      <section className="modal-section">
        <h3>Merged</h3>
        <ul className="qa-merged small">
          {r.merged.map((b) => (
            <li key={b.task_id + b.branch}>
              <span className="mono">{b.branch}</span> · sub-task “{b.title}” at <span className="mono">{(b.merge ?? b.head).slice(0, 8)}</span>
              {b.port && <span className="chip" title="Built on the goal's work before its squash merge; its new changes were ported onto the target"> ported</span>}
            </li>
          ))}
        </ul>
        <div className="mono muted small">integration branch {r.branch}{r.head && ` @ ${r.head.slice(0, 8)}`}</div>
      </section>
    </Modal>
  );
}
