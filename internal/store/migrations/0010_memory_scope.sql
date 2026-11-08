-- +goose Up
-- Phase 9 (ADR 0037): relevant project memory. An entry scoped to roles applies to agents of those
-- roles; one scoped to paths applies to agents whose task declares files that overlap them.
-- JSON arrays; [] = everyone / every file.
ALTER TABLE memory ADD COLUMN roles TEXT NOT NULL DEFAULT '[]';
ALTER TABLE memory ADD COLUMN paths TEXT NOT NULL DEFAULT '[]';
