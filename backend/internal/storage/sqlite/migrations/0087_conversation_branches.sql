-- +goose Up
-- +goose StatementBegin
-- A conversation branch is a provider-thread lineage node. AO keeps every
-- sibling durable and changes only conversations.active_branch_id when the user
-- navigates or edits an earlier prompt. Timeline sequence numbers remain
-- conversation-scoped and immutable; fork_after_sequence is the inclusive
-- ancestry boundary used by branch-aware reads.
CREATE TABLE conversation_branches (
    id                       TEXT PRIMARY KEY,
    conversation_id          TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id               TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    provider_conversation_id TEXT NOT NULL DEFAULT '',
    parent_branch_id         TEXT REFERENCES conversation_branches(id) ON DELETE RESTRICT,
    fork_after_turn_id       TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replaced_turn_id         TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replacement_turn_id      TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL,
    fork_after_sequence      INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMP NOT NULL,
    UNIQUE (conversation_id, provider_conversation_id)
);

CREATE INDEX idx_conversation_branches_lineage
    ON conversation_branches(conversation_id, parent_branch_id, fork_after_sequence);

ALTER TABLE conversations ADD COLUMN active_branch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_turns ADD COLUMN branch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_messages ADD COLUMN branch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_activities ADD COLUMN branch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_provider_events ADD COLUMN branch_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_conversation_turns_branch
    ON conversation_turns(branch_id, requested_at);
CREATE INDEX idx_conversation_messages_branch
    ON conversation_messages(branch_id, sequence);
CREATE INDEX idx_conversation_activities_branch
    ON conversation_activities(branch_id, sequence);
CREATE INDEX idx_conversation_provider_events_branch
    ON conversation_provider_events(branch_id, id);

-- Every pre-branching conversation becomes one root without rewriting or
-- discarding any of its durable history.
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id, fork_after_sequence, created_at
)
SELECT c.id || ':root', c.id, c.current_session_id,
       COALESCE(s.provider_conversation_id, ''), 0, c.created_at
FROM conversations c
LEFT JOIN sessions s ON s.id = c.current_session_id;

UPDATE conversations SET active_branch_id = id || ':root';
UPDATE conversation_turns SET branch_id = conversation_id || ':root';
UPDATE conversation_messages SET branch_id = conversation_id || ':root';
UPDATE conversation_activities SET branch_id = conversation_id || ':root';
UPDATE conversation_provider_events SET branch_id = conversation_id || ':root';
-- +goose StatementEnd

-- +goose StatementBegin
-- Writers may omit branch_id. The active branch is assigned in the database so
-- every existing projection path, including provider-event transactions, follows
-- the same controller head without duplicating branch logic in each caller.
CREATE TRIGGER conversation_turns_branch_insert
AFTER INSERT ON conversation_turns
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_turns
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_messages_branch_insert
AFTER INSERT ON conversation_messages
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_messages
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_activities_branch_insert
AFTER INSERT ON conversation_activities
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_activities
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_provider_events_branch_insert
AFTER INSERT ON conversation_provider_events
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_provider_events
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

-- Fresh Chat startup creates AO's conversation before the provider returns its
-- thread handle. Bind that handle to the root exactly once when Session Manager
-- persists the successful controller result. Later branch activations never
-- rewrite lineage identity because their target branch already has a handle.
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS conversation_branch_root_provider_update;
DROP TRIGGER IF EXISTS conversation_provider_events_branch_insert;
DROP TRIGGER IF EXISTS conversation_activities_branch_insert;
DROP TRIGGER IF EXISTS conversation_messages_branch_insert;
DROP TRIGGER IF EXISTS conversation_turns_branch_insert;

DROP INDEX IF EXISTS idx_conversation_provider_events_branch;
DROP INDEX IF EXISTS idx_conversation_activities_branch;
DROP INDEX IF EXISTS idx_conversation_messages_branch;
DROP INDEX IF EXISTS idx_conversation_turns_branch;
DROP INDEX IF EXISTS idx_conversation_branches_lineage;
DROP TABLE IF EXISTS conversation_branches;
-- +goose StatementEnd

-- +goose StatementBegin
-- The branch columns are additive durable history metadata. As with migration
-- 0072, older code safely ignores them, while rebuilding five live tables merely
-- to remove columns would add destructive downgrade risk.
SELECT 1;
-- +goose StatementEnd
