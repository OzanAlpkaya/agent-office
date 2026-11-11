// Branch clean-up (Phase 18): grouping the preview and building the request.
// Pure, so Vitest covers it. The daemon classifies and checks again; this
// only decides what the user is shown and asked.
import type { BranchDelete, BranchInfo, BranchPreview } from "./api";

export interface BranchGroups {
  safe: BranchInfo[]; // merged or squashed, not in use: selected by default
  unmerged: BranchInfo[]; // not in use; need a second confirmation
  inUse: BranchInfo[]; // checked out or their agent runs: never deleted
}

export function branchGroups(branches: readonly BranchInfo[]): BranchGroups {
  const byName = (a: BranchInfo, b: BranchInfo): number => a.name.localeCompare(b.name);
  return {
    safe: branches.filter((b) => !b.in_use && b.class !== "unmerged").sort(byName),
    unmerged: branches.filter((b) => !b.in_use && b.class === "unmerged").sort(byName),
    inUse: branches.filter((b) => Boolean(b.in_use)).sort(byName),
  };
}

// deleteRequest asks to delete the chosen branches at the heads the preview
// showed. Unmerged ones go in the explicit list too: the user confirmed them
// by name. Branches in use are never asked for.
export function deleteRequest(p: BranchPreview, chosen: ReadonlySet<string>): BranchDelete {
  const picked = p.branches.filter((b) => chosen.has(b.name) && !b.in_use);
  return {
    branches: picked.map((b) => ({ name: b.name, head: b.head })),
    unmerged: picked.filter((b) => b.class === "unmerged").map((b) => b.name),
  };
}
