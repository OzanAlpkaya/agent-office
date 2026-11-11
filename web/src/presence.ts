// The user's presence (Phase 18): which of their devices have the UI open
// and visible. The daemon counts the pages on its hub WebSocket; each page
// reports whether it is visible. The office seats the user at their desk
// while the list is not empty. Pure.

export interface Presence {
  devices: string[]; // names of paired devices with a visible page, distinct and sorted
}

export function isPresence(v: unknown): v is Presence {
  return typeof v === "object" && v !== null && Array.isArray((v as { devices?: unknown }).devices) &&
    (v as { devices: unknown[] }).devices.every((d) => typeof d === "string");
}

// present says whether the user's character sits at their desk.
export const present = (p: Presence | undefined): boolean => (p?.devices.length ?? 0) > 0;

// presenceText is the tooltip of the user's nameplate.
export function presenceText(p: Presence | undefined): string {
  const ds = p?.devices ?? [];
  if (ds.length === 0) return "Away: no device has agent-office open";
  return `You, on ${ds.length === 1 ? ds[0] : `${ds.slice(0, -1).join(", ")} and ${ds[ds.length - 1]}`}`;
}

// visibilityMessage is what a page sends on its hub WebSocket.
export function visibilityMessage(visible: boolean): string {
  return JSON.stringify({ type: "presence", visible });
}
