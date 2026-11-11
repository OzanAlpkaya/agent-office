-- +goose Up
-- Phase 17 (ADR 0054, 0055; ADR 0035 amended).

-- A session that got a prompt has a transcript even if no turn finished (Claude Code writes a fork's
-- transcript when it gets a message): a resume continues it instead of skipping it (ADR 0035).
ALTER TABLE sessions ADD COLUMN prompted BOOLEAN NOT NULL DEFAULT 0;

-- The Tech Lead's report of a goal (ADR 0055): its summary and the commit message for the squash merge.
-- One row per report_done; the latest counts. Agent-written text, kept in this local database.
CREATE TABLE goal_reports (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_id        TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    agent_id       TEXT REFERENCES agents (id) ON DELETE SET NULL,
    summary        TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 2000),
    commit_message TEXT NOT NULL DEFAULT '' CHECK (length(commit_message) <= 4000),
    created_at     DATETIME NOT NULL
);
CREATE INDEX goal_reports_goal ON goal_reports (goal_id, id);

-- The user's squash merges of a goal's QA branch into the project's target branch, and their pushes
-- (ADR 0055).
CREATE TABLE goal_merges (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_id       TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    qa_run_id     INTEGER REFERENCES qa_runs (id) ON DELETE SET NULL,
    target        TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 255),
    source_head   TEXT NOT NULL,
    target_before TEXT NOT NULL,
    commit_sha    TEXT NOT NULL,
    message       TEXT NOT NULL CHECK (length(message) BETWEEN 1 AND 4000),
    created_at    DATETIME NOT NULL,
    pushed_at     DATETIME,
    push_remote   TEXT NOT NULL DEFAULT '' CHECK (length(push_remote) <= 100)
);
CREATE INDEX goal_merges_goal ON goal_merges (goal_id, id);
