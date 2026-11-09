-- +goose Up
-- UI/UX polish (ADR 0043): a human first name per agent, chosen by the daemon when the agent starts
-- and kept for its lifetime. The id stays the key everywhere; the name is for people. '' for agents
-- recorded before this migration until the engine names them at its next start.
ALTER TABLE agents ADD COLUMN name TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 40);
