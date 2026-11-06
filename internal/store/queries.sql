-- Queries for sqlc (run `make generate` after changing this file).

-- name: UpsertProject :one
INSERT INTO projects (id, name, repo_path, slug, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_path) DO UPDATE SET name = excluded.name
RETURNING *;

-- name: CreateTask :one
INSERT INTO tasks (id, project_id, title, description, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: ListTasks :many
SELECT * FROM tasks WHERE project_id = ? ORDER BY created_at, id;

-- name: UpdateTask :one
UPDATE tasks SET title = ?, description = ?, status = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: RemoveTask :execrows
DELETE FROM tasks WHERE id = ?;

-- name: CreateAgent :exec
INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpdateAgentState :exec
UPDATE agents SET state = ?, activity = ? WHERE id = ?;

-- name: MarkAgentExited :exec
UPDATE agents SET state = 'exited', activity = '', exited_at = ?, exit_code = ?, exit_reason = ?
WHERE id = ?;

-- name: MarkAgentRemoved :exec
UPDATE agents SET removed_at = ? WHERE id = ?;

-- name: ListVisibleAgents :many
SELECT agents.*, worktrees.path AS worktree_path, worktrees.branch AS branch,
       worktrees.status AS worktree_status
FROM agents JOIN worktrees ON worktrees.agent_id = agents.id
WHERE agents.project_id = ? AND agents.removed_at IS NULL
ORDER BY agents.started_at, agents.id;

-- name: MarkUnfinishedAgentsExited :execrows
UPDATE agents SET state = 'exited', activity = '', exited_at = ?, exit_reason = 'daemon_restart'
WHERE state != 'exited';

-- name: CreateSession :exec
INSERT INTO sessions (id, agent_id, started_at) VALUES (?, ?, ?);

-- name: EndSessions :exec
UPDATE sessions SET ended_at = ? WHERE agent_id = ? AND ended_at IS NULL;

-- name: CreateWorktree :exec
INSERT INTO worktrees (agent_id, project_id, path, branch, base_commit, status, created_at)
VALUES (?, ?, ?, ?, ?, 'active', ?);

-- name: GetWorktree :one
SELECT * FROM worktrees WHERE agent_id = ?;

-- name: SetWorktreeStatus :exec
UPDATE worktrees SET status = ?, removed_at = ? WHERE agent_id = ?;

-- name: ListWorktreesWithStatus :many
SELECT * FROM worktrees WHERE project_id = ? AND status = ?;

-- name: InsertEvent :exec
INSERT INTO events (agent_id, session_id, kind, hook, tool, detail, state, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListEvents :many
SELECT * FROM events WHERE agent_id = ? ORDER BY id DESC LIMIT ?;
