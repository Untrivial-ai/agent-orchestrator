package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Exercise both direct upgrade paths with independent databases, but pay for
// the shared historical schema only once. VACUUM INTO snapshots real migrations
// (including WAL contents), not a hand-maintained or globally cached fixture.
func TestMigrateCheckpointProvenance(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 109)
	snapshotPath := filepath.Join(t.TempDir(), "version-109.db")
	if _, err := db.Exec(`VACUUM INTO ?`, snapshotPath); err != nil {
		t.Fatalf("snapshot version 109: %v", err)
	}

	t.Run("from_109_after_latest_prompt_timestamp", func(t *testing.T) {
		snapshot, err := sql.Open("sqlite", "file:"+snapshotPath+pragmas)
		if err != nil {
			t.Fatalf("open version 109 snapshot: %v", err)
		}
		snapshot.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = snapshot.Close() })
		var promptTimestampColumns, checkpointColumns int
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'latest_user_prompt_at'`).Scan(&promptTimestampColumns); err != nil {
			t.Fatalf("read prompt timestamp column: %v", err)
		}
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name LIKE 'conversation_checkpoint_%'`).Scan(&checkpointColumns); err != nil {
			t.Fatalf("read pre-upgrade checkpoint columns: %v", err)
		}
		if promptTimestampColumns != 1 || checkpointColumns != 0 {
			t.Fatalf("version 109 schema timestamp=%d checkpoint=%d, want 1 and 0", promptTimestampColumns, checkpointColumns)
		}
		if err := migrate(snapshot); err != nil {
			t.Fatalf("migrate version 109 database: %v", err)
		}
		var applied, steerTables, editTables int
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id BETWEEN 109 AND 112 AND is_applied = 1`).Scan(&applied); err != nil {
			t.Fatalf("read migration ledger: %v", err)
		}
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversation_steer_deliveries'`).Scan(&steerTables); err != nil {
			t.Fatalf("read steer delivery table: %v", err)
		}
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversation_edit_deliveries'`).Scan(&editTables); err != nil {
			t.Fatalf("read edit delivery table: %v", err)
		}
		if err := snapshot.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name LIKE 'conversation_checkpoint_%'`).Scan(&checkpointColumns); err != nil {
			t.Fatalf("read checkpoint columns: %v", err)
		}
		if applied != 4 || steerTables != 1 || editTables != 1 || checkpointColumns != 4 {
			t.Fatalf("upgraded schema applied=%d steer=%d edit=%d checkpoint=%d, want 4,1,1,4", applied, steerTables, editTables, checkpointColumns)
		}
	})

	t.Run("from_populated_main_139", func(t *testing.T) {
		// The original remains at 109 after the clone's upgrade. Advancing it
		// to main proves 140/141 upgrade an actual pre-PR populated database.
		var sourceVersion int
		if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&sourceVersion); err != nil {
			t.Fatal(err)
		}
		if sourceVersion != 109 {
			t.Fatalf("clone upgrade mutated source version to %d, want 109", sourceVersion)
		}
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
	})
}
