-- +goose Up
-- Phase 3 (ADR 0014): every import of a sandboxed agent's branch into the main repository.
-- flagged holds commit ids and check results only (JSON), never commit content.
CREATE TABLE imports (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    trigger  TEXT NOT NULL CHECK (trigger IN ('exit', 'manual', 'restart', 'remove', 'save_work')),
    outcome  TEXT NOT NULL
             CHECK (outcome IN ('no_commits', 'created', 'fast_forward', 'up_to_date', 'diverged')),
    branch   TEXT NOT NULL DEFAULT '',
    head     TEXT NOT NULL DEFAULT '',
    previous TEXT NOT NULL DEFAULT '',
    commits  INTEGER NOT NULL DEFAULT 0,
    flagged  TEXT NOT NULL DEFAULT '[]',
    at       DATETIME NOT NULL
);
CREATE INDEX imports_agent ON imports (agent_id, id);
