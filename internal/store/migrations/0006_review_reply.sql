-- +goose Up
-- A worker in a review follow-up round may answer "no change needed" with a reason instead of
-- committing (ADR 0022, amended in Phase 6). The reason is stored on the review it answers
-- and posted on the pull request as a comment; reply_url is that comment, empty until posted.
ALTER TABLE reviews ADD COLUMN reply TEXT NOT NULL DEFAULT '' CHECK (length(reply) <= 4000);
ALTER TABLE reviews ADD COLUMN reply_url TEXT NOT NULL DEFAULT '';
