-- Queries for sqlc (run `make generate` after changing this file).

-- Projects (migration 0013, ADR 0046).

-- name: CreateProject :one
INSERT INTO projects (id, name, repo_path, slug, created_at, settings)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: GetProjectByRepo :one
SELECT * FROM projects WHERE repo_path = ?;

-- name: ListProjects :many
-- Registered projects, oldest first; removed ones are hidden.
SELECT * FROM projects WHERE removed_at IS NULL ORDER BY created_at, id;

-- name: RestoreProject :one
-- Brings a removed project back with its history.
UPDATE projects SET removed_at = NULL WHERE id = ?
RETURNING *;

-- name: UpdateProject :one
UPDATE projects SET name = ?, settings = ? WHERE id = ? AND removed_at IS NULL
RETURNING *;

-- name: RemoveProject :execrows
UPDATE projects SET removed_at = ? WHERE id = ? AND removed_at IS NULL;

-- name: CreateTask :one
INSERT INTO tasks (id, project_id, title, description, status, created_at, updated_at, parent_task_id, created_by,
    depends_on, files, acceptance, role, spec_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateTaskPlan :one
-- A sub-task's plan fields (ADR 0032), e.g. from an approved spec revision.
UPDATE tasks SET title = ?, description = ?, depends_on = ?, files = ?, acceptance = ?, role = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: SetTaskConflicts :one
UPDATE tasks SET conflicts = ?, conflicts_checked_at = ? WHERE id = ?
RETURNING *;

-- name: ListChildTasks :many
SELECT * FROM tasks WHERE parent_task_id = ? ORDER BY created_at, id;

-- name: CountChildTasks :one
SELECT count(*) FROM tasks WHERE parent_task_id = ?;

-- name: CountOpenChildTasks :one
-- Sub-tasks that count against a goal's limit (ADR 0062): to do, in progress or blocked.
SELECT count(*) FROM tasks WHERE parent_task_id = ? AND status IN ('todo', 'in_progress', 'blocked');

-- name: SupersedeTask :one
-- Closes a sub-task another replaced (ADR 0062); once.
UPDATE tasks SET status = 'done', superseded_by = ?, updated_at = ? WHERE id = ? AND status != 'done'
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
INSERT INTO agents (id, project_id, task_id, model, inherit_user_settings, state, started_at, sandbox, kind, role, name, cli)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetAgentName :exec
-- Names agents recorded before names existed (ADR 0043).
UPDATE agents SET name = ? WHERE id = ? AND name = '';

-- name: UpdateAgentState :exec
UPDATE agents SET state = ?, activity = ? WHERE id = ?;

-- name: MarkAgentExited :exec
UPDATE agents SET state = 'exited', activity = '', exited_at = ?, exit_code = ?, exit_reason = ?
WHERE id = ?;

-- name: SetAgentExitDetail :exec
-- Why the agent exited, in plain words (ADR 0051).
UPDATE agents SET exit_signal = ?, oom_killed = ?, exit_detail = ? WHERE id = ?;

-- name: GetAutoResumes :one
SELECT auto_resumes FROM agents WHERE id = ?;

-- name: AddAutoResume :one
-- Counts an automatic resume (ADR 0051); the caller checks the count it returns.
UPDATE agents SET auto_resumes = auto_resumes + 1 WHERE id = ? RETURNING auto_resumes;

-- name: MarkAgentRemoved :exec
UPDATE agents SET removed_at = ?, resume_state = '' WHERE id = ?;

-- name: ListVisibleAgents :many
SELECT agents.*, worktrees.path AS worktree_path, worktrees.branch AS branch,
       worktrees.status AS worktree_status, worktrees.kind AS worktree_kind
FROM agents JOIN worktrees ON worktrees.agent_id = agents.id
WHERE agents.project_id = ? AND agents.removed_at IS NULL
ORDER BY agents.started_at, agents.id;

-- name: MarkUnfinishedAgentsExited :execrows
-- A crash: the last recorded state is what a restore resumes from (ADR 0035).
UPDATE agents SET resume_state = state, state = 'exited', activity = '', exited_at = ?, exit_reason = 'daemon_restart'
WHERE project_id = ? AND state != 'exited';

-- name: CreateSession :exec
INSERT INTO sessions (id, agent_id, started_at, resumed_from, cli_session_id) VALUES (?, ?, ?, ?, ?);

-- name: SetSessionCLIID :exec
-- A Codex session's own id, from its first hook (migration 0014, ADR 0049); set once.
UPDATE sessions SET cli_session_id = ? WHERE id = ? AND cli_session_id = '';

-- name: EndSessions :exec
UPDATE sessions SET ended_at = ?, exit_reason = ? WHERE agent_id = ? AND ended_at IS NULL;

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
INSERT INTO messages (project_id, from_agent_id, to_agent_id, body, created_at, from_user)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ClaimMessages :many
-- Hands out a recipient's oldest undelivered messages exactly once (ADR 0018), only those
-- queued during its current session: a relaunched agent (ADR 0022) never gets messages
-- meant for its previous session.
UPDATE messages SET delivered_at = sqlc.arg(delivered_at), delivery = sqlc.arg(delivery)
WHERE id IN (
    SELECT m.id FROM messages m
    WHERE m.to_agent_id = sqlc.arg(to_agent_id) AND m.delivered_at IS NULL AND m.created_at >= sqlc.arg(since)
    ORDER BY m.id LIMIT 10
)
RETURNING *;

-- name: CountPendingMessages :one
SELECT count(*) FROM messages WHERE to_agent_id = ? AND delivered_at IS NULL AND created_at >= ?;

-- name: ListUndeliveredMessagesTo :many
-- The stall watchdog's summary (ADR 0052): messages still waiting for these agents.
SELECT * FROM messages WHERE delivered_at IS NULL AND to_agent_id IN (sqlc.slice(agent_ids)) ORDER BY id LIMIT 50;

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
-- Compare-and-swap from pending, so a request is decided once (ADR 0019). The lead's decisions
-- carry its agent id and reason (ADR 0053).
UPDATE access_requests SET status = ?, decided_at = ?, decided_by = ?, decider_agent_id = ?, decision_reason = ?,
    scope = ?
WHERE id = ? AND status = 'pending'
RETURNING *;

-- name: InsertAccessGrant :exec
INSERT INTO access_grants (project_id, agent_id, host, request_id, created_at) VALUES (?, ?, ?, ?, ?);

-- name: ListAccessGrants :many
-- The hosts an agent's launch is granted (ADR 0053): its project's, its own, and those of earlier
-- workers on its task (a sub-task taken back and assigned again continues their work).
SELECT g.* FROM access_grants g
WHERE g.project_id = sqlc.arg(project_id) AND (g.agent_id IS NULL OR g.agent_id = sqlc.arg(agent_id)
    OR g.agent_id IN (SELECT a.id FROM agents a WHERE a.project_id = sqlc.arg(project_id) AND a.task_id IS NOT NULL
        AND a.task_id = (SELECT b.task_id FROM agents b WHERE b.id = sqlc.arg(agent_id))))
ORDER BY g.id;

-- name: ListPendingAccessForAgents :many
SELECT * FROM access_requests WHERE status = 'pending' AND agent_id IN (sqlc.slice(agent_ids)) ORDER BY id;

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
UPDATE agents SET state = 'starting', activity = '', exited_at = NULL, exit_code = NULL, exit_reason = '',
    resume_state = '', exit_signal = '', oom_killed = 0, exit_detail = ''
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

-- name: SetPullRequestFlagged :one
UPDATE pull_requests SET pending_flagged = ?, accepted_flagged = ?, error = ?, updated_at = ? WHERE id = ?
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

-- name: SetReviewReply :one
-- A worker's "no change needed" answer to a review (ADR 0022); reply_url once it is posted.
UPDATE reviews SET reply = ?, reply_url = ?, updated_at = ? WHERE id = ?
RETURNING *;

-- name: StartReview :one
-- Hands a queued review to its reviewer agent, once (ADR 0021).
UPDATE reviews SET status = 'running', agent_id = ?, attempts = attempts + 1, updated_at = ?
WHERE id = ? AND status = 'queued'
RETURNING *;

-- Token usage (migration 0007, ADR 0026).

-- name: UpsertUsage :exec
INSERT INTO usage_messages (session_id, agent_id, task_id, source, message_id, request_id, model, speed,
    inference_geo, input_tokens, output_tokens, cache_read, cache_write_5m, cache_write_1h, at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, message_id) DO UPDATE SET
    request_id = CASE WHEN excluded.request_id != '' THEN excluded.request_id ELSE usage_messages.request_id END,
    model = excluded.model, speed = excluded.speed, inference_geo = excluded.inference_geo,
    input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
    cache_read = excluded.cache_read, cache_write_5m = excluded.cache_write_5m,
    cache_write_1h = excluded.cache_write_1h, updated_at = excluded.updated_at;

-- name: ListSessionUsage :many
SELECT * FROM usage_messages WHERE session_id = ? ORDER BY id;

-- name: ListGoalUsage :many
-- Everything recorded under a goal: its own agents (the lead), its sub-tasks' workers and
-- their reviewers (ADR 0027).
SELECT u.* FROM usage_messages u LEFT JOIN tasks t ON t.id = u.task_id
WHERE u.task_id = sqlc.arg(goal_id) OR t.parent_task_id = sqlc.arg(goal_id)
ORDER BY u.id;

-- name: ListProjectUsage :many
SELECT u.* FROM usage_messages u JOIN agents a ON a.id = u.agent_id
WHERE a.project_id = ? ORDER BY u.id;

-- name: ListProjectSessions :many
SELECT s.id, s.agent_id, s.started_at, s.ended_at, s.turns, s.tool_calls, s.cli_cost_micro_usd,
       a.kind, a.role, a.sandbox, a.task_id, a.model, a.removed_at, a.cli
FROM sessions s JOIN agents a ON a.id = s.agent_id
WHERE a.project_id = ? ORDER BY s.started_at;

-- name: AddSessionCounts :exec
UPDATE sessions SET turns = turns + sqlc.arg(turns), tool_calls = tool_calls + sqlc.arg(tool_calls)
WHERE id = sqlc.arg(id);

-- name: SetSessionCLICost :exec
UPDATE sessions SET cli_cost_micro_usd = ? WHERE id = ?;

-- Retention (ADR 0028).

-- name: DeleteEventsBefore :execrows
-- One project's events (ADR 0028, 0046): every project engine prunes its own.
DELETE FROM events WHERE at < ? AND agent_id IN (SELECT id FROM agents WHERE project_id = ?);

-- Conflict rounds (ADR 0032).

-- name: TakeConflictRound :one
-- Counts one automatic conflict-resolution relaunch, only while below the limit.
UPDATE pull_requests SET conflict_rounds = conflict_rounds + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND conflict_rounds < sqlc.arg(max_rounds)
RETURNING *;

-- Messages to the user (migration 0008, ADR 0030).

-- name: InsertInbox :one
INSERT INTO inbox (project_id, agent_id, task_id, goal_id, role, kind, body, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetInbox :one
SELECT * FROM inbox WHERE id = ?;

-- name: ListInbox :many
SELECT * FROM inbox WHERE project_id = ? ORDER BY id DESC LIMIT ?;

-- name: CountInboxFromSince :one
SELECT count(*) FROM inbox WHERE agent_id = ? AND created_at >= ?;

-- name: CountUnreadInboxFrom :one
SELECT count(*) FROM inbox WHERE agent_id = ? AND read_at IS NULL;

-- name: MarkInboxRead :one
UPDATE inbox SET read_at = COALESCE(read_at, ?) WHERE id = ?
RETURNING *;

-- name: AnswerInbox :one
UPDATE inbox SET reply = ?, answered_at = ?, read_at = COALESCE(read_at, ?) WHERE id = ?
RETURNING *;

-- Specs (migration 0008, ADR 0031).

-- name: NextSpecVersion :one
SELECT CAST(COALESCE(MAX(version), 0) + 1 AS INTEGER) FROM specs WHERE goal_id = ?;

-- name: InsertSpec :one
-- Run in one transaction with NextSpecVersion; UNIQUE (goal_id, version) backs it up.
INSERT INTO specs (goal_id, version, status, author, plan, markdown, created_at)
VALUES (?, ?, 'proposed', ?, ?, ?, ?)
RETURNING *;

-- name: GetSpec :one
SELECT * FROM specs WHERE id = ?;

-- name: ListGoalSpecs :many
SELECT * FROM specs WHERE goal_id = ? ORDER BY version DESC;

-- name: ListProjectSpecs :many
SELECT specs.* FROM specs JOIN tasks ON tasks.id = specs.goal_id
WHERE tasks.project_id = ? ORDER BY specs.goal_id, specs.version DESC;

-- name: LatestApprovedSpec :one
SELECT * FROM specs WHERE goal_id = ? AND status = 'approved' ORDER BY version DESC LIMIT 1;

-- name: SupersedeProposedSpecs :exec
-- A new version replaces the proposals before it.
UPDATE specs SET status = 'superseded' WHERE goal_id = ? AND status = 'proposed' AND id != ?;

-- name: ApproveSpec :one
-- Approves a proposed version once; the approved version before it is superseded.
UPDATE specs SET status = 'approved', branch = ?, commit_sha = ?, path = ?, approved_at = ?
WHERE id = ? AND status = 'proposed'
RETURNING *;

-- name: SupersedeApprovedSpecs :exec
UPDATE specs SET status = 'superseded' WHERE goal_id = ? AND status = 'approved' AND id != ?;

-- QA runs (migration 0008, ADR 0033).

-- name: InsertQARun :one
INSERT INTO qa_runs (goal_id, agent_id, status, branch, merged, created_at, updated_at)
VALUES (?, ?, 'running', ?, ?, ?, ?)
RETURNING *;

-- name: GetQARun :one
SELECT * FROM qa_runs WHERE id = ?;

-- name: QARunByAgent :one
SELECT * FROM qa_runs WHERE agent_id = ? ORDER BY id DESC LIMIT 1;

-- name: LatestQARun :one
SELECT * FROM qa_runs WHERE goal_id = ? ORDER BY id DESC LIMIT 1;

-- name: ListProjectQARuns :many
SELECT qa_runs.* FROM qa_runs JOIN tasks ON tasks.id = qa_runs.goal_id
WHERE tasks.project_id = ? ORDER BY qa_runs.id DESC LIMIT ?;

-- name: FinishQARun :one
-- Ends a running QA run once.
UPDATE qa_runs SET status = ?, head = ?, summary = ?, bugs = ?, error = ?, updated_at = ?
WHERE id = ? AND status = 'running'
RETURNING *;

-- Project memory (migration 0008, ADR 0034).

-- name: InsertMemory :one
INSERT INTO memory (project_id, kind, title, body, status, source, created_at, updated_at, roles, paths)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMemory :one
SELECT * FROM memory WHERE id = ?;

-- name: ListMemory :many
SELECT * FROM memory WHERE project_id = ? ORDER BY id DESC;

-- name: ListAcceptedMemory :many
SELECT * FROM memory WHERE project_id = ? AND status = 'accepted' ORDER BY id DESC;

-- name: CountProposedMemory :one
SELECT count(*) FROM memory WHERE project_id = ? AND status = 'proposed';

-- name: UpdateMemory :one
UPDATE memory SET kind = ?, title = ?, body = ?, status = ?, updated_at = ?, roles = ?, paths = ? WHERE id = ?
RETURNING *;

-- name: DeleteMemory :execrows
DELETE FROM memory WHERE id = ?;

-- name: CountAgentSessions :one
-- An agent's sessions: one per start or relaunch (ADR 0022, 0031).
SELECT count(*) FROM sessions WHERE agent_id = ?;

-- name: CountSpecsSince :one
SELECT count(*) FROM specs WHERE goal_id = ? AND created_at >= ?;

-- Resume and restore (migration 0009, ADR 0035).

-- name: SetResumeState :exec
UPDATE agents SET resume_state = ? WHERE id = ?;

-- name: LatestSession :one
SELECT * FROM sessions WHERE agent_id = ? ORDER BY started_at DESC, rowid DESC LIMIT 1;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ?;

-- name: ListAgentSessions :many
SELECT * FROM sessions WHERE agent_id = ? ORDER BY started_at DESC, rowid DESC;

-- name: AgentUsageMessageIDs :many
-- Message ids an agent's sessions recorded: a forked transcript repeats its parent's (ADR 0035).
SELECT DISTINCT message_id FROM usage_messages WHERE agent_id = ? AND session_id != ?;

-- name: ClearResumeStates :exec
UPDATE agents SET resume_state = '' WHERE project_id = ? AND resume_state != '';

-- name: DeleteSession :exec
-- A resumed session whose CLI could not load the conversation (ADR 0035): it never ran, and a later
-- resume must continue the session before it.
DELETE FROM sessions WHERE id = ?;

-- Paired devices (migration 0011, ADR 0039).

-- name: CreateDevice :exec
INSERT INTO devices (id, name, kind, token_hash, paired_via, paired_by, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDevices :many
SELECT * FROM devices ORDER BY created_at, id;

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = ? WHERE id = ?;

-- name: DeleteDevice :execrows
DELETE FROM devices WHERE id = ?;

-- Phase 17 (migration 0016).

-- name: MarkSessionPrompted :exec
-- The session got a prompt, so its transcript exists even if no turn finished (ADR 0035).
UPDATE sessions SET prompted = 1 WHERE id = ? AND prompted = 0;

-- name: CloseChildTasks :many
-- A goal marked done closes its sub-tasks (ADR 0054).
UPDATE tasks SET status = 'done', updated_at = ? WHERE parent_task_id = ? AND status != 'done'
RETURNING *;

-- name: ListGoalAgents :many
-- Every agent that worked on a goal or one of its sub-tasks, removed ones included (ADR 0054).
SELECT agents.*, worktrees.branch AS branch, worktrees.status AS worktree_status
FROM agents JOIN worktrees ON worktrees.agent_id = agents.id
WHERE agents.project_id = sqlc.arg(project_id) AND (agents.task_id = sqlc.arg(goal_id)
    OR agents.task_id IN (SELECT t.id FROM tasks t WHERE t.parent_task_id = sqlc.arg(goal_id)))
ORDER BY agents.started_at, agents.id;

-- name: ListGoalQARuns :many
SELECT * FROM qa_runs WHERE goal_id = ? ORDER BY id DESC;

-- name: InsertGoalReport :one
INSERT INTO goal_reports (goal_id, agent_id, summary, commit_message, created_at) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: LatestGoalReport :one
SELECT * FROM goal_reports WHERE goal_id = ? ORDER BY id DESC LIMIT 1;

-- name: InsertGoalMerge :one
INSERT INTO goal_merges (goal_id, qa_run_id, target, source_head, target_before, commit_sha, message, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListProjectGoalMerges :many
SELECT m.* FROM goal_merges m JOIN tasks t ON t.id = m.goal_id WHERE t.project_id = ? ORDER BY m.id DESC LIMIT ?;

-- name: LatestGoalMerge :one
SELECT * FROM goal_merges WHERE goal_id = ? ORDER BY id DESC LIMIT 1;

-- name: ListGoalMerges :many
SELECT * FROM goal_merges WHERE goal_id = ? ORDER BY id DESC;

-- name: MarkGoalMergePushed :one
-- Compare-and-swap: a merge is recorded as pushed once.
UPDATE goal_merges SET pushed_at = ?, push_remote = ? WHERE id = ? AND pushed_at IS NULL
RETURNING *;

-- name: ListProjectAccessGrants :many
-- The project's stored grants with the decision that made them (ADR 0053, 0054).
SELECT g.id, g.project_id, g.agent_id, g.host, g.request_id, g.created_at,
       COALESCE(r.decided_by, '') AS decided_by, COALESCE(r.decider_agent_id, '') AS decider_agent_id,
       COALESCE(r.decision_reason, '') AS decision_reason
FROM access_grants g LEFT JOIN access_requests r ON r.id = g.request_id
WHERE g.project_id = ?
ORDER BY g.id;

-- name: DeleteAccessGrant :one
DELETE FROM access_grants WHERE id = ? AND project_id = ?
RETURNING *;

-- name: ListProjectAgentBranches :many
-- Every agent of a project with its branch, removed ones included: whose branch is whose (Phase 18).
SELECT agents.id, agents.name, agents.task_id, agents.kind, agents.removed_at, worktrees.branch AS branch
FROM agents JOIN worktrees ON worktrees.agent_id = agents.id
WHERE agents.project_id = ?
ORDER BY agents.started_at, agents.id;

-- name: InsertDeletedBranch :one
INSERT INTO deleted_branches (project_id, goal_id, name, head, class, deleted_at) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListGoalDeletedBranches :many
SELECT * FROM deleted_branches WHERE goal_id = ? ORDER BY id;

-- name: GetProfile :one
SELECT * FROM profile WHERE id = 1;

-- name: SetProfile :one
INSERT INTO profile (id, name, look, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, look = excluded.look, updated_at = excluded.updated_at
RETURNING *;

-- Phase 19 (migration 0019).

-- name: GetDaemonSettings :one
SELECT * FROM daemon_settings WHERE id = 1;

-- name: SetDaemonMaxAgents :one
INSERT INTO daemon_settings (id, max_agents, updated_at) VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET max_agents = excluded.max_agents, updated_at = excluded.updated_at
RETURNING *;

-- name: RequestSpecChanges :one
UPDATE specs SET change_request = ?, change_requested_at = ? WHERE id = ? AND status IN ('proposed', 'approved')
RETURNING *;

-- name: InsertTaskReport :one
INSERT INTO task_reports (task_id, agent_id, summary, verification, created_at) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ListTaskReports :many
SELECT * FROM task_reports WHERE task_id = ? ORDER BY id;

-- name: ListAgentStateEvents :many
-- An agent's reduced state changes, oldest first, for its working and waiting time.
SELECT session_id, state, at FROM events WHERE agent_id = ? ORDER BY id;

-- name: CountOpenQuestionsFrom :one
-- An agent's questions to the user that wait for an answer.
SELECT count(*) FROM inbox WHERE agent_id = ? AND kind = 'question' AND answered_at IS NULL;
