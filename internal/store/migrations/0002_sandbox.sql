-- +goose Up
-- Phase 3 (ADR 0014, 0016): how an agent runs and what kind of workspace it has.
-- Existing rows are host-mode agents with linked worktrees, which the defaults describe.
ALTER TABLE agents ADD COLUMN sandbox TEXT NOT NULL DEFAULT 'host'
    CHECK (sandbox IN ('host', 'podman'));
ALTER TABLE worktrees ADD COLUMN kind TEXT NOT NULL DEFAULT 'worktree'
    CHECK (kind IN ('worktree', 'clone'));
