-- +goose Up
-- Phase 18 review (ADR 0058 amended): the user's profile, daemon-wide: the name on their nameplate and
-- their character's look in every project's office. One row; absent until the user saves one.
CREATE TABLE profile (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    name       TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    look       TEXT NOT NULL DEFAULT '{}' CHECK (length(look) <= 1000),
    updated_at DATETIME NOT NULL
);
