import { type JSX, useEffect, useMemo, useRef, useState } from "react";
import { type Agent, type DaemonUsage, type ModelUsage, type VendorUsage, type SessionUsage, type Status, type Task, type UsageReport, api as daemonApi } from "./api";
import { useApi } from "./ProjectContext";
import { AgentName } from "./AgentName";
import { type UsageRow, formatMoney, formatTokens, groupSessions, sessionList, share, totalTokens } from "./usage";

type Grouping = "goal" | "task" | "agent" | "session" | "model";

const groupings: { id: Grouping; label: string }[] = [
  { id: "goal", label: "By goal" },
  { id: "task", label: "By task" },
  { id: "agent", label: "By agent" },
  { id: "session", label: "By session" },
  { id: "model", label: "By model" },
];

interface Props {
  usage: Record<string, SessionUsage>;
  agents: Agent[];
  tasks: Task[];
  status?: Status;
  onOpenAgent: (id: string) => void;
}

// Usage is the cost dashboard (ADR 0026): totals, then one table for the
// chosen grouping, each row with a bar of its share of the largest row's
// estimated cost. Costs are integer micro-USD until formatted.
export default function Usage({ usage, agents, tasks, status, onOpenAgent }: Props): JSX.Element {
  const api = useApi();
  const [grouping, setGrouping] = useState<Grouping>("task");
  const [report, setReport] = useState<UsageReport>();
  const [loadError, setLoadError] = useState(false);
  const sessions = useMemo(() => sessionList(usage), [usage]);
  const loaded = useRef(false);

  // The per-model table and the price table come from the report; refresh it
  // a little after live usage changes, not on every update.
  useEffect(() => {
    const t = window.setTimeout(() => {
      // Both outcomes are handled; a failure only hides the per-model table.
      void api.usage().then((r) => { loaded.current = true; setReport(r); setLoadError(false); }, () => setLoadError(true));
    }, loaded.current ? 3000 : 0);
    return () => window.clearTimeout(t);
  }, [usage]);

  const title = (id?: string): string | undefined => tasks.find((t) => t.id === id)?.title;
  const agentById = new Map(agents.map((a) => [a.id, a]));
  const total = groupSessions(sessions, () => "all")[0];
  const running = sessions.filter((s) => !s.ended_at && agentById.get(s.agent_id)?.state !== "exited").length;
  const prices = report?.prices ?? status?.prices;
  const budgets = report?.budgets ?? status?.budgets;

  return (
    <div className="usage">
      <div className="usage-head">
        <h2>Usage</h2>
        <p className="muted small">
          Estimated from token counts with the price table
          {prices ? <> as of <strong>{prices.as_of}</strong>{prices.file ? ` (with ${prices.file})` : ""}</> : null}.
          Not a bill: subscription use is not charged per token. Sandboxed agents are metered on their API socket;
          host-mode agents are read from their session transcripts.
        </p>
        <p className="muted small" data-testid="budgets">
          {budgets && (budgets.session_micro_usd > 0 || budgets.goal_micro_usd > 0)
            ? <>Budgets (sandboxed agents): {budgets.session_micro_usd > 0 ? `${formatMoney(budgets.session_micro_usd)} per session` : "no session limit"}
              {", "}{budgets.goal_micro_usd > 0 ? `${formatMoney(budgets.goal_micro_usd)} per goal` : "no goal limit"}. Requests
              already running when a limit is reached still finish.</>
            : "No budgets set (--budget-session-usd, --budget-goal-usd)."}
        </p>
        {loadError && <p className="error">Could not load the usage report; showing live sessions only.</p>}
      </div>

      <div className="tiles">
        <Tile label="Estimated cost" value={formatMoney(total?.costMicroUSD ?? 0)} testid="tile-cost"
          note={total && total.unpricedTokens > 0 ? `+ ${formatTokens(total.unpricedTokens)} tokens without a price` : undefined} />
        <Tile label="Tokens" value={formatTokens(total ? totalTokens(total.tokens) : 0)} testid="tile-tokens"
          note={total ? `in ${formatTokens(total.tokens.input)} · out ${formatTokens(total.tokens.output)} · cache read ${formatTokens(total.tokens.cache_read)} · cache write ${formatTokens(total.tokens.cache_write_5m + total.tokens.cache_write_1h)}` : undefined} />
        <Tile label="Sessions" value={String(sessions.length)} note={`${running} running`} testid="tile-sessions" />
        <Tile label="API messages" value={String(total?.messages ?? 0)} note={total ? `${total.turns} turns · ${total.toolCalls} tool calls` : undefined} testid="tile-messages" />
      </div>

      <AllProjects usage={usage} />

      <div className="segmented" role="tablist" aria-label="Group usage">
        {groupings.map((g) => (
          <button key={g.id} role="tab" aria-selected={grouping === g.id} className={grouping === g.id ? "active" : ""}
            onClick={() => setGrouping(g.id)}>{g.label}</button>
        ))}
      </div>

      {sessions.length === 0 ? (
        <p className="empty">No usage yet. Start an agent; its tokens appear here as it works.</p>
      ) : grouping === "model" ? (
        <>
          {(report?.vendors?.length ?? 0) > 1 && <VendorTable vendors={report?.vendors ?? []} />}
          <ModelTable models={report?.models ?? []} />
        </>
      ) : grouping === "session" ? (
        <SessionTable sessions={sessions} title={title} onOpenAgent={onOpenAgent} agentKnown={(id) => agentById.has(id)} />
      ) : (
        <GroupTable rows={groupSessions(sessions, keyOf[grouping])}
          label={(k) => labelOf(grouping, k, title, agentById)}
          onOpen={grouping === "agent" ? (k) => agentById.has(k) && onOpenAgent(k) : undefined} />
      )}
    </div>
  );
}

// AllProjects is the usage of every project and the daemon's total (ADR
// 0046), refreshed a little after this project's live usage changes. With
// one project it says nothing new, so it is not shown.
function AllProjects({ usage }: { usage: Props["usage"] }): JSX.Element | null {
  const [all, setAll] = useState<DaemonUsage>();
  const loaded = useRef(false);
  useEffect(() => {
    const t = window.setTimeout(() => {
      // Both outcomes are handled; a failure only hides the table.
      void daemonApi.usageAll().then((u) => { loaded.current = true; setAll(u); }, () => setAll(undefined));
    }, loaded.current ? 3000 : 0);
    return () => window.clearTimeout(t);
  }, [usage]);
  if (!all || all.projects.length < 2) return null;
  return (
    <section className="all-projects" aria-label="All projects">
      <h3>All projects</h3>
      <table className="usage-table">
        <thead><tr><th>Project</th><th>Sessions</th><th>Tokens</th><th>Estimated cost</th></tr></thead>
        <tbody>
          {all.projects.map((p) => (
            <tr key={p.project_id} data-project={p.project_id}>
              <td>{p.name}{p.state !== "open" ? " (not open)" : ""}</td>
              <td>{p.totals.sessions}</td>
              <td>{formatTokens(totalTokens(p.totals.tokens))}</td>
              <td>{formatMoney(p.totals.cost_micro_usd)}</td>
            </tr>
          ))}
          <tr className="total" data-testid="all-projects-total">
            <td>All projects</td>
            <td>{all.totals.sessions}</td>
            <td>{formatTokens(totalTokens(all.totals.tokens))}</td>
            <td>{formatMoney(all.totals.cost_micro_usd)}</td>
          </tr>
        </tbody>
      </table>
    </section>
  );
}

const keyOf: Record<Exclude<Grouping, "session" | "model">, (s: SessionUsage) => string> = {
  goal: (s) => s.goal_id ?? "",
  task: (s) => s.task_id ?? "",
  agent: (s) => s.agent_id,
};

function labelOf(g: Grouping, key: string, title: (id?: string) => string | undefined, agents: Map<string, Agent>): string {
  if (g === "agent") {
    const a = agents.get(key);
    // The name first (ADR 0043); a removed agent is only known by its id.
    return a ? `${a.name || key.slice(0, 6)} · ${a.role} ${key.slice(0, 6)}` : `${key.slice(0, 6)} (removed)`;
  }
  if (key === "") return g === "goal" ? "No goal" : "No task";
  return title(key) ?? `${key.slice(0, 6)} (deleted)`;
}

function Tile({ label, value, note, testid }: { label: string; value: string; note?: string; testid: string }): JSX.Element {
  return (
    <div className="tile" data-testid={testid}>
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {note && <div className="tile-note">{note}</div>}
    </div>
  );
}

// CostCell is the cost with a bar of its share of the largest row. One
// series, so no legend; the exact value is the text beside it.
function CostCell({ micro, max, unpriced }: { micro: number; max: number; unpriced: number }): JSX.Element {
  const pct = Math.round(share(micro, max) * 100);
  return (
    <td className="cost">
      <div className="bar-track" aria-hidden="true" title={`${formatMoney(micro)} (${pct}% of the largest row)`}>
        <div className="bar" style={{ width: `${pct}%` }} />
      </div>
      <span className="cost-value">{formatMoney(micro)}</span>
      {unpriced > 0 && <span className="hint small" title="tokens of models without a price"> + {formatTokens(unpriced)} unpriced</span>}
    </td>
  );
}

function TokenCells({ r }: { r: Pick<UsageRow, "tokens"> }): JSX.Element {
  const t = r.tokens;
  return (
    <>
      <td className="num">{formatTokens(t.input)}</td>
      <td className="num">{formatTokens(t.output)}</td>
      <td className="num">{formatTokens(t.cache_read)}</td>
      <td className="num" title={`5-minute ${t.cache_write_5m}, 1-hour ${t.cache_write_1h}`}>{formatTokens(t.cache_write_5m + t.cache_write_1h)}</td>
    </>
  );
}

const tokenHeads = (
  <>
    <th className="num">Input</th>
    <th className="num">Output</th>
    <th className="num">Cache read</th>
    <th className="num">Cache write</th>
  </>
);

function GroupTable({ rows, label, onOpen }: {
  rows: UsageRow[]; label: (key: string) => string; onOpen?: (key: string) => void;
}): JSX.Element {
  const max = Math.max(0, ...rows.map((r) => r.costMicroUSD));
  return (
    <table className="usage-table">
      <thead>
        <tr><th>Name</th><th className="num">Sessions</th><th className="num">Turns</th><th className="num">Tool calls</th>
          {tokenHeads}<th className="cost">Estimated cost</th></tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            <td className="name">{onOpen ? <button className="link" onClick={() => onOpen(r.key)}>{label(r.key)}</button> : label(r.key)}</td>
            <td className="num">{r.sessions}</td>
            <td className="num">{r.turns}</td>
            <td className="num">{r.toolCalls}</td>
            <TokenCells r={r} />
            <CostCell micro={r.costMicroUSD} max={max} unpriced={r.unpricedTokens} />
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function SessionTable({ sessions, title, onOpenAgent, agentKnown }: {
  sessions: SessionUsage[]; title: (id?: string) => string | undefined; onOpenAgent: (id: string) => void;
  agentKnown: (id: string) => boolean;
}): JSX.Element {
  const max = Math.max(0, ...sessions.map((s) => s.cost_micro_usd));
  const newest = sessions.slice().reverse();
  return (
    <table className="usage-table">
      <thead>
        <tr><th>Session</th><th>Agent</th><th>Task</th><th>Source</th><th className="num">Turns</th><th className="num">Tool calls</th>
          {tokenHeads}<th className="cost">Estimated cost</th><th className="num" title="Claude Code's own estimate, read from the transcript">Claude Code</th></tr>
      </thead>
      <tbody>
        {newest.map((s) => (
          <tr key={s.session_id}>
            <td className="mono" title={`${s.session_id}, started ${new Date(s.started_at).toLocaleString()}`}>
              {new Date(s.started_at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}
            </td>
            <td>{agentKnown(s.agent_id)
              ? <button className="link" onClick={() => onOpenAgent(s.agent_id)}>{s.role} <AgentName id={s.agent_id} /></button>
              : `${s.role} ${s.agent_id.slice(0, 6)}`}</td>
            <td className="name">{s.task_id ? title(s.task_id) ?? s.task_id.slice(0, 6) : <span className="muted">none</span>}</td>
            <td className="muted small">{s.source === "api" ? "API socket" : "transcript"}{s.ended_at ? "" : " · running"}</td>
            <td className="num">{s.turns}</td>
            <td className="num">{s.tool_calls}</td>
            <TokenCells r={s} />
            <CostCell micro={s.cost_micro_usd} max={max} unpriced={s.unpriced_tokens} />
            <td className="num muted">{s.cli_cost_micro_usd !== undefined ? formatMoney(s.cli_cost_micro_usd) : "–"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const vendorName: Record<string, string> = { anthropic: "Anthropic (Claude Code)", openai: "OpenAI (Codex)" };

// VendorTable is usage per vendor (ADR 0050), when a project uses more than one.
function VendorTable({ vendors }: { vendors: VendorUsage[] }): JSX.Element {
  const max = Math.max(0, ...vendors.map((v) => v.cost_micro_usd));
  return (
    <table className="usage-table" aria-label="Usage by vendor">
      <thead>
        <tr><th>Vendor</th><th className="num">Sessions</th><th className="num">Messages</th>{tokenHeads}<th className="cost">Estimated cost</th></tr>
      </thead>
      <tbody>
        {vendors.map((v) => (
          <tr key={v.vendor} data-vendor={v.vendor}>
            <td className="name">{vendorName[v.vendor] ?? v.vendor}</td>
            <td className="num">{v.sessions}</td>
            <td className="num">{v.messages}</td>
            <TokenCells r={v} />
            <CostCell micro={v.cost_micro_usd} max={max} unpriced={v.unpriced_tokens} />
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ModelTable({ models }: { models: ModelUsage[] }): JSX.Element {
  const max = Math.max(0, ...models.map((m) => m.cost_micro_usd));
  const sorted = models.slice().sort((a, b) => b.cost_micro_usd - a.cost_micro_usd || a.model.localeCompare(b.model));
  return (
    <table className="usage-table">
      <thead>
        <tr><th>Model</th><th className="num">Messages</th>{tokenHeads}<th className="cost">Estimated cost</th></tr>
      </thead>
      <tbody>
        {sorted.map((m) => (
          <tr key={m.model}>
            <td className="name mono">{m.model}{!m.priced && <span className="hint small"> no price</span>}</td>
            <td className="num">{m.messages}</td>
            <TokenCells r={m} />
            {m.priced
              ? <CostCell micro={m.cost_micro_usd} max={max} unpriced={0} />
              : <td className="cost"><span className="hint">no price</span></td>}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
