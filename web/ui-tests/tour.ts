// The world of the screenshot tour (Phase 18, docs/screenshots/phase-18):
// a team at work on one goal, a second goal reported with QA passed, two
// done goals in the archive, inbox items and memory, so every main screen
// has realistic content. Shared by screens.spec.ts and its before/after runs.
import type { GoalMerge, GoalRecord, MergePreview, QARun } from "../src/api";
import { type World, agent, task, team, teamUsage } from "./daemon";

const t = (m: number): string => new Date(Date.parse("2026-11-12T09:00:00Z") + m * 60_000).toISOString();
export const qaHead = "1111111111111111111111111111111111111111";
export const mainHead = "2222222222222222222222222222222222222222";

export function tourWorld(): World {
  const w = teamUsage(team(10));
  const reported = task("g00000000002", "Export links as CSV", "review", {
    description: "Users can download their saved links as a CSV file from the list page and the CLI.", created_at: t(-60),
  });
  const done1 = task("g00000000003", "Sprint 3: full-text search", "done", { updated_at: t(-30), created_at: t(-300) });
  const done2 = task("g00000000004", "Sprint 2: login with passkeys", "done", { updated_at: t(-900), created_at: t(-1200) });
  w.tasks[0] = { ...w.tasks[0], description: "Links can carry tags; the list API and the CLI filter by them." };
  w.tasks = [
    ...w.tasks.map((x) => (x.id === "t00000000001"
      ? { ...x, description: "A tags table and a links_tags join table, with a migration.",
        acceptance: ["migration applies on an empty and a seeded database", "tags are unique per user"], files: ["internal/store/"] }
      : x)),
    reported,
    task("t00000000021", "CSV writer", "review", { parent_task_id: reported.id, created_by: "a1ead0000009" }),
    task("t00000000022", "Download button", "review", { parent_task_id: reported.id, created_by: "a1ead0000009" }),
    done1, done2,
    task("t00000000031", "Search index", "done", { parent_task_id: done1.id }),
    task("t00000000032", "Search box", "done", { parent_task_id: done1.id }),
    task("t00000000041", "Passkey registration", "done", { parent_task_id: done2.id }),
  ];
  w.agents.push(
    agent("a1ead0000009", "lead", "idle", { name: "Sana", task_id: reported.id, started_at: t(-60) }),
    agent("f1f2f3000021", "worker", "exited", { name: "Noor", task_id: "t00000000021", exit_reason: "reported_done", exit_code: 0 }),
  );
  const qa: QARun = {
    id: 3, goal_id: reported.id, agent_id: "f9e8d7000031", status: "passed", branch: "agent-office/qa-export-links-f9e8d7", head: qaHead,
    merged: [
      { task_id: "t00000000021", title: "CSV writer", branch: "agent-office/csv-writer-f1f2f3", head: "3333333333333333333333333333333333333333" },
      { task_id: "t00000000022", title: "Download button", branch: "agent-office/download-button-a4b5c6", head: "4444444444444444444444444444444444444444" },
    ],
    summary: "go test ./... passes; exported a 2,000-link account and opened it in a spreadsheet.", bugs: [], created_at: t(-20), updated_at: t(-10),
  };
  const oldQA: QARun = { ...qa, id: 2, goal_id: done1.id, branch: "agent-office/qa-search-c3d4e5", summary: "search works", merged: [] };
  w.qaRuns = [qa, oldQA];
  const merge: GoalMerge = {
    id: 1, goal_id: done1.id, qa_run_id: 2, target: "main", source_head: qaHead, target_before: mainHead,
    commit: "5555555555555555555555555555555555555555", message: "feat: full-text search", created_at: t(-31), pushed_at: t(-30),
    push_remote: "origin",
  };
  w.merges = [merge];
  const preview: MergePreview = {
    goal_id: reported.id, qa_run_id: 3, branch: qa.branch, source_head: qaHead, target: "main", target_head: mainHead,
    message: "feat: export links as CSV\n\nAdds a CSV writer and a download button.", files: 5, untested: 0, checked_out: true, ready: true,
    paths: ["internal/export/csv.go", "internal/export/csv_test.go", "web/src/List.tsx", "cmd/linkshelf/export.go", "README.md"],
    sensitive: [], remote: "origin", remote_url: "github-personal:OzanAlpkaya/agent-office-playground.git",
  };
  w.previews = { [reported.id]: preview };
  const record: GoalRecord = {
    goal: done1,
    sub_tasks: [task("t00000000031", "Search index", "done", { parent_task_id: done1.id }),
      task("t00000000032", "Search box", "done", { parent_task_id: done1.id })],
    agents: [
      { id: "a1ead0000003", name: "Kofi", role: "tech-lead", kind: "lead", cli: "claude", model: "haiku", task_id: done1.id,
        branch: "agent-office/a1ead0000003", state: "exited", exit_reason: "goal_done", started_at: t(-300), removed: true },
      { id: "b2c3d4000003", name: "Ada", role: "worker", kind: "worker", cli: "codex", task_id: "t00000000031",
        branch: "agent-office/search-index-b2c3d4", state: "exited", exit_reason: "reported_done", started_at: t(-290), removed: false },
      { id: "c2c3d4000004", name: "Jonas", role: "worker", kind: "worker", cli: "claude", model: "haiku", task_id: "t00000000032",
        branch: "agent-office/search-box-c2c3d4", state: "exited", exit_reason: "reported_done", started_at: t(-280), removed: false },
    ],
    qa_runs: [oldQA],
    report: { summary: "Search shipped: an FTS5 index and a search box on the list page.",
      commit_message: "feat: full-text search\n\nAn FTS5 index over titles and notes, and a search box.", created_at: t(-35) },
    merges: [merge],
  };
  w.records = {
    [done1.id]: record,
    [reported.id]: {
      goal: reported, sub_tasks: [], qa_runs: [qa], merges: [],
      agents: [{ id: "a1ead0000009", name: "Sana", role: "tech-lead", kind: "lead", cli: "claude", model: "haiku", task_id: reported.id,
        branch: "agent-office/a1ead0000009", state: "idle", started_at: t(-60), removed: false }],
      report: { summary: "CSV export works from the list page and `linkshelf export --csv`; QA passed on both branches.",
        commit_message: "feat: export links as CSV\n\nAdds a CSV writer and a download button.", created_at: t(-9) },
    },
  };
  w.inbox = [
    { id: 1, agent_id: "a1ead0000001", task_id: "g00000000001", goal_id: "g00000000001", role: "tech-lead", kind: "question",
      body: "Should tag names be case-insensitive (\"Go\" and \"go\" the same tag)?", created_at: t(-5) },
    { id: 2, agent_id: "a1ead0000009", task_id: reported.id, goal_id: reported.id, role: "tech-lead", kind: "info",
      body: "Export links as CSV is reported: QA passed; ready for your merge.", created_at: t(-9) },
  ];
  w.memory = [
    { id: 1, kind: "convention", title: "Migrations are forward-only", body: "Never edit an applied migration; add a new one.",
      status: "accepted", created_at: t(-2000), updated_at: t(-2000) },
    { id: 2, kind: "lesson", title: "Run go test with -race", body: "The store has goroutines; CI runs -race.",
      status: "proposed", source: "a1ead0000001", created_at: t(-50), updated_at: t(-50) },
  ];
  return w;
}
