package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// migrationSnapshots caches a database image for every goose version the
// package's tests start from. Under -race a single full migration replay costs
// ~12s, and the suite has ~80 tests that each rebuilt their starting schema
// from scratch, so the package spent ~15 minutes replaying migrations. Images
// are built once, in ascending order, one migration at a time: the package now
// pays for one replay no matter how many versions the tests ask for.
var migrationSnapshots struct {
	sync.Mutex
	images  map[int64][]byte
	highest int64
}

// snapshotDSN keeps the checkpoint databases in rollback-journal mode so ao.db
// is complete on disk after every commit. WAL would leave committed frames in a
// sidecar file that a plain file read silently misses.
const snapshotDSN = "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

// noForeignKeysDSN is for tests that seed rows whose parents do not exist yet.
const noForeignKeysDSN = "?_pragma=busy_timeout(5000)"

// migratedDatabaseSnapshot returns a copy of a database migrated to exactly
// version. Versions below the highest one built are served from the cache, so
// requests may arrive in any order.
func migratedDatabaseSnapshot(t *testing.T, version int64) []byte {
	t.Helper()
	migrationSnapshots.Lock()
	defer migrationSnapshots.Unlock()
	if snapshot, ok := migrationSnapshots.images[version]; ok {
		return append([]byte(nil), snapshot...)
	}
	return append([]byte(nil), buildMigrationSnapshots(t, version)...)
}

func buildMigrationSnapshots(t *testing.T, version int64) []byte {
	t.Helper()
	if migrationSnapshots.images == nil {
		migrationSnapshots.images = map[int64][]byte{}
	}
	databasePath := filepath.Join(t.TempDir(), "ao.db")
	if migrationSnapshots.highest > 0 {
		if err := os.WriteFile(databasePath, migrationSnapshots.images[migrationSnapshots.highest], 0o600); err != nil {
			t.Fatalf("resume migration checkpoint %d: %v", migrationSnapshots.highest, err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+snapshotDSN)
	if err != nil {
		t.Fatalf("open migration checkpoint: %v", err)
	}
	defer func() { _ = db.Close() }()
	for next := migrationSnapshots.highest + 1; next <= version; next++ {
		upTo(t, db, next)
		snapshot, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatalf("read migration checkpoint %d: %v", next, err)
		}
		migrationSnapshots.images[next] = snapshot
		migrationSnapshots.highest = next
	}
	return migrationSnapshots.images[version]
}

// openMigratedDatabaseCopy returns an isolated database migrated to exactly
// version, opened with the package's production pragmas. Tests that exercise
// migration behaviour seed their legacy data here and then call migrate().
func openMigratedDatabaseCopy(t *testing.T, version int64) *sql.DB {
	t.Helper()
	return openMigratedDatabaseCopyAt(t, t.TempDir(), version, pragmas)
}

// openMigratedDatabaseCopyNoForeignKeys is for tests that seed rows whose
// parents do not exist yet.
func openMigratedDatabaseCopyNoForeignKeys(t *testing.T, version int64) *sql.DB {
	t.Helper()
	return openMigratedDatabaseCopyAt(t, t.TempDir(), version, noForeignKeysDSN)
}

// openMigratedDatabaseCopyAt clones the snapshot for version into dataDir. Use
// it when the test later hands that directory to the production Open path.
func openMigratedDatabaseCopyAt(t *testing.T, dataDir string, version int64, dsn string) *sql.DB {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dataDir, "ao.db"), migratedDatabaseSnapshot(t, version), 0o600); err != nil {
		t.Fatalf("copy migration %d checkpoint: %v", version, err)
	}
	db, err := sql.Open("sqlite", databaseURI(dataDir)+dsn)
	if err != nil {
		t.Fatalf("open migration %d copy: %v", version, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrationSnapshotsMatchTheRequestedVersion guards the cache: a snapshot
// must record exactly the requested version, must be independent of the images
// it was built from, and must carry the schema that version really has.
func TestMigrationSnapshotsMatchTheRequestedVersion(t *testing.T) {
	latest, err := expectedMigrationVersion()
	if err != nil {
		t.Fatalf("expected migration version: %v", err)
	}

	// Deliberately request a high version first so the lower ones are served
	// from the cache rather than rebuilt.
	for _, version := range []int64{latest, 12, 43, 103, latest} {
		db := openMigratedDatabaseCopy(t, version)
		var got int64
		if err := db.QueryRow(
			`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1`,
		).Scan(&got); err != nil {
			t.Fatalf("read applied version at %d: %v", version, err)
		}
		if got != version {
			t.Fatalf("snapshot %d reports applied version %d", version, got)
		}
	}

	// A clone must not share schema, data, or ledger state with the snapshot it
	// came from or with a sibling clone.
	first := openMigratedDatabaseCopy(t, 103)
	if _, err := first.Exec(`
INSERT INTO projects (id, path, registered_at) VALUES ('clone-only', '/clone-only', CURRENT_TIMESTAMP);
CREATE TABLE clone_only (id INTEGER);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1);
`); err != nil {
		t.Fatalf("seed clone: %v", err)
	}
	second := openMigratedDatabaseCopy(t, 103)
	var leakedRows, leakedTables, leakedVersions int
	if err := second.QueryRow(`SELECT
		(SELECT COUNT(*) FROM projects WHERE id = 'clone-only'),
		(SELECT COUNT(*) FROM sqlite_master WHERE name = 'clone_only'),
		(SELECT COUNT(*) FROM goose_db_version WHERE version_id = 9999)`).Scan(&leakedRows, &leakedTables, &leakedVersions); err != nil {
		t.Fatalf("read second clone: %v", err)
	}
	if leakedRows != 0 || leakedTables != 0 || leakedVersions != 0 {
		t.Fatalf("clone leaked rows/tables/versions = %d/%d/%d into a sibling clone", leakedRows, leakedTables, leakedVersions)
	}
}
