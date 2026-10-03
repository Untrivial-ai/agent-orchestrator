-- +goose Up
ALTER TABLE conversation_branches ADD COLUMN purpose TEXT NOT NULL DEFAULT ''
    CHECK (purpose IN ('', 'main', 'side'));
ALTER TABLE conversation_branches ADD COLUMN label TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_conversation_branches_purpose
    ON conversation_branches(conversation_id, purpose, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_conversation_branches_purpose;
ALTER TABLE conversation_branches DROP COLUMN label;
ALTER TABLE conversation_branches DROP COLUMN purpose;
