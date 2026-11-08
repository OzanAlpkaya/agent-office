-- +goose Up
-- Phase 10 (ADR 0039): paired devices. A device is a browser (or a script) the user paired with a
-- one-time code; it authenticates with a random token whose SHA-256 is stored here, never the
-- token itself. Devices belong to the daemon's data dir, not to a project. Revoking deletes the row.
CREATE TABLE devices (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    kind         TEXT NOT NULL CHECK (kind IN ('browser', 'token')),
    token_hash   TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    paired_via   TEXT NOT NULL DEFAULT '' CHECK (length(paired_via) <= 200),
    created_at   DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL
);
