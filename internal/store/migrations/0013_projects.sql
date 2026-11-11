-- +goose Up
-- Phase 14 (ADR 0046): projects are first-class. settings holds what a project overrides of the daemon's
-- flags (pack, models, GitHub, budgets, agent cap), as JSON the engine decodes strictly; '{}' overrides
-- nothing. removed_at hides a removed project and keeps its data: adding the same repository again brings
-- it back with its history.
ALTER TABLE projects ADD COLUMN settings TEXT NOT NULL DEFAULT '{}' CHECK (length(settings) <= 20000);
ALTER TABLE projects ADD COLUMN removed_at DATETIME;
