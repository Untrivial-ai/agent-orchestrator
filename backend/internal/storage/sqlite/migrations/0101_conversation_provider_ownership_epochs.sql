-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- A provider conversation may become AO's active owner more than once. For
-- example, A -> B -> A resumes A's retained native conversation but still needs
-- a new boundary node after B so AO history keeps the ownership epochs ordered.
-- The 0087 uniqueness constraint treated the provider handle as a branch
-- identity and rejected that valid return activation. SQLite cannot drop a
-- table constraint in place, so rebuild the table without that constraint.
PRAGMA foreign_keys=OFF;

CREATE TABLE conversation_branches_next (
    id                       TEXT PRIMARY KEY,
    conversation_id          TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id               TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    provider_conversation_id TEXT NOT NULL DEFAULT '',
    parent_branch_id         TEXT REFERENCES conversation_branches_next(id) ON DELETE RESTRICT,
    fork_after_turn_id       TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replaced_turn_id         TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replacement_turn_id      TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL,
    fork_after_sequence      INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMP NOT NULL
);

INSERT INTO conversation_branches_next (
    id, conversation_id, session_id, provider_conversation_id,
    parent_branch_id, fork_after_turn_id, replaced_turn_id,
    replacement_turn_id, fork_after_sequence, created_at
)
SELECT
    id, conversation_id, session_id, provider_conversation_id,
    parent_branch_id, fork_after_turn_id, replaced_turn_id,
    replacement_turn_id, fork_after_sequence, created_at
FROM conversation_branches;

DROP TRIGGER IF EXISTS conversation_branch_root_provider_update;
DROP TABLE conversation_branches;
ALTER TABLE conversation_branches_next RENAME TO conversation_branches;

CREATE INDEX idx_conversation_branches_lineage
    ON conversation_branches(conversation_id, parent_branch_id, fork_after_sequence);
CREATE INDEX idx_conversation_branches_provider_identity
    ON conversation_branches(conversation_id, provider_conversation_id);

CREATE TRIGGER conversation_branch_root_provider_update
AFTER UPDATE OF provider_conversation_id ON sessions
WHEN OLD.provider_conversation_id = '' AND NEW.provider_conversation_id <> ''
BEGIN
    UPDATE conversation_branches
    SET provider_conversation_id = NEW.provider_conversation_id
    WHERE parent_branch_id IS NULL
      AND provider_conversation_id = ''
      AND id IN (
          SELECT active_branch_id
          FROM conversations
          WHERE current_session_id = NEW.id
      );
END;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Once one provider id has multiple ownership epochs, restoring the old unique
-- constraint would discard lineage or make downgrade fail. Keep the relaxed,
-- backward-compatible table shape.
SELECT 1;
-- +goose StatementEnd
