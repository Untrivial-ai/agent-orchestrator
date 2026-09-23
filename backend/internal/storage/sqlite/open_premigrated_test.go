package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenPreMigratedSucceedsForMigratedDatabase verifies that OpenPreMigrated
// accepts a database that was created and migrated through the normal sqlite.Open
// path (or equivalently through openMigratedTestDB which calls migrate()).
func TestOpenPreMigratedSucceedsForMigratedDatabase(t *testing.T) {
	dataDir := t.TempDir()

	// Create and migrate a database through the normal production path.
	db := openMigratedTestDBIn(t, dataDir)
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated db: %v", err)
	}

	store, err := OpenPreMigrated(dataDir)
	if err != nil {
		t.Fatalf("OpenPreMigrated: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
}

// TestOpenPreMigratedRejectsNewEmptyDatabase verifies that OpenPreMigrated
// returns an error when the database has never been migrated (no
// goose_db_version table or no applied rows).
func TestOpenPreMigratedRejectsNewEmptyDatabase(t *testing.T) {
	dataDir := t.TempDir()
	if err := createEmptyDatabase(filepath.Join(dataDir, "ao.db")); err != nil {
		t.Fatalf("create empty database: %v", err)
	}

	_, err := OpenPreMigrated(dataDir)
	if err == nil {
		t.Fatal("OpenPreMigrated succeeded on empty database; want error")
	}
}

// TestOpenPreMigratedRejectsStaleMigrationVersion verifies that
// OpenPreMigrated rejects a database whose applied migration version is behind
// the current embedded migration version.
func TestOpenPreMigratedRejectsStaleMigrationVersion(t *testing.T) {
	dataDir := t.TempDir()

	want, err := expectedMigrationVersion()
	if err != nil {
		t.Fatalf("expected migration version: %v", err)
	}
	// Put a database one version behind the latest on disk.
	if err := os.WriteFile(filepath.Join(dataDir, "ao.db"), migratedDatabaseSnapshot(t, want-1), 0o600); err != nil {
		t.Fatalf("write stale db: %v", err)
	}

	_, err = OpenPreMigrated(dataDir)
	if err == nil {
		t.Fatal("OpenPreMigrated succeeded on stale database; want error")
	}
}

// openMigratedTestDBIn creates a fully migrated SQLite database in dataDir,
// returning the raw *sql.DB handle (caller must close it).
func openMigratedTestDBIn(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// createEmptyDatabase creates a minimal SQLite database file with no tables.
func createEmptyDatabase(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.Ping()
}
