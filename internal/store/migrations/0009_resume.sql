-- +goose Up
-- Phase 9 (ADR 0035): resuming an agent's conversation and restoring agents after a restart.

-- The session a resumed session continues (claude --resume <it> --fork-session); '' for a fresh one.
ALTER TABLE sessions ADD COLUMN resumed_from TEXT NOT NULL DEFAULT '';

-- What the agent was doing when the daemon stopped it (clean shutdown or crash): the state it is
-- restored from at the next start. '' = not to be restored.
ALTER TABLE agents ADD COLUMN resume_state TEXT NOT NULL DEFAULT '';

-- Sandboxed agents never load the user's settings (ADR 0003); earlier rows stored the daemon flag.
UPDATE agents SET inherit_user_settings = 0 WHERE sandbox = 'podman';
