// Project memory scopes (ADR 0037): pure helpers for the memory panel.
import type { MemoryEntry } from "./api";

// parseScope turns "worker, qa" or one item per line into a clean list:
// trimmed, without empty items or duplicates.
export function parseScope(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[,\n]/)) {
    const s = raw.trim();
    if (s && !out.includes(s)) out.push(s);
  }
  return out;
}

// scopeLabel says whom an entry reaches, e.g. "roles worker, qa · files web/".
export function scopeLabel(m: Pick<MemoryEntry, "roles" | "paths">): string {
  const parts: string[] = [];
  if (m.roles?.length) parts.push(`roles ${m.roles.join(", ")}`);
  if (m.paths?.length) parts.push(`files ${m.paths.join(", ")}`);
  return parts.length ? parts.join(" · ") : "every agent";
}
