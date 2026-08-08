package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRecognizesAgentSwitchSchemaFromVersions0080And0081(t *testing.T) {
	db := openAgentSwitchMigrationTestDB(t)
	upTo(t, db, 79)
	applyLegacyAgentSwitchMigrations(t, db, "migrations/0080_agent_switching.sql", "migrations/0081_finalized_agent_handoff.sql")
	assertAgentSwitchMigrationHistoryRepaired(t, db)
}

func TestMigrateRecognizesAgentSwitchSchemaFromVersions0081And0082(t *testing.T) {
	db := openAgentSwitchMigrationTestDB(t)
	upTo(t, db, 80)
	applyLegacyAgentSwitchMigrations(t, db, "migrations/0081_agent_switching.sql", "migrations/0082_finalized_agent_handoff.sql")
	assertAgentSwitchMigrationHistoryRepaired(t, db)
}

func openAgentSwitchMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyLegacyAgentSwitchMigrations(t *testing.T, db *sql.DB, switchPath, handoffPath string) {
	t.Helper()
	agentSwitchMigration, err := migrationsFS.ReadFile("migrations/0083_agent_switching.sql")
	if err != nil {
		t.Fatalf("read agent-switch migration: %v", err)
	}
	finalHandoffMigration, err := migrationsFS.ReadFile("migrations/0084_finalized_agent_handoff.sql")
	if err != nil {
		t.Fatalf("read finalized-handoff migration: %v", err)
	}
	goose.SetBaseFS(fstest.MapFS{
		switchPath:  {Data: agentSwitchMigration},
		handoffPath: {Data: finalHandoffMigration},
	})
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatalf("apply legacy agent-switch migrations: %v", err)
	}
}

func assertAgentSwitchMigrationHistoryRepaired(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrate(db); err != nil {
		t.Fatalf("migrate legacy agent-switch database: %v", err)
	}
	for _, version := range []int64{80, 81, 82, 83, 84} {
		var applied int
		if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied = %d, want 1", version, applied)
		}
	}
	if got, err := reviewHasSessionHarnessUnique(db); err != nil || !got {
		t.Fatalf("review per-harness shape = %v, err = %v", got, err)
	}
	for table, column := range map[string]string{
		"sessions":       "browser_capability_verifier",
		"agent_switches": "final_handoff_path",
	} {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
		).Scan(&count); err != nil {
			t.Fatalf("read %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Fatalf("%s.%s count = %d, want 1", table, column, count)
		}
	}
	var primeHarnessShape int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table'
  AND name = 'sessions'
  AND instr(COALESCE(sql, ''), '''prime-agent''') > 0`).Scan(&primeHarnessShape); err != nil {
		t.Fatalf("read Prime Agent session shape: %v", err)
	}
	if primeHarnessShape != 1 {
		t.Fatalf("Prime Agent session shape = %d, want 1", primeHarnessShape)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
}
