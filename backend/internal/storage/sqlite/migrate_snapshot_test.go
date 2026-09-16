package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var (
	migrationSnapshotMu sync.Mutex
	migrationSnapshots  = map[int64][]byte{}
)

// migratedDatabaseSnapshot builds shared migration checkpoints cumulatively.
// Migration compatibility tests can then exercise isolated database copies
// without rebuilding the complete historical schema for every scenario.
func migratedDatabaseSnapshot(t *testing.T, version int64) []byte {
	t.Helper()
	migrationSnapshotMu.Lock()
	defer migrationSnapshotMu.Unlock()
	return append([]byte(nil), migratedDatabaseSnapshotLocked(t, version)...)
}

func migratedDatabaseSnapshotLocked(t *testing.T, version int64) []byte {
	t.Helper()
	if snapshot, ok := migrationSnapshots[version]; ok {
		return snapshot
	}

	var baseVersion int64
	switch {
	case version >= 141:
		baseVersion = 128
	case version >= 128:
		baseVersion = 109
	case version >= 109:
		baseVersion = 108
	}
	var base []byte
	if baseVersion > 0 {
		base = migratedDatabaseSnapshotLocked(t, baseVersion)
	}

	databasePath := filepath.Join(t.TempDir(), "ao.db")
	if len(base) > 0 {
		if err := os.WriteFile(databasePath, base, 0o600); err != nil {
			t.Fatalf("copy migration %d checkpoint: %v", baseVersion, err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open migration %d checkpoint: %v", version, err)
	}
	upTo(t, db, version)
	if err := db.Close(); err != nil {
		t.Fatalf("close migration %d checkpoint: %v", version, err)
	}
	snapshot, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read migration %d checkpoint: %v", version, err)
	}
	migrationSnapshots[version] = snapshot
	return snapshot
}

func openMigratedDatabaseCopy(t *testing.T, version int64) *sql.DB {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "ao.db")
	if err := os.WriteFile(databasePath, migratedDatabaseSnapshot(t, version), 0o600); err != nil {
		t.Fatalf("copy migration %d checkpoint: %v", version, err)
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+pragmas)
	if err != nil {
		t.Fatalf("open migration %d copy: %v", version, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
