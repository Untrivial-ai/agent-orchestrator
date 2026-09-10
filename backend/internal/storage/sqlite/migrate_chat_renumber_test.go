package sqlite

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrateRecognizesPreRenumberedChatSchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 79)

	// Reproduce a database opened by the feature branch when the exact same Chat
	// migrations were numbered 0052-0065. Remove main's current 0052 schema so
	// its burned version must be released and applied under its real meaning.
	if _, err := db.Exec(`
UPDATE app_settings SET default_session_mode = 'chat';
DROP TRIGGER IF EXISTS usage_sources_cdc_update;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP VIEW IF EXISTS usage_session_integrity;
DROP VIEW IF EXISTS usage_codex_pending_children;
DROP VIEW IF EXISTS usage_codex_source_discovery;
DROP TABLE IF EXISTS model_usage_events;
DROP TABLE IF EXISTS usage_sources;
DROP TABLE IF EXISTS usage_bindings;
DELETE FROM goose_db_version WHERE version_id = 52 OR version_id BETWEEN 66 AND 79;
`); err != nil {
		t.Fatalf("seed pre-renumbered schema: %v", err)
	}
	for version := int64(52); version <= 65; version++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("seed old Chat migration %d: %v", version, err)
		}
	}

	// Assert the repair itself preserves the preference before later migrations
	// run. The product-default migration writes Chat too, so a final-state-only
	// assertion would no longer detect a repair that reset this row first.
	if err := repairRenumberedChatMigrationHistory(db); err != nil {
		t.Fatalf("repair pre-renumbered Chat migration history: %v", err)
	}
	var preservedMode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&preservedMode); err != nil {
		t.Fatalf("read app setting after repair: %v", err)
	}
	if preservedMode != "chat" {
		t.Fatalf("default mode after repair = %q, want preserved chat", preservedMode)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate pre-renumbered Chat database: %v", err)
	}

	var defaultMode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&defaultMode); err != nil {
		t.Fatalf("read preserved app setting: %v", err)
	}
	if defaultMode != "chat" {
		t.Fatalf("default mode = %q, want preserved chat", defaultMode)
	}
	for table, wantColumns := range expectedUsageTableColumns {
		if got := tableColumns(t, db, table); !reflect.DeepEqual(got, wantColumns) {
			t.Errorf("%s columns = %v, want %v", table, got, wantColumns)
		}
	}

	var mapped int
	if err := db.QueryRow(`
SELECT COUNT(DISTINCT version_id) FROM goose_db_version
WHERE is_applied = 1 AND version_id BETWEEN 66 AND 79
`).Scan(&mapped); err != nil {
		t.Fatalf("read mapped Chat migrations: %v", err)
	}
	if mapped != 14 {
		t.Fatalf("mapped Chat migrations = %d, want 14", mapped)
	}

	// A second startup must be a no-op, not another attempt to replay either set.
	if err := migrate(db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
}
