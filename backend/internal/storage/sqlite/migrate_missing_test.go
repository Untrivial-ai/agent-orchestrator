package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateAppliesMissingMigrationsBeforeCurrentVersion covers databases
// created by builds whose migration history advanced past migrations that were
// later added or renumbered. Goose rejects that history by default, which
// prevents the daemon from starting even though the missing migrations can be
// applied normally.
func TestMigrateAppliesMissingMigrationsBeforeCurrentVersion(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 27)
	if _, err := db.Exec(`
INSERT INTO projects (
	id, path, repo_origin_url, display_name, registered_at, config, kind
) VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		"project-1",
		`C:\src\project-1`,
		"https://example.com/project-1.git",
		"Project One",
		"2026-07-25T00:00:00Z",
		`{"worker":{"agent":"codex"}}`,
		"single_repo",
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (46, 1)`,
	); err != nil {
		t.Fatalf("seed later migration version: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with missing earlier versions: %v", err)
	}

	var applied int
	if err := db.QueryRow(`
SELECT COUNT(DISTINCT version_id)
FROM goose_db_version
WHERE is_applied = 1 AND version_id IN (28, 29, 30, 31)
`).Scan(&applied); err != nil {
		t.Fatalf("query applied migrations: %v", err)
	}
	if applied != 4 {
		t.Fatalf("applied migrations 28-31 = %d, want 4", applied)
	}

	var path, displayName string
	if err := db.QueryRow(
		`SELECT path, display_name FROM projects WHERE id = ?`,
		"project-1",
	).Scan(&path, &displayName); err != nil {
		t.Fatalf("query preserved project: %v", err)
	}
	if path != `C:\src\project-1` || displayName != "Project One" {
		t.Fatalf("preserved project = (%q, %q), want Windows path and display name unchanged", path, displayName)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}
}

// TestMigrateAppliesBrowserVerifierAfterUpstreamVersion41 guards the migration
// number collision with upstream's 0041_notification_resolution migration.
// A database that already recorded version 41 must still receive the browser
// capability column from the append-only 0081 migration.
func TestMigrateAppliesBrowserVerifierAfterUpstreamVersion41(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 40)
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (41, 1)`); err != nil {
		t.Fatalf("seed upstream migration version 41: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with upstream version 41: %v", err)
	}

	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	if !strings.Contains(schema, "browser_capability_verifier") {
		t.Fatalf("sessions schema missing browser capability verifier after migration:\n%s", schema)
	}

	var applied int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM goose_db_version WHERE version_id = 81 AND is_applied = 1",
	).Scan(&applied); err != nil {
		t.Fatalf("query browser verifier migration version: %v", err)
	}
	if applied != 1 {
		t.Fatalf("browser verifier migration applied rows = %d, want 1", applied)
	}
}

func TestMigrateRecognizesBrowserVerifierFromEarlierBranchBuild(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 48)
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN browser_capability_verifier TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("seed verifier from earlier branch build: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with pre-existing verifier: %v", err)
	}

	var columns, applied int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'browser_capability_verifier'`,
	).Scan(&columns); err != nil {
		t.Fatalf("query browser verifier column: %v", err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM goose_db_version WHERE version_id = 81 AND is_applied = 1",
	).Scan(&applied); err != nil {
		t.Fatalf("query browser verifier migration version: %v", err)
	}
	if columns != 1 || applied != 1 {
		t.Fatalf("browser verifier state = columns %d, applied rows %d; want 1, 1", columns, applied)
	}
}

func TestMigrateRepairsQueuedTurnPromotionColumnsWhenVersionAlreadyClaimed(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 85)
	if _, err := db.Exec(`ALTER TABLE conversation_turns ADD COLUMN promotion_started_at TIMESTAMP`); err != nil {
		t.Fatalf("seed promotion timestamp from earlier branch build: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE conversation_turns ADD COLUMN promoted_to_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL`); err != nil {
		t.Fatalf("seed promoted turn from earlier branch build: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (86, 1)`); err != nil {
		t.Fatalf("seed claimed promotion migration: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with claimed promotion version: %v", err)
	}

	for _, column := range []string{"promotion_started_at", "promoted_to_turn_id"} {
		var columns int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('conversation_turns') WHERE name = ?`, column,
		).Scan(&columns); err != nil {
			t.Fatalf("query %s column: %v", column, err)
		}
		if columns != 1 {
			t.Errorf("conversation_turns.%s count = %d, want 1", column, columns)
		}
	}
	var applied int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM goose_db_version WHERE version_id = 89 AND is_applied = 1",
	).Scan(&applied); err != nil {
		t.Fatalf("query renumbered promotion migration version: %v", err)
	}
	if applied != 1 {
		t.Fatalf("promotion migration 89 applied rows = %d, want 1", applied)
	}
	var defaultBranchColumns int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('workspace_repos') WHERE name = 'default_branch'`,
	).Scan(&defaultBranchColumns); err != nil {
		t.Fatalf("query workspace default branch column: %v", err)
	}
	if defaultBranchColumns != 1 {
		t.Fatalf("workspace_repos.default_branch count = %d, want 1", defaultBranchColumns)
	}
}

func TestMigrateRepairsQueuedTurnPromotionWhenVersion88WasClaimed(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 87)
	if _, err := db.Exec(`ALTER TABLE conversation_turns ADD COLUMN promotion_started_at TIMESTAMP`); err != nil {
		t.Fatalf("seed promotion timestamp from version 88 branch build: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE conversation_turns ADD COLUMN promoted_to_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL`); err != nil {
		t.Fatalf("seed promoted turn from version 88 branch build: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (88, 1)`); err != nil {
		t.Fatalf("seed version 88 promotion migration: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with version 88 promotion schema: %v", err)
	}

	for table, column := range map[string]string{
		"sessions": "auto_inject_ci",
		"pr":       "auto_inject_ci",
	} {
		var columns int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
		).Scan(&columns); err != nil {
			t.Fatalf("query %s.%s: %v", table, column, err)
		}
		if columns != 1 {
			t.Fatalf("%s.%s count = %d, want 1", table, column, columns)
		}
	}
	for _, version := range []int64{88, 89} {
		var applied int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1", version,
		).Scan(&applied); err != nil {
			t.Fatalf("query migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied rows = %d, want 1", version, applied)
		}
	}
}

// TestMigrateAppliesAgentModelCatalogAfterUpstreamMigration covers a database
// that has already applied main's version 41. The catalog must use the next
// migration version so goose applies it independently.
func TestMigrateAppliesAgentModelCatalogAfterUpstreamMigration(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 41)

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database after upstream migration: %v", err)
	}

	var table string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'agent_model_catalog'`,
	).Scan(&table); err != nil {
		t.Fatalf("query agent model catalog table: %v", err)
	}
	if table != "agent_model_catalog" {
		t.Fatalf("model catalog table = %q, want agent_model_catalog", table)
	}
}
