package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateStampsSchemaAppVersion(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	latest, err := latestMigrationVersion()
	if err != nil {
		t.Fatalf("latest migration: %v", err)
	}
	var version int64
	if err := db.QueryRow(`SELECT version FROM schema_app_version WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read schema app version: %v", err)
	}
	if version != latest {
		t.Fatalf("schema app version = %d, want latest migration %d", version, latest)
	}
}

func TestMigrateRefusesNewerSchemaAppVersion(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	latest, err := latestMigrationVersion()
	if err != nil {
		t.Fatalf("latest migration: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE schema_app_version (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO schema_app_version (id, version) VALUES (1, ?);
`, latest+1); err != nil {
		t.Fatalf("seed newer schema version: %v", err)
	}

	err = migrate(db)
	if err == nil || !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("migrate error = %v, want newer-schema refusal", err)
	}
}

func TestOpenReadOnlyRefusesNewerSchemaAppVersion(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	latest, err := latestMigrationVersion()
	if err != nil {
		t.Fatalf("latest migration: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE schema_app_version (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO schema_app_version (id, version) VALUES (1, ?);
`, latest+1); err != nil {
		_ = db.Close()
		t.Fatalf("seed newer schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	_, err = OpenReadOnly(context.Background(), dataDir)
	if err == nil || !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("OpenReadOnly error = %v, want newer-schema refusal", err)
	}
}
