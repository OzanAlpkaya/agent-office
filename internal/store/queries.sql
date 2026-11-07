-- Queries for sqlc (run `make generate` after changing this file).

-- name: UpsertProject :one
INSERT INTO projects (id, name, repo_path, slug, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_path) DO UPDATE SET name = excluded.name
RETURNING *;

-- name: CreateTask :one
INSERT INTO tasks (id, project_id, title, description, status, created_at, updated_at, parent_task_id, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListChildTasks :many
SELECT * FROM tasks WHERE parent_task_id = ? ORDER BY created_at, id;

-- name: CountChildTasks :one
SELECT count(*) FROM tasks WHERE parent_task_id = ?;

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
INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at, sandbox, role)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateAgentState :exec
UPDATE agents SET state = ?, activity = ? WHERE id = ?;

-- name: MarkAgentExited :exec
UPDATE agents SET state = 'exited', activity = '', exited_at = ?, exit_code = ?, exit_reason = ?
WHERE id = ?;

-- name: MarkAgentRemoved :exec
UPDATE agents SET removed_at = ? WHERE id = ?;

-- name: ListVisibleAgents :many
SELECT agents.*, worktrees.path AS worktree_path, worktrees.branch AS branch,
       worktrees.status AS worktree_status, worktrees.kind AS worktree_kind
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
INSERT INTO worktrees (agent_id, project_id, path, branch, base_commit, status, created_at, kind)
VALUES (?, ?, ?, ?, ?, 'active', ?, ?);

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

-- name: ListSandboxEvents :many
SELECT * FROM events WHERE agent_id = ? AND hook = 'sandbox' ORDER BY id DESC LIMIT ?;

-- name: InsertImport :one
INSERT INTO imports (agent_id, trigger, outcome, branch, head, previous, commits, flagged, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: LatestImport :one
SELECT * FROM imports WHERE agent_id = ? ORDER BY id DESC LIMIT 1;

-- name: ListImports :many
SELECT * FROM imports WHERE agent_id = ? ORDER BY id DESC LIMIT ?;

-- name: InsertMessage :one
INSERT INTO messages (project_id, from_agent_id, to_agent_id, body, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ClaimMessages :many
-- Hands out a recipient's oldest undelivered messages exactly once (ADR 0018).
UPDATE messages SET delivered_at = ?, delivery = ?
WHERE id IN (
    SELECT m.id FROM messages m
    WHERE m.to_agent_id = ? AND m.delivered_at IS NULL
    ORDER BY m.id LIMIT 10
)
RETURNING *;

-- name: CountPendingMessages :one
SELECT count(*) FROM messages WHERE to_agent_id = ? AND delivered_at IS NULL;

-- name: CountMessagesSentSince :one
SELECT count(*) FROM messages WHERE from_agent_id = ? AND created_at >= ?;

-- name: ListAgentMessages :many
SELECT * FROM messages WHERE to_agent_id = sqlc.arg(agent_id) OR from_agent_id = sqlc.arg(agent_id)
ORDER BY id DESC LIMIT sqlc.arg(lim);

-- name: InsertAccessRequest :one
INSERT INTO access_requests (agent_id, kind, value, reason, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAccessRequest :one
SELECT * FROM access_requests WHERE id = ?;

-- name: FindPendingAccessRequest :one
SELECT * FROM access_requests WHERE agent_id = ? AND kind = ? AND value = ? AND status = 'pending'
ORDER BY id LIMIT 1;

-- name: CountPendingAccessRequests :one
SELECT count(*) FROM access_requests WHERE agent_id = ? AND status = 'pending';

-- name: DecideAccessRequest :one
-- Compare-and-swap from pending, so a request is decided once (ADR 0019).
UPDATE access_requests SET status = ?, decided_at = ?, decided_by = ?
WHERE id = ? AND status = 'pending'
RETURNING *;

-- name: ExpireAccessRequests :many
UPDATE access_requests SET status = 'expired', decided_at = ?, decided_by = 'agent-office'
WHERE agent_id = ? AND status = 'pending'
RETURNING *;

-- name: ExpireAllAccessRequests :execrows
UPDATE access_requests SET status = 'expired', decided_at = ?, decided_by = 'agent-office'
WHERE status = 'pending'
  AND agent_id IN (SELECT id FROM agents WHERE project_id = ?);

-- name: ListAccessRequests :many
SELECT access_requests.* FROM access_requests JOIN agents ON agents.id = access_requests.agent_id
WHERE agents.project_id = ?
ORDER BY (access_requests.status = 'pending') DESC, access_requests.id DESC
LIMIT ?;

-- name: UnclaimMessage :exec
-- Returns a message to the queue when typing it into the terminal failed (ADR 0018).
UPDATE messages SET delivered_at = NULL, delivery = '' WHERE id = ?;

-- name: CountPendingMessagesFrom :one
SELECT count(*) FROM messages WHERE to_agent_id = ? AND from_agent_id = ? AND delivered_at IS NULL;

-- name: ReopenAgent :exec
-- A follow-up relaunches an exited agent in its workspace (ADR 0022).
UPDATE agents SET state = 'starting', activity = '', exited_at = NULL, exit_code = NULL, exit_reason = ''
WHERE id = ? AND state = 'exited';

-- name: UpsertPullRequest :one
-- Get-or-create the row for a branch; an existing row keeps its state and number.
INSERT INTO pull_requests (project_id, task_id, agent_id, branch, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id, branch) DO UPDATE SET updated_at = excluded.updated_at
RETURNING *;

-- name: GetPullRequest :one
SELECT * FROM pull_requests WHERE id = ?;

-- name: ListPullRequests :many
SELECT * FROM pull_requests WHERE project_id = ? ORDER BY id;

-- name: UpdatePullRequest :one
UPDATE pull_requests
SET base = ?, number = ?, url = ?, state = ?, head = ?, checks = ?, failed_checks = ?, error = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: SetPullRequestAccepted :one
UPDATE pull_requests SET accepted_flagged = ?, updated_at = ? WHERE id = ?
RETURNING *;

-- name: TakePullRequestRound :one
-- Counts one automatic follow-up, only while below the limit (ADR 0022).
UPDATE pull_requests SET rounds = rounds + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND rounds < sqlc.arg(max_rounds)
RETURNING *;

-- name: InsertReview :one
INSERT INTO reviews (pr_id, reviewer, head, status, verdict, summary, comments, github_review_id, url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetReview :one
SELECT * FROM reviews WHERE id = ?;

-- name: ListReviews :many
SELECT * FROM reviews WHERE pr_id = ? ORDER BY id DESC LIMIT ?;

-- name: ListReviewsByStatus :many
SELECT reviews.* FROM reviews JOIN pull_requests ON pull_requests.id = reviews.pr_id
WHERE pull_requests.project_id = ? AND reviews.status = ?
ORDER BY reviews.id;

-- name: ReviewForHead :one
SELECT * FROM reviews WHERE pr_id = ? AND reviewer = ? AND head = ? ORDER BY id DESC LIMIT 1;

-- name: ReviewByGitHubID :one
SELECT * FROM reviews WHERE pr_id = ? AND github_review_id = ?;

-- name: ReviewByAgent :one
SELECT * FROM reviews WHERE agent_id = ? ORDER BY id DESC LIMIT 1;

-- name: UpdateReview :one
UPDATE reviews
SET status = ?, verdict = ?, summary = ?, comments = ?, agent_id = ?, github_review_id = ?, url = ?, error = ?,
    attempts = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: ClaimReviewFollowup :one
-- Decides a review's follow-up once (ADR 0022).
UPDATE reviews SET followup = ?, updated_at = ? WHERE id = ? AND followup = ''
RETURNING *;

-- name: SetReviewFollowup :one
UPDATE reviews SET followup = ?, updated_at = ? WHERE id = ?
RETURNING *;
