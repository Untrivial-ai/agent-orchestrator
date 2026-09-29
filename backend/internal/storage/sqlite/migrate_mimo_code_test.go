package sqlite

import (
	"strings"
	"testing"
)

func TestMigration0165AllowsMiMoCodeAndPreservesFXAndGemini(t *testing.T) {
	for _, legacyQM := range []bool{false, true} {
		name := "current"
		if legacyQM {
			name = "legacy_qm"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 164)
			if legacyQM {
				mustExec(t, db, `PRAGMA writable_schema = ON`)
				mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, '''fx'', ''fake''', '''fx'', ''qm'', ''fake''') WHERE type = 'table' AND name = 'sessions'`)
				mustExec(t, db, `PRAGMA writable_schema = RESET`)
			}
			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			upTo(t, db, 165)
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('mimo-project', '/tmp/mimo-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'mimo-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "fx-session", 1, "fx")
			mustExec(t, db, insert, "mimo-session", 2, "mimo-code")
			mustExec(t, db, insert, "gemini-session", 5, "gemini")
			if legacyQM {
				mustExec(t, db, insert, "qm-session", 3, "qm")
			}
			if _, err := db.Exec(insert, "unknown", 4, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}
			mustExec(t, db, `UPDATE sessions SET harness = '' WHERE harness = 'mimo-code'`)
			downTo(t, db, 164)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "mimo-after-down", 4, "mimo-code"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("MiMo insertion after downgrade = %v; want CHECK failure", err)
			}
		})
	}
}
