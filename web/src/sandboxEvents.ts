// Sandbox events (ADR 0016, Phase 17): problems to act on, and Claude Code's
// own background requests, which the sandbox refuses as expected.
import type { SandboxEvent } from "./api";

export const backgroundKinds: readonly string[] = ["egress_background", "api_background"];

// isBackground reports whether an event is expected background traffic.
export function isBackground(e: SandboxEvent): boolean {
  return backgroundKinds.includes(e.kind);
}

// splitEvents separates problems from background refusals, keeping order.
export function splitEvents(events: readonly SandboxEvent[]): { problems: SandboxEvent[]; background: SandboxEvent[] } {
  return { problems: events.filter((e) => !isBackground(e)), background: events.filter(isBackground) };
}

// backgroundLine is the quiet summary of background refusals, or "".
export function backgroundLine(n: number): string {
  if (n === 0) return "";
  return n === 1 ? "1 background request of Claude Code refused (expected)" : `${n} background requests of Claude Code refused (expected)`;
}
