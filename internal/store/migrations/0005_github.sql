-- +goose Up
-- Phase 5 (ADR 0020-0022): the reviewer role, pull requests and reviews.

-- agents.role gains 'reviewer'. SQLite cannot change a CHECK constraint in place, and
-- rebuilding agents would cascade-delete its children (sessions, worktrees, events, ...)
-- through their foreign keys. So: add a column with the new CHECK, copy, drop the old
-- column, rename the new one. Every row keeps its role; role stays the last column.
ALTER TABLE agents ADD COLUMN role_v5 TEXT NOT NULL DEFAULT 'worker'
    CHECK (role_v5 IN ('lead', 'worker', 'reviewer'));
UPDATE agents SET role_v5 = role;
ALTER TABLE agents DROP COLUMN role;
ALTER TABLE agents RENAME COLUMN role_v5 TO role;

-- One row per branch pushed for review (ADR 0020). agent_id is the worker whose branch it
-- is; follow-ups relaunch that agent (ADR 0022). accepted_flagged lists commit ids the user
-- accepted despite a failed identity check (ADR 0014), as JSON.
CREATE TABLE pull_requests (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    task_id          TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    agent_id         TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    branch           TEXT NOT NULL,
    base             TEXT NOT NULL DEFAULT '',
    number           INTEGER NOT NULL DEFAULT 0,
    url              TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'open', 'merged', 'closed', 'failed')),
    head             TEXT NOT NULL DEFAULT '',
    checks           TEXT NOT NULL DEFAULT ''
                     CHECK (checks IN ('', 'none', 'pending', 'success', 'failure', 'unavailable')),
    failed_checks    TEXT NOT NULL DEFAULT '[]',
    accepted_flagged TEXT NOT NULL DEFAULT '[]',
    rounds           INTEGER NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    UNIQUE (project_id, branch)
);
CREATE INDEX pull_requests_task ON pull_requests (task_id);
CREATE INDEX pull_requests_state ON pull_requests (project_id, state);

-- One row per review of one head (ADR 0021). comments is JSON (path, line, body). Bodies
-- come from a reviewer agent, CodeRabbit or a person on GitHub; they stay in this local
-- database and on GitHub, never in logs.
CREATE TABLE reviews (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id            INTEGER NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    reviewer         TEXT NOT NULL CHECK (reviewer IN ('internal', 'coderabbit', 'human')),
    head             TEXT NOT NULL,
    status           TEXT NOT NULL
                     CHECK (status IN ('queued', 'running', 'done', 'failed', 'timed_out')),
    verdict          TEXT NOT NULL DEFAULT ''
                     CHECK (verdict IN ('', 'approve', 'changes_requested', 'comment')),
    summary          TEXT NOT NULL DEFAULT '' CHECK (length(summary) <= 8000),
    comments         TEXT NOT NULL DEFAULT '[]',
    agent_id         TEXT REFERENCES agents (id) ON DELETE SET NULL,
    github_review_id INTEGER NOT NULL DEFAULT 0,
    url              TEXT NOT NULL DEFAULT '',
    followup         TEXT NOT NULL DEFAULT ''
                     CHECK (followup IN ('', 'started', 'messaged', 'skipped', 'refused')),
    error            TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    attempts         INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX reviews_pr ON reviews (pr_id, id);
CREATE INDEX reviews_agent ON reviews (agent_id);
CREATE UNIQUE INDEX reviews_github_id ON reviews (pr_id, github_review_id) WHERE github_review_id != 0;
