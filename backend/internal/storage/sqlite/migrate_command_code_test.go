package sqlite

import (
	"strings"
	"testing"
)

// TestMigration0169AllowsCommandCodeAndReversesBothHistoricalSchemas mirrors the
// fx and DeepSeek coverage: the new harness must be insertable on a current and a
// legacy-qm schema, an unknown harness must still be rejected, and the down
// migration must restore the exact prior constraint.
func TestMigration0169AllowsCommandCodeAndReversesBothHistoricalSchemas(t *testing.T) {
	for _, legacyQM := range []bool{false, true} {
		name := "current"
		if legacyQM {
			name = "legacy_qm"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 168)
			if legacyQM {
				// The retained legacy 'qm' fixture harness sits before
				// 'deepseek-harness' in every variant 0169 rewrites, so anchor
				// there to produce a schema the migration still recognizes.
				mustExec(t, db, `PRAGMA writable_schema = ON`)
				mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, '''mimo-code'', ''deepseek-harness''', '''mimo-code'', ''qm'', ''deepseek-harness''') WHERE type = 'table' AND name = 'sessions'`)
				mustExec(t, db, `PRAGMA writable_schema = RESET`)
			}
			upTo(t, db, 168)
			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('cc-project', '/tmp/cc-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'cc-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "existing-omp", 1, "omp")
			if legacyQM {
				mustExec(t, db, insert, "existing-qm", 2, "qm")
			}
			upTo(t, db, 169)
			if _, err := db.Exec(insert, "cc-session", 3, "command-code"); err != nil {
				t.Fatalf("insert command-code session after migration: %v", err)
			}
			var version int
			if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil || version != 169 {
				t.Fatalf("migration version = %d, err = %v; want 169", version, err)
			}
			if _, err := db.Exec(insert, "unknown", 4, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}
			// Clear the new harness value before downgrading to the older contract.
			mustExec(t, db, `UPDATE sessions SET harness = '' WHERE harness = 'command-code'`)
			downTo(t, db, 168)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "cc-after-down", 5, "command-code"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("command-code insertion after downgrade = %v; want CHECK failure", err)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity after downgrade = %q, %v", integrity, err)
			}
		})
	}
}

// TestCommandCodeRepairAnchorMatchesEveryHistoricalList guards the anchor the
// schema repair relies on for this harness: whatever schema a database arrives
// from, the sessions harness CHECK must end with the retained 'fake' fixture
// harness, otherwise the repair would silently skip it and a database that
// missed an earlier harness migration could never accept this harness.
func TestCommandCodeRepairAnchorMatchesEveryHistoricalHarnessList(t *testing.T) {
	for _, version := range []int64{26, 53, 54, 82, 95, 155, 163, 164, 165, 166, 167, 168} {
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
			t.Fatalf("schema at %d ends its harness CHECK with %q; the Command Code repair anchor expects 'fake'))", version, check)
		}
		if !strings.Contains(check, `'fake'`) {
			t.Fatalf("schema at %d has no fake harness entry: %q", version, check)
		}
	}
}

// TestReconcileCommandCodeAfterBurnedMigration covers a database that never ran
// an earlier harness migration: goose skips the burned number, the current-schema
// rewrite in 0169 matches nothing, and the repair is the only thing that adds
// Command Code. It must also leave every earlier harness intact, which is the
// failure mode when a migration widens a schema variant the repair also relies on.
func TestReconcileCommandCodeAfterBurnedMigration(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 164)
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (165, 1)`); err != nil {
		t.Fatalf("seed burned MiMo migration: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate burned profile: %v", err)
	}
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"'fx'", "'gemini'", "'unreal-agent'", "'mimo-code'", "'deepseek-harness'", "'command-code'"} {
		if !strings.Contains(schema, harness) {
			t.Fatalf("repaired sessions constraint lost %s: %s", harness, schema)
		}
	}
	if err := reconcileHarnessConstraint(db); err != nil {
		t.Fatalf("repeat repair: %v", err)
	}
}
