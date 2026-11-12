-- +goose Up
-- Phase 19 (ADR 0061-0063; ADR 0031 amended).

-- Daemon-wide settings changed in the app (ADR 0061). One row; absent until the user saves one. A NULL
-- max_agents means --max-agents.
CREATE TABLE daemon_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    max_agents INTEGER CHECK (max_agents IS NULL OR max_agents BETWEEN 1 AND 32),
    updated_at DATETIME NOT NULL
);

-- A sub-task replaced by another (ADR 0062): closed as done, with what replaced it: a task id, or
-- "spec:<version>" for a plan revision that dropped it.
ALTER TABLE tasks ADD COLUMN superseded_by TEXT NOT NULL DEFAULT '' CHECK (length(superseded_by) <= 64);

-- The user's change request on a plan version (ADR 0031 amended), for the Architect to revise.
ALTER TABLE specs ADD COLUMN change_request TEXT NOT NULL DEFAULT '' CHECK (length(change_request) <= 4000);
ALTER TABLE specs ADD COLUMN change_requested_at DATETIME;

-- Why a session ended (ADR 0051's exit reasons), for the task modal's session history.
ALTER TABLE sessions ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '' CHECK (length(exit_reason) <= 64);

-- A worker's report_done: its summary and the checks it says it ran. Agent-written text, kept in this
-- local database and shown as text.
CREATE TABLE task_reports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    agent_id     TEXT REFERENCES agents (id) ON DELETE SET NULL,
    summary      TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 2000),
    verification TEXT NOT NULL DEFAULT '' CHECK (length(verification) <= 2000),
    created_at   DATETIME NOT NULL
);
CREATE INDEX task_reports_task ON task_reports (task_id, id);
