// Builders for unit tests. Not imported by the app.
import type { Agent, Message, Task } from "../api";

export function agent(id: string, over: Partial<Agent> = {}): Agent {
  return {
    id, state: "working", kind: "worker", role: "worker", inherit_user_settings: false, sandbox: "podman",
    branch: `agent-office/${id}`, worktree_path: `/tmp/${id}`, workspace_kind: "clone",
    worktree_status: "active", has_terminal: true, started_at: "2026-11-08T10:00:00Z", ...over,
  };
}

export function task(id: string, over: Partial<Task> = {}): Task {
  return {
    id, title: `Task ${id}`, description: "", status: "todo",
    created_at: "2026-11-08T09:00:00Z", updated_at: "2026-11-08T09:00:00Z", ...over,
  };
}

export function message(id: number, from: string | undefined, to: string, created_at: string): Message {
  return { id, from, to, body: `message ${id}`, created_at };
}
