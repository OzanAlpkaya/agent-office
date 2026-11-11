-- +goose Up
-- Phase 18 (ADR 0059): agent-office branches the user deleted, with the head each pointed at, so the
-- archive can name them and a branch can be restored (git branch <name> <head>). goal_id is set when
-- the branch was deleted from a done goal; empty for the project-wide clean-up.
CREATE TABLE deleted_branches (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    goal_id    TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    head       TEXT NOT NULL CHECK (length(head) BETWEEN 40 AND 64),
    class      TEXT NOT NULL CHECK (class IN ('merged', 'squashed', 'unmerged')),
    deleted_at DATETIME NOT NULL
);
CREATE INDEX deleted_branches_goal ON deleted_branches (goal_id, id);
CREATE INDEX deleted_branches_project ON deleted_branches (project_id, id);
