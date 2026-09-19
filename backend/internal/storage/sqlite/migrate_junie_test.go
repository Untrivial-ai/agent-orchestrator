package sqlite

import (
	"database/sql"
	"strings"
	"testing"
)

func TestMigration0148AllowsJunieAndReversesBothHistoricalSchemas(t *testing.T) {
	fixture := migrationFixture(t, 147)
	for _, legacyQM := range []bool{false, true} {
		name := "current"
		if legacyQM {
			name = "legacy_qm"
		}
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", databaseURI(fixture(t))+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			if legacyQM {
				mustExec(t, db, `PRAGMA writable_schema = ON`)
				mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, '''omp'', ''fake''', '''omp'', ''qm'', ''fake''') WHERE type = 'table' AND name = 'sessions'`)
				mustExec(t, db, `PRAGMA writable_schema = RESET`)
			}
			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('junie-project', '/tmp/junie-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'junie-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "existing-omp", 1, "omp")
			if legacyQM {
				mustExec(t, db, insert, "existing-qm", 2, "qm")
			}
			if err := migrate(db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(insert, "junie-session", 3, "junie"); err != nil {
				t.Fatalf("insert junie session after migration: %v", err)
			}
			var version int
			if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil || version != 148 {
				t.Fatalf("migration version = %d, err = %v; want 148", version, err)
			}
			if _, err := db.Exec(insert, "unknown", 4, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}
			// Clear the new harness value before downgrading to the older contract.
			mustExec(t, db, `UPDATE sessions SET harness = '' WHERE harness = 'junie'`)
			downTo(t, db, 147)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "junie-after-down", 4, "junie"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("junie insertion after downgrade = %v; want CHECK failure", err)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity after downgrade = %q, %v", integrity, err)
			}
		})
	}
}

func TestMigrateRepairsJunieForHistoricalHarnessSchemas(t *testing.T) {
	fixture := migrationFixture(t, 148)
	for _, historical := range []string{sessionsHarnessCheckWithMuse, sessionsHarnessCheckWithMuseQMLegacyPrimeAgent} {
		t.Run(historical, func(t *testing.T) {
			db, err := sql.Open("sqlite", databaseURI(fixture(t))+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			var schema string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			start := strings.Index(schema, "CHECK (harness IN (")
			end := start + strings.Index(schema[start:], "))") + 2
			mustExec(t, db, `PRAGMA writable_schema = ON`)
			mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, ?, ?) WHERE name = 'sessions'`, schema[start:end], historical)
			mustExec(t, db, `PRAGMA writable_schema = RESET`)
			if err := migrate(db); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('junie-repair', '/tmp/junie-repair', CURRENT_TIMESTAMP)`)
			mustExec(t, db, `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES ('junie-repair-1', 'junie-repair', 1, 'junie', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
		})
	}
}
