// The team workflow world (Phase 12): a goal with a proposed plan, sub-tasks
// with dependencies and conflicts, QA and project memory. Shared by specs.
import type { Spec } from "../src/api";
import { type World, agent, task } from "./daemon";

const at = "2026-11-08T10:05:00Z";

export function workflow(): World {
  const goal = task("g00000000001", "Add tags to saved links", "in_progress");
  const sub = (id: string, title: string, status: "todo" | "in_progress" | "review", over = {}) =>
    task(id, title, status, { parent_task_id: goal.id, ...over });
  const tasks = [
    goal,
    sub("t00000000001", "Tags table", "review", { spec_key: "schema", files: ["internal/store/"], acceptance: ["tags are stored"] }),
    sub("t00000000003", "Tag API", "in_progress", { spec_key: "api" }),
    sub("t00000000002", "Tag filter", "todo", { spec_key: "filter", depends_on: ["t00000000001", "t00000000003"] }),
    sub("t00000000004", "Tag CLI", "review", {
      conflicts: [{ branch: "origin/main", files: ["cmd/main.go"] },
        { with: "t00000000001", branch: "agent-office/tags-table-b2c3d4", files: ["internal/store/store.go"] }],
    }),
  ];
  const spec: Spec = {
    id: 1, goal_id: goal.id, version: 2, status: "proposed", author: "a7c4e1000001", created_at: at, markdown: "# Add tags",
    plan: {
      summary: "Users can tag links and filter by tag.", design: "A tags table and a ?tag= filter.",
      sub_tasks: [
        { key: "schema", title: "Tags table", description: "Add the table.", files: ["internal/store/"] },
        { key: "filter", title: "Tag filter", description: "Filter by tag.", depends_on: ["schema"], acceptance: ["?tag=x filters"] },
      ],
      risks: "Migrations.",
    },
  };
  return {
    tasks,
    agents: [
      agent("a7c4e1000001", "architect", "idle", { name: "Ines", task_id: goal.id }),
      agent("a1ead0000001", "lead", "idle", { task_id: goal.id }),
      agent("b2c3d4000001", "worker", "working", { task_id: "t00000000003" }),
      agent("c3d4e5000002", "worker", "exited", { task_id: "t00000000004", exit_reason: "reported_done" }),
      agent("d4e5f6000003", "worker", "idle", { role: "docs", task_id: "t00000000001" }),
    ],
    accessRequests: [],
    messages: [],
    specs: [spec],
    inbox: [
      { id: 2, kind: "plan", body: "The architect's plan (version 2) is ready for your approval.", created_at: at, goal_id: goal.id, task_id: goal.id },
      { id: 1, kind: "question", agent_id: "a7c4e1000001", role: "architect", task_id: goal.id, goal_id: goal.id,
        body: "Should tags be case-insensitive?", created_at: at },
    ],
    qaRuns: [{
      id: 1, goal_id: goal.id, status: "failed", branch: "agent-office/qa-add-tags-e5f6a7", merged: [], created_at: at, updated_at: at,
      summary: "The filter ignores case.", bugs: [{ title: "Filter is case-sensitive", description: "?tag=Go misses go", bug_task_id: "t00000000009" }],
    }],
    memory: [{ id: 1, kind: "convention", title: "Errors are wrapped", body: "Wrap errors with %w.", status: "proposed",
      source: "f00000000001", roles: ["worker"], paths: ["internal/store/"], created_at: at, updated_at: at }],
  };
}
