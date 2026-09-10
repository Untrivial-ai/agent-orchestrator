package sqlite

import (
	"testing"
	"time"
)

func TestMigration0101AllowsRepeatedProviderOwnershipEpochs(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 99)

	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	mustExec(t, db, `
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('provider-epochs', '/tmp/provider-epochs', 'provider epochs', ?);
INSERT INTO sessions (
    id, project_id, num, harness, session_mode, activity_last_at,
    provider_conversation_id, created_at, updated_at
) VALUES (
    'provider-epochs-1', 'provider-epochs', 1, 'claude-code', 'chat', ?, '', ?, ?
);
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id,
    active_branch_id, created_at, updated_at
) VALUES (
    'conversation-1', 'session', 'provider-epochs', 'provider-epochs-1',
    'provider-epochs-1', 'conversation-1:root', ?, ?
);
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id,
    fork_after_sequence, created_at
) VALUES (
    'conversation-1:root', 'conversation-1', 'provider-epochs-1', '', 0, ?
);`, now, now, now, now, now, now)

	upTo(t, db, 101)

	// The session-owned trigger from 0087 must survive the table rebuild.
	mustExec(t, db, `
UPDATE sessions
SET provider_conversation_id = 'native-a', updated_at = ?
WHERE id = 'provider-epochs-1';`, now.Add(time.Second))
	var rootProviderID string
	if err := db.QueryRow(`
SELECT provider_conversation_id
FROM conversation_branches
WHERE id = 'conversation-1:root';`).Scan(&rootProviderID); err != nil {
		t.Fatalf("read rebound root provider id: %v", err)
	}
	if rootProviderID != "native-a" {
		t.Fatalf("root provider id = %q, want native-a", rootProviderID)
	}

	// Returning to native-a after another provider creates a new ownership node;
	// it must not collide with the retained root identity.
	mustExec(t, db, `
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id,
    parent_branch_id, fork_after_sequence, created_at
) VALUES (
    'conversation-1:return-a', 'conversation-1', 'provider-epochs-1',
    'native-a', 'conversation-1:root', 0, ?
);`, now.Add(2*time.Second))
	var epochs int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM conversation_branches
WHERE conversation_id = 'conversation-1'
  AND provider_conversation_id = 'native-a';`).Scan(&epochs); err != nil {
		t.Fatalf("count provider ownership epochs: %v", err)
	}
	if epochs != 2 {
		t.Fatalf("provider ownership epochs = %d, want 2", epochs)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check;`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check: %v", err)
	}
}
