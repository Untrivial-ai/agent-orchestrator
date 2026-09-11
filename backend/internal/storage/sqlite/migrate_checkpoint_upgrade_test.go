package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The recovery migrations must upgrade an actual current-main database, not
// merely a fresh database from the old PR's drafts stack.
func TestMigrateCheckpointProvenanceFromMain139(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 139)
	if _, err := db.Exec(`INSERT INTO projects (id, path, registered_at)
		VALUES ('checkpoint-upgrade', '/repos/checkpoint-upgrade', CURRENT_TIMESTAMP);
		INSERT INTO sessions (id, project_id, num, activity_last_at, created_at, updated_at,
			latest_user_prompt, latest_user_prompt_at, latest_assistant_update)
		VALUES ('checkpoint-upgrade-1', 'checkpoint-upgrade', 1, CURRENT_TIMESTAMP,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'Say hi to', '2026-09-07 14:35:15', 'Hi!')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("upgrade from main: %v", err)
	}
	var applied, checkpointColumns, historyPolicyColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version
		WHERE version_id IN (140, 141) AND is_applied = 1`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions')
		WHERE name LIKE 'conversation_checkpoint_%'`).Scan(&checkpointColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('session_interface_transitions')
		WHERE name = 'history_policy'`).Scan(&historyPolicyColumns); err != nil {
		t.Fatal(err)
	}
	if applied != 2 || checkpointColumns != 4 || historyPolicyColumns != 1 {
		t.Fatalf("upgraded schema: migrations=%d checkpoints=%d policy=%d, want 2/4/1",
			applied, checkpointColumns, historyPolicyColumns)
	}
	var prompt, assistant, state, generation, nativeID string
	var unsettled bool
	if err := db.QueryRow(`SELECT latest_user_prompt, latest_assistant_update,
		conversation_checkpoint_state, conversation_checkpoint_generation,
		conversation_checkpoint_native_id, conversation_checkpoint_unsettled
		FROM sessions WHERE id = 'checkpoint-upgrade-1'`).Scan(
		&prompt, &assistant, &state, &generation, &nativeID, &unsettled); err != nil {
		t.Fatal(err)
	}
	if prompt != "Say hi to" || assistant != "Hi!" || state != "legacy" ||
		generation != "" || nativeID != "" || unsettled {
		t.Fatalf("upgrade changed or trusted legacy checkpoint: %q/%q %q %q %q %v",
			prompt, assistant, state, generation, nativeID, unsettled)
	}
}
