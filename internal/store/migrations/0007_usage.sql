-- +goose Up
-- Token usage (ADR 0026): one row per API message of an agent session, from the API socket
-- (sandboxed agents) or the session transcript (host mode). Numbers and ids only; cost is
-- computed when read, from the price table. Re-reading a transcript or a stream's later
-- message_delta upserts the same row.
CREATE TABLE usage_messages (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     TEXT NOT NULL,
    agent_id       TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    task_id        TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    source         TEXT NOT NULL CHECK (source IN ('api', 'transcript')),
    message_id     TEXT NOT NULL CHECK (length(message_id) BETWEEN 1 AND 128),
    request_id     TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
    model          TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 128),
    speed          TEXT NOT NULL DEFAULT '',
    inference_geo  TEXT NOT NULL DEFAULT '',
    input_tokens   INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens  INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read     INTEGER NOT NULL DEFAULT 0 CHECK (cache_read >= 0),
    cache_write_5m INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_5m >= 0),
    cache_write_1h INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_1h >= 0),
    at             DATETIME NOT NULL, -- first seen
    updated_at     DATETIME NOT NULL,
    UNIQUE (session_id, message_id)
);
CREATE INDEX usage_messages_agent ON usage_messages (agent_id);
CREATE INDEX usage_messages_task ON usage_messages (task_id);

-- Per-session counters from the main agent's hooks, and Claude Code's own cost estimate
-- (cost-state in a host-mode transcript), shown next to agent-office's.
ALTER TABLE sessions ADD COLUMN turns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN tool_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN cli_cost_micro_usd INTEGER; -- money is integer (ADR 0026)

-- Retention (ADR 0028) deletes old events by time.
CREATE INDEX events_at ON events (at);
