package sqlite

import (
	"strings"
	"testing"
)

// TestMigration0166AllowsDeepSeekAndReversesBothHistoricalSchemas mirrors the fx
// coverage: the new harness must be insertable on a current and a legacy-qm
// schema, an unknown harness must still be rejected, and the down migration must
// restore the exact prior constraint.
func TestMigration0165AllowsDeepSeekAndReversesBothHistoricalSchemas(t *testing.T) {
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
			// Land on the exact schema 0166 upgrades, so the down migration can be
			// compared against the constraint it started from.
			upTo(t, db, 165)
			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('deepseek-project', '/tmp/deepseek-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'deepseek-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "existing-omp", 1, "omp")
			if legacyQM {
				mustExec(t, db, insert, "existing-qm", 2, "qm")
			}
			upTo(t, db, 166)
			if _, err := db.Exec(insert, "deepseek-session", 3, "deepseek-harness"); err != nil {
				t.Fatalf("insert deepseek session after migration: %v", err)
			}
			var version int
			if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil || version != 166 {
				t.Fatalf("migration version = %d, err = %v; want 166", version, err)
			}
			if _, err := db.Exec(insert, "unknown", 4, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}
			// Clear the new harness value before downgrading to the older contract.
			mustExec(t, db, `UPDATE sessions SET harness = '' WHERE harness = 'deepseek-harness'`)
			downTo(t, db, 165)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "deepseek-after-down", 5, "deepseek-harness"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("deepseek-harness insertion after downgrade = %v; want CHECK failure", err)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity after downgrade = %q, %v", integrity, err)
			}
		})
	}
}

// TestDeepSeekRepairAnchorMatchesEveryHistoricalList guards the anchor the
// schema repair relies on for this harness: whatever schema a database arrives
// from, the sessions harness CHECK must end with the retained 'fake' fixture
// harness, otherwise the repair would silently skip it and a database that
// missed an earlier harness migration could never accept this harness.
func TestMigration0165WidensEveryHistoricalHarnessList(t *testing.T) {
	for _, version := range []int64{26, 53, 54, 82, 95, 155, 163, 164, 165} {
		db := openMigratedDatabaseCopy(t, version)
		var sql string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		start := strings.Index(sql, "CHECK (harness IN")
		if start < 0 {
			t.Fatalf("schema at %d has no harness CHECK", version)
		}
		end := strings.Index(sql[start:], "))")
		if end < 0 {
			t.Fatalf("schema at %d has an unterminated harness CHECK", version)
		}
		check := sql[start : start+end+2]
		if !strings.HasSuffix(check, `'fake'))`) {
			t.Fatalf("schema at %d ends its harness CHECK with %q; the DeepSeek repair anchor expects 'fake'))", version, check)
		}
		if !strings.Contains(check, `'fake'`) {
			t.Fatalf("schema at %d has no fake harness entry: %q", version, check)
		}
	}
}
