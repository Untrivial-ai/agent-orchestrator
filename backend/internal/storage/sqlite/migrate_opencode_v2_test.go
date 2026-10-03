package sqlite

import (
	"strings"
	"testing"
)

func TestMigration0167AllowsOpenCodeV2AndPreservesOpenCodeV1(t *testing.T) {
	for _, legacyQM := range []bool{false, true} {
		name := "current"
		if legacyQM {
			name = "legacy_qm"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 166)
			if legacyQM {
				mustExec(t, db, `PRAGMA writable_schema = ON`)
				mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, '''mimo-code'', ''deepseek-harness''', '''mimo-code'', ''qm'', ''deepseek-harness''') WHERE type = 'table' AND name = 'sessions'`)
				mustExec(t, db, `PRAGMA writable_schema = RESET`)
			}

			var before string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('opencode-project', '/tmp/opencode-project', CURRENT_TIMESTAMP)`)
			insert := `INSERT INTO sessions (id, project_id, num, harness, created_at, updated_at, activity_last_at) VALUES (?, 'opencode-project', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
			mustExec(t, db, insert, "opencode-v1", 1, "opencode")

			upTo(t, db, 167)
			mustExec(t, db, insert, "opencode-v2", 2, "opencode-v2")
			var v1Harness string
			if err := db.QueryRow(`SELECT harness FROM sessions WHERE id = 'opencode-v1'`).Scan(&v1Harness); err != nil || v1Harness != "opencode" {
				t.Fatalf("OpenCode 1 harness after upgrade = %q, err=%v; want opencode", v1Harness, err)
			}
			if _, err := db.Exec(insert, "unknown", 3, "unknown-agent"); err == nil {
				t.Fatal("unknown harness bypassed the CHECK constraint")
			}

			mustExec(t, db, `UPDATE sessions SET harness = 'opencode' WHERE harness = 'opencode-v2'`)
			downTo(t, db, 166)
			var after string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&after); err != nil || after != before {
				t.Fatalf("down migration did not restore original schema: %v", err)
			}
			if _, err := db.Exec(insert, "opencode-v2-after-down", 4, "opencode-v2"); err == nil || !strings.Contains(err.Error(), "CHECK") {
				t.Fatalf("OpenCode 2 insertion after downgrade = %v; want CHECK failure", err)
			}
		})
	}
}
