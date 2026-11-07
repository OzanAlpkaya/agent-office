-- +goose Up
-- Phase 4 (ADR 0017-0019): agent roles, sub-tasks, messages and access requests.
-- Existing agents are workers; existing tasks are top-level and were created by the user.
ALTER TABLE agents ADD COLUMN role TEXT NOT NULL DEFAULT 'worker'
    CHECK (role IN ('lead', 'worker'));
ALTER TABLE tasks ADD COLUMN parent_task_id TEXT REFERENCES tasks (id) ON DELETE SET NULL;
-- '' = the user; otherwise the id of the agent that created the task.
ALTER TABLE tasks ADD COLUMN created_by TEXT NOT NULL DEFAULT '';
CREATE INDEX tasks_parent ON tasks (parent_task_id);

-- Messages between agents (ADR 0018). from_agent_id NULL = agent-office itself.
-- Bodies are needed for delivery; they stay in this local database only.
CREATE TABLE messages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    from_agent_id TEXT REFERENCES agents (id) ON DELETE CASCADE,
    to_agent_id   TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    body          TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at    DATETIME NOT NULL,
    delivered_at  DATETIME,
    delivery      TEXT NOT NULL DEFAULT '' CHECK (delivery IN ('', 'stop_hook', 'pty'))
);
CREATE INDEX messages_to ON messages (to_agent_id, delivered_at, id);
CREATE INDEX messages_from ON messages (from_agent_id, created_at);

-- Access requests (ADR 0019). Phase 4 grants only 'domain'; the other kinds are reserved
-- for the stack packs (Phase 6) so they need no table rebuild.
CREATE TABLE access_requests (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('domain', 'secret', 'path')),
    value      TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 253),
    reason     TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    created_at DATETIME NOT NULL,
    decided_at DATETIME,
    decided_by TEXT NOT NULL DEFAULT '' CHECK (decided_by IN ('', 'user', 'agent-office'))
);
CREATE INDEX access_requests_agent ON access_requests (agent_id, status);

-- imports.trigger gains 'report_done'. SQLite cannot change a CHECK constraint in place, so
-- the table is rebuilt (ADR 0012): copy every row, then replace the old table, in this
-- migration's transaction. Nothing references imports.
CREATE TABLE imports_v4 (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    trigger  TEXT NOT NULL
             CHECK (trigger IN ('exit', 'manual', 'restart', 'remove', 'save_work', 'report_done')),
    outcome  TEXT NOT NULL
             CHECK (outcome IN ('no_commits', 'created', 'fast_forward', 'up_to_date', 'diverged')),
    branch   TEXT NOT NULL DEFAULT '',
    head     TEXT NOT NULL DEFAULT '',
    previous TEXT NOT NULL DEFAULT '',
    commits  INTEGER NOT NULL DEFAULT 0,
    flagged  TEXT NOT NULL DEFAULT '[]',
    at       DATETIME NOT NULL
);
INSERT INTO imports_v4 (id, agent_id, trigger, outcome, branch, head, previous, commits, flagged, at)
SELECT id, agent_id, trigger, outcome, branch, head, previous, commits, flagged, at FROM imports;
DROP TABLE imports;
ALTER TABLE imports_v4 RENAME TO imports;
CREATE INDEX imports_agent ON imports (agent_id, id);
