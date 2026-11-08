-- +goose Up
-- Phase 12 (ADR 0029-0034): roles as data, messages to the user, specs, dependencies and
-- conflicts, QA runs, project memory.

-- ADR 0029: agents.kind is the behaviour the engine implements (CHECK-constrained);
-- agents.role becomes the role's name, which is data. The old role CHECK cannot change in
-- place and agents cannot be rebuilt (its children would cascade, see 0005), so: copy the old
-- role into kind, add the new role column, copy with leads renamed to tech-lead, drop the old
-- column, rename. Every row keeps its behaviour.
ALTER TABLE agents ADD COLUMN kind TEXT NOT NULL DEFAULT 'worker'
    CHECK (kind IN ('architect', 'lead', 'worker', 'qa', 'reviewer', 'assistant'));
UPDATE agents SET kind = role;
ALTER TABLE agents ADD COLUMN role_v8 TEXT NOT NULL DEFAULT 'worker' CHECK (length(role_v8) BETWEEN 1 AND 32);
UPDATE agents SET role_v8 = CASE role WHEN 'lead' THEN 'tech-lead' ELSE role END;
ALTER TABLE agents DROP COLUMN role;
ALTER TABLE agents RENAME COLUMN role_v8 TO role;

-- ADR 0032: a sub-task's plan. JSON arrays: depends_on (task ids), files (paths, directories
-- ending in '/', globs), acceptance (criteria). role: the worker role that builds it ('' =
-- worker). spec_key: its key in the goal's spec. conflicts: the last merge-tree check.
ALTER TABLE tasks ADD COLUMN depends_on TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN files TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN acceptance TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN role TEXT NOT NULL DEFAULT '' CHECK (length(role) <= 32);
ALTER TABLE tasks ADD COLUMN spec_key TEXT NOT NULL DEFAULT '' CHECK (length(spec_key) <= 32);
ALTER TABLE tasks ADD COLUMN conflicts TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN conflicts_checked_at DATETIME;

-- ADR 0030: the user's replies are delivered like other messages, framed as the user's words.
ALTER TABLE messages ADD COLUMN from_user BOOLEAN NOT NULL DEFAULT 0;

-- ADR 0032: automatic conflict-resolution relaunches per pull request.
ALTER TABLE pull_requests ADD COLUMN conflict_rounds INTEGER NOT NULL DEFAULT 0;

-- ADR 0030: messages to the user. agent_id NULL = agent-office itself (e.g. a plan to
-- approve). Bodies are agent-written; they stay in this local database.
CREATE TABLE inbox (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    agent_id    TEXT REFERENCES agents (id) ON DELETE SET NULL,
    task_id     TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    goal_id     TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    role        TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('question', 'info', 'blocked', 'briefing', 'plan')),
    body        TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at  DATETIME NOT NULL,
    read_at     DATETIME,
    answered_at DATETIME,
    reply       TEXT NOT NULL DEFAULT '' CHECK (length(reply) <= 4000)
);
CREATE INDEX inbox_project ON inbox (project_id, id);
CREATE INDEX inbox_agent ON inbox (agent_id, created_at);

-- ADR 0031: a goal's plan, one row per version. plan is the structured plan (JSON),
-- markdown its rendering; branch and commit_sha once approved and committed.
CREATE TABLE specs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('proposed', 'approved', 'superseded')),
    author      TEXT NOT NULL DEFAULT '', -- '' = the user; else the architect's agent id
    plan        TEXT NOT NULL,
    markdown    TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    commit_sha  TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL,
    approved_at DATETIME,
    UNIQUE (goal_id, version)
);

-- ADR 0033: QA runs. merged: the sub-task branches and heads QA was given (JSON); bugs: what
-- it reported (JSON).
CREATE TABLE qa_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    agent_id   TEXT REFERENCES agents (id) ON DELETE SET NULL,
    status     TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed', 'error')),
    branch     TEXT NOT NULL DEFAULT '',
    head       TEXT NOT NULL DEFAULT '',
    merged     TEXT NOT NULL DEFAULT '[]',
    summary    TEXT NOT NULL DEFAULT '' CHECK (length(summary) <= 4000),
    bugs       TEXT NOT NULL DEFAULT '[]',
    error      TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE INDEX qa_runs_goal ON qa_runs (goal_id, id);

-- ADR 0034: project memory. Only accepted entries reach agents' prompts.
CREATE TABLE memory (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('decision', 'convention', 'lesson')),
    title      TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body       TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    status     TEXT NOT NULL CHECK (status IN ('proposed', 'accepted')),
    source     TEXT NOT NULL DEFAULT '', -- '' = the user; else the proposing agent's id
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE INDEX memory_project ON memory (project_id, status, id);
