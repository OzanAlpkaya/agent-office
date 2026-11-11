-- +goose Up
-- Phase 16 (ADR 0051, 0052): why an agent exited, in plain words, and its automatic resume.
-- exit_signal is the signal that ended the agent's process ('' for none), oom_killed whether the
-- out-of-memory killer ended it, exit_detail the sentence the UI and the lead are shown.
-- auto_resumes counts the automatic resumes of the agent (at most one, ADR 0051).
ALTER TABLE agents ADD COLUMN exit_signal TEXT NOT NULL DEFAULT '' CHECK (length(exit_signal) <= 16);
ALTER TABLE agents ADD COLUMN oom_killed BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN exit_detail TEXT NOT NULL DEFAULT '' CHECK (length(exit_detail) <= 1000);
ALTER TABLE agents ADD COLUMN auto_resumes INTEGER NOT NULL DEFAULT 0 CHECK (auto_resumes >= 0);

-- Access decisions by the Tech Lead (ADR 0053): decided_by gains 'lead'; decider_agent_id is the
-- lead's agent id, decision_reason the reason it gave (or the user's, optional), scope whether an
-- approval covers the asking agent or every agent of the project. SQLite cannot change a CHECK
-- constraint in place, so the table is rebuilt (ADR 0012) in this migration's transaction.
-- Nothing references access_requests.
CREATE TABLE access_requests_v15 (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id         TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('domain', 'secret', 'path')),
    value            TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 253),
    reason           TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    created_at       DATETIME NOT NULL,
    decided_at       DATETIME,
    decided_by       TEXT NOT NULL DEFAULT '' CHECK (decided_by IN ('', 'user', 'agent-office', 'lead')),
    decider_agent_id TEXT NOT NULL DEFAULT '' CHECK (length(decider_agent_id) <= 64),
    decision_reason  TEXT NOT NULL DEFAULT '' CHECK (length(decision_reason) <= 1000),
    scope            TEXT NOT NULL DEFAULT 'agent' CHECK (scope IN ('agent', 'project'))
);
INSERT INTO access_requests_v15 (id, agent_id, kind, value, reason, status, created_at, decided_at, decided_by)
SELECT id, agent_id, kind, value, reason, status, created_at, decided_at, decided_by FROM access_requests;
DROP TABLE access_requests;
ALTER TABLE access_requests_v15 RENAME TO access_requests;
CREATE INDEX access_requests_agent ON access_requests (agent_id, status);

-- Approved hosts outlive the session that asked (ADR 0053): every launch of a sandboxed agent loads
-- its project's grants and its own. agent_id is NULL for a project-wide grant.
CREATE TABLE access_grants (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    agent_id   TEXT REFERENCES agents (id) ON DELETE CASCADE,
    host       TEXT NOT NULL CHECK (length(host) BETWEEN 1 AND 253),
    request_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL
);
CREATE INDEX access_grants_project ON access_grants (project_id, agent_id);

-- Messages waiting for an agent that was not running are put into the first prompt of the session
-- that resumes or relaunches it (ADR 0051): delivery gains 'prompt'. Rebuilt like access_requests;
-- nothing references messages.
CREATE TABLE messages_v15 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    from_agent_id TEXT REFERENCES agents (id) ON DELETE CASCADE,
    to_agent_id   TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    body          TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at    DATETIME NOT NULL,
    delivered_at  DATETIME,
    delivery      TEXT NOT NULL DEFAULT '' CHECK (delivery IN ('', 'stop_hook', 'pty', 'prompt')),
    from_user     BOOLEAN NOT NULL DEFAULT 0
);
INSERT INTO messages_v15 (id, project_id, from_agent_id, to_agent_id, body, created_at, delivered_at, delivery, from_user)
SELECT id, project_id, from_agent_id, to_agent_id, body, created_at, delivered_at, delivery, from_user FROM messages;
DROP TABLE messages;
ALTER TABLE messages_v15 RENAME TO messages;
CREATE INDEX messages_to ON messages (to_agent_id, delivered_at, id);
CREATE INDEX messages_from ON messages (from_agent_id, created_at);
