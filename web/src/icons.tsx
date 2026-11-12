// Line icons for the navigation and buttons (Phase 18 design system): 20×20,
// stroked with currentColor, drawn here so nothing is fetched. Decorative:
// the button or link next to one carries the name.
import type { JSX } from "react";

export type IconName =
  | "board" | "office" | "usage" | "settings" | "agents" | "inbox" | "approvals" | "search" | "plus" | "close" | "back"
  | "terminal" | "branch" | "merge" | "check";

const paths: Record<IconName, JSX.Element> = {
  board: <><rect x="3" y="4" width="4" height="12" rx="1" /><rect x="8" y="4" width="4" height="8" rx="1" /><rect x="13" y="4" width="4" height="10" rx="1" /></>,
  office: <><path d="M3 17V7l7-4 7 4v10" /><path d="M8 17v-5h4v5" /><path d="M3 17h14" /></>,
  usage: <><path d="M4 16V9" /><path d="M8 16V5" /><path d="M12 16v-4" /><path d="M16 16V7" /></>,
  settings: <><circle cx="10" cy="10" r="2.5" /><path d="M10 2.8v2M10 15.2v2M17.2 10h-2M4.8 10h-2M15.1 4.9l-1.4 1.4M6.3 13.7l-1.4 1.4M15.1 15.1l-1.4-1.4M6.3 6.3 4.9 4.9" /></>,
  agents: <><circle cx="7.5" cy="7" r="2.5" /><path d="M3 16c.6-2.6 2.3-4 4.5-4s3.9 1.4 4.5 4" /><circle cx="14" cy="8" r="2" /><path d="M13.5 12.1c1.8.2 3 1.4 3.5 3.4" /></>,
  inbox: <><path d="M3 11l2-6h10l2 6v5H3z" /><path d="M3 11h4l1 2h4l1-2h4" /></>,
  approvals: <><path d="M10 3l6 2.5v4.2c0 3.4-2.5 6-6 7.3-3.5-1.3-6-3.9-6-7.3V5.5z" /><path d="M7.5 10l1.8 1.8L13 8" /></>,
  search: <><circle cx="9" cy="9" r="5" /><path d="M13 13l4 4" /></>,
  plus: <><path d="M10 4v12M4 10h12" /></>,
  close: <><path d="M5 5l10 10M15 5L5 15" /></>,
  back: <><path d="M12 4l-6 6 6 6" /></>,
  terminal: <><rect x="3" y="4" width="14" height="12" rx="2" /><path d="M6 8l2.5 2L6 12M10 12h4" /></>,
  branch: <><circle cx="6" cy="5" r="1.8" /><circle cx="6" cy="15" r="1.8" /><circle cx="14" cy="7" r="1.8" /><path d="M6 7v6M14 9c0 3-4 2.5-7.5 4.5" /></>,
  merge: <><circle cx="6" cy="5" r="1.8" /><circle cx="6" cy="15" r="1.8" /><circle cx="14" cy="15" r="1.8" /><path d="M6 7v6M6 7c0 4 4 6 6.5 6.5" /></>,
  check: <><path d="M4.5 10.5l3.5 3.5 7.5-8" /></>,
};

export function Icon({ name, size = 18 }: { name: IconName; size?: number }): JSX.Element {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"
      strokeLinejoin="round" aria-hidden="true" focusable="false">
      {paths[name]}
    </svg>
  );
}
