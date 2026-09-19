package sqlite

import "testing"

func TestNativeHistoryUpgradePreservesLegacyRows(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 140)
	upTo(t, db, 146)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at) VALUES ('native', '/native', CURRENT_TIMESTAMP);
INSERT INTO sessions (id, project_id, num, activity_last_at, created_at, updated_at,
 latest_user_prompt, latest_assistant_update, provider_conversation_id)
VALUES ('native-1', 'native', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
 'question', 'answer', 'thread-A');
INSERT INTO conversations (id, scope, project_id, session_id, current_session_id, created_at, updated_at)
VALUES ('native-history', 'session', 'native', 'native-1', 'native-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO conversation_branches (id, conversation_id, session_id, provider_conversation_id, created_at)
VALUES ('native-root', 'native-history', 'native-1', 'thread-A', CURRENT_TIMESTAMP);
`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("upgrade populated main database: %v", err)
	}
	var prompt, answer, native string
	var unknownAssistantTime, unknownIdentityTime bool
	if err := db.QueryRow(`SELECT latest_user_prompt, latest_assistant_update, provider_conversation_id,
 latest_assistant_update_at IS NULL, native_identity_observed_at IS NULL
 FROM sessions WHERE id = 'native-1'`).Scan(&prompt, &answer, &native, &unknownAssistantTime, &unknownIdentityTime); err != nil {
		t.Fatal(err)
	}
	if prompt != "question" || answer != "answer" || native != "thread-A" || !unknownAssistantTime || !unknownIdentityTime {
		t.Fatalf("upgrade changed legacy facts or invented timestamps: %q %q %q %v %v", prompt, answer, native, unknownAssistantTime, unknownIdentityTime)
	}
	var scoped bool
	if err := db.QueryRow(`SELECT provider_ids_scoped FROM conversation_branches WHERE conversation_id = 'native-history'`).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if scoped {
		t.Fatal("upgrade changed existing provider ID encoding")
	}
}
