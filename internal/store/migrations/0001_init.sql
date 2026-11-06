-- +goose Up
-- Data model: ADR 0011. Timestamps are UTC.
-- Migrations are forward-only: there are no down migrations, so nothing in the
-- tooling can drop tables by accident. Schema changes get a new numbered file.

CREATE TABLE projects (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    repo_path  TEXT NOT NULL UNIQUE,
    slug       TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL
);

CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    title       TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 20000),
    status      TEXT NOT NULL DEFAULT 'todo'
                CHECK (status IN ('todo', 'in_progress', 'review', 'done', 'blocked')),
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);
CREATE INDEX tasks_project ON tasks (project_id, created_at);

CREATE TABLE agents (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    task_id               TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    model                 TEXT NOT NULL DEFAULT '',
    inherit_user_settings BOOLEAN NOT NULL,
    state                 TEXT NOT NULL,
    activity              TEXT NOT NULL DEFAULT '',
    started_at            DATETIME NOT NULL,
    exited_at             DATETIME,
    exit_code             INTEGER,
    exit_reason           TEXT NOT NULL DEFAULT '',
    removed_at            DATETIME
);
CREATE INDEX agents_task ON agents (task_id);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY, -- the CLI's --session-id
    agent_id   TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    started_at DATETIME NOT NULL,
    ended_at   DATETIME
);
CREATE INDEX sessions_agent ON sessions (agent_id);

CREATE TABLE worktrees (
    agent_id    TEXT PRIMARY KEY REFERENCES agents (id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    path        TEXT NOT NULL UNIQUE,
    branch      TEXT NOT NULL,
    base_commit TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('active', 'kept', 'removed')),
    created_at  DATETIME NOT NULL,
    removed_at  DATETIME
);

-- Reduced hook events only; never payload content (ADR 0003).
CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    kind       TEXT NOT NULL,
    hook       TEXT NOT NULL DEFAULT '',
    tool       TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL,
    at         DATETIME NOT NULL
);
CREATE INDEX events_agent ON events (agent_id, id);
