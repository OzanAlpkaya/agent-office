-- +goose Up
-- Phase 15 (ADR 0049, 0050): agents run on Claude Code or the Codex CLI. cli is the agent's CLI; existing
-- agents are Claude Code's. A Codex session's id is chosen by Codex, not by the daemon: cli_session_id
-- keeps it (learned from the session's first hook) so a resume can continue that conversation; empty
-- for Claude Code, whose session id is the daemon's.
ALTER TABLE agents ADD COLUMN cli TEXT NOT NULL DEFAULT 'claude' CHECK (cli IN ('claude', 'codex'));
ALTER TABLE sessions ADD COLUMN cli_session_id TEXT NOT NULL DEFAULT '' CHECK (length(cli_session_id) <= 64);
