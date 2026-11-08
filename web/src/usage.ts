// Pure helpers for token usage (ADR 0026): formatting integer micro-USD and
// grouping sessions by agent, task and goal. Money stays integer here; only
// the formatted string has a decimal point.
import type { SessionUsage, Tokens } from "./api";

export const zeroTokens: Tokens = { input: 0, output: 0, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 };

export function addTokens(a: Tokens, b: Tokens): Tokens {
  return {
    input: a.input + b.input,
    output: a.output + b.output,
    cache_read: a.cache_read + b.cache_read,
    cache_write_5m: a.cache_write_5m + b.cache_write_5m,
    cache_write_1h: a.cache_write_1h + b.cache_write_1h,
  };
}

export function totalTokens(t: Tokens): number {
  return t.input + t.output + t.cache_read + t.cache_write_5m + t.cache_write_1h;
}

// formatMoney shows micro-USD as dollars: whole dollars from $100, cents from
// $1, and four decimals below (agent turns often cost fractions of a cent).
// Rounds half up, in integers.
export function formatMoney(micro: number): string {
  const m = Math.max(0, Math.round(micro));
  const dollars = Math.floor(m / 1_000_000);
  const rest = m % 1_000_000;
  if (m >= 100_000_000) return `$${Math.floor((m + 500_000) / 1_000_000).toLocaleString("en-US")}`;
  if (m >= 1_000_000) {
    const cents = Math.floor((rest + 5_000) / 10_000);
    return cents === 100 ? `$${dollars + 1}.00` : `$${dollars}.${String(cents).padStart(2, "0")}`;
  }
  const tenThousandths = Math.floor((m + 50) / 100); // units of $0.0001
  if (tenThousandths >= 10_000) return "$1.00";
  return `$0.${String(tenThousandths).padStart(4, "0")}`;
}

// formatTokens shortens a token count: 950, 1,234, 12.3k, 4.56M.
export function formatTokens(n: number): string {
  if (n < 10_000) return n.toLocaleString("en-US");
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(2)}M`;
}

// UsageRow sums the sessions that share a key.
export interface UsageRow {
  key: string;
  sessions: number;
  messages: number;
  turns: number;
  toolCalls: number;
  tokens: Tokens;
  costMicroUSD: number;
  unpricedTokens: number;
  agents: string[];
}

function emptyRow(key: string): UsageRow {
  return { key, sessions: 0, messages: 0, turns: 0, toolCalls: 0, tokens: zeroTokens, costMicroUSD: 0, unpricedTokens: 0, agents: [] };
}

// groupSessions sums sessions by key (undefined: the session is left out),
// most expensive first, then by key.
export function groupSessions(sessions: SessionUsage[], key: (s: SessionUsage) => string | undefined): UsageRow[] {
  const rows = new Map<string, UsageRow>();
  for (const s of sessions) {
    const k = key(s);
    if (k === undefined) continue;
    const r = rows.get(k) ?? emptyRow(k);
    rows.set(k, {
      ...r,
      sessions: r.sessions + 1,
      messages: r.messages + s.messages,
      turns: r.turns + s.turns,
      toolCalls: r.toolCalls + s.tool_calls,
      tokens: addTokens(r.tokens, s.tokens),
      costMicroUSD: r.costMicroUSD + s.cost_micro_usd,
      unpricedTokens: r.unpricedTokens + s.unpriced_tokens,
      agents: r.agents.includes(s.agent_id) ? r.agents : [...r.agents, s.agent_id],
    });
  }
  return [...rows.values()].sort((a, b) => b.costMicroUSD - a.costMicroUSD || a.key.localeCompare(b.key));
}

// usageBy sums sessions per key into a lookup, for cards.
export function usageBy(sessions: SessionUsage[], key: (s: SessionUsage) => string | undefined): Map<string, UsageRow> {
  return new Map(groupSessions(sessions, key).map((r) => [r.key, r]));
}

// sessionList turns the session map into a list, oldest first.
export function sessionList(byId: Record<string, SessionUsage>): SessionUsage[] {
  return Object.values(byId).sort((a, b) => a.started_at.localeCompare(b.started_at) || a.session_id.localeCompare(b.session_id));
}

// share is a row's part of the largest row, 0 to 1, for inline bars.
export function share(value: number, max: number): number {
  return max > 0 ? Math.min(1, Math.max(0, value / max)) : 0;
}
