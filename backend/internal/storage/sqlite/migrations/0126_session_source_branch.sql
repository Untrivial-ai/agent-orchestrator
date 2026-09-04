-- +goose Up
-- An imported conversation records the branch it actually ran on. That is not
-- always the branch its AO session ends up owning: git allows one checkout per
-- branch, so when the original is already checked out in the user's own clone
-- the session is created on a fresh branch instead.
--
-- Before this column those two facts shared one field, so keeping the import
-- working meant discarding the only link back to the conversation's pull
-- request, and every imported session sat in one column awaiting a PR that
-- could never be found. Recording the source branch separately lets the SCM
-- observer match on it while the worktree keeps whatever branch it could get.
ALTER TABLE sessions
    ADD COLUMN source_branch TEXT NOT NULL DEFAULT '';

-- Matching a pull request scans sessions by branch, so the new column needs the
-- same access path the existing one has.
CREATE INDEX IF NOT EXISTS idx_sessions_source_branch
    ON sessions (source_branch)
    WHERE source_branch <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_sessions_source_branch;
ALTER TABLE sessions DROP COLUMN source_branch;
