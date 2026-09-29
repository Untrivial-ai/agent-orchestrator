package sqlite

import (
	"strings"
	"testing"
)

func TestMigration0163AllowsFXAndReversesBothHistoricalSchemas(t *testing.T) {
	for _, legacyQM := range []bool{false, true} {
		name := "current"
		if legacyQM {
			name = "legacy_qm"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 162)
			if legacyQM {
				mustExec(t, db, `PRAGMA writable_schema = ON`)
				mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, '''unreal-agent'', ''fake''', '''unreal-agent'', ''qm'', ''fake''') WHERE type = 'table' AND name = 'sessions'`)
				mustExec(t, db, `PRAGMA writable_schema = RESET`)
			}
			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('fx-project', '/tmp/fx-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'fx-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "existing-omp", 1, "omp")
			if legacyQM {
				mustExec(t, db, insert, "existing-qm", 2, "qm")
			}
			upTo(t, db, 163)
			if _, err := db.Exec(insert, "fx-session", 3, "fx"); err != nil {
				t.Fatalf("insert fx session after migration: %v", err)
			}
			var version int
			if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil || version != 163 {
				t.Fatalf("migration version = %d, err = %v; want 163", version, err)
			}
			if _, err := db.Exec(insert, "unknown", 4, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}
			// Clear the new harness value before downgrading to the older contract.
			mustExec(t, db, `UPDATE sessions SET harness = '' WHERE harness = 'fx'`)
			downTo(t, db, 162)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "fx-after-down", 4, "fx"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("fx insertion after downgrade = %v; want CHECK failure", err)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity after downgrade = %q, %v", integrity, err)
			}
		})
	}
}
