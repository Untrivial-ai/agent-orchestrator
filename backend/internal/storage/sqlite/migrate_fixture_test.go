package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// migrationFixture runs a group's shared historical migrations once. Each call
// to the returned function creates a separate database; callers still run the
// migration under test after seeding their own legacy data and version ledger.
// Nothing is cached across tests, and the fixture comes from real migrations,
// not a schema dump that can drift from the embedded SQL.
func migrationFixture(t *testing.T, version int64) func(*testing.T) string {
	t.Helper()
	sourceDir := t.TempDir()
	db, err := sql.Open("sqlite", databaseURI(sourceDir)+pragmas)
	if err != nil {
		t.Fatalf("open migration fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close migration fixture: %v", err)
		}
	}()
	upTo(t, db, version)
	// VACUUM INTO includes committed WAL contents. Copying the live ao.db file
	// directly can silently omit the schema or ledger we just created.
	snapshotPath := filepath.Join(sourceDir, "snapshot.db")
	if _, err := db.Exec(`VACUUM INTO ?`, snapshotPath); err != nil {
		t.Fatalf("snapshot migration fixture: %v", err)
	}
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read migration fixture: %v", err)
	}
	return func(t *testing.T) string {
		t.Helper()
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "ao.db"), snapshot, 0o600); err != nil {
			t.Fatalf("clone migration fixture: %v", err)
		}
		return dataDir
	}
}

func TestMigrationFixtureIsolatesSchemaDataAndLedger(t *testing.T) {
	fixture := migrationFixture(t, 12)
	for _, name := range []string{"first", "after_first_clone_closes"} {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", databaseURI(fixture(t))+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			var version, projects, leakedTables int
			if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil {
				t.Fatalf("read copied migration ledger: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
				t.Fatalf("read copied schema: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'fixture_only'`).Scan(&leakedTables); err != nil {
				t.Fatal(err)
			}
			if version != 12 || projects != 0 || leakedTables != 0 {
				t.Fatalf("fixture version/projects/leaked tables = %d/%d/%d, want 12/0/0", version, projects, leakedTables)
			}
			if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at) VALUES ('fixture-only', '/fixture-only', CURRENT_TIMESTAMP);
CREATE TABLE fixture_only (id INTEGER);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1);
`); err != nil {
				t.Fatalf("mutate isolated clone: %v", err)
			}
		})
	}
}
