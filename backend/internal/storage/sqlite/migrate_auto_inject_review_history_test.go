package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateAcceptsUnrecordedAutoInjectReviewSchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 83)
	for _, table := range []string{"sessions", "review_run", "pr_reviews", "pr_comment"} {
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE`); err != nil {
			t.Fatalf("seed %s.auto_inject_review: %v", table, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with unrecorded auto-inject-review schema: %v", err)
	}

	for _, version := range []int64{84, 85, 86} {
		var applied int
		if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied = %d, want 1", version, applied)
		}
	}

	var agentSwitchTable, autoInjectCIColumn int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_switches'`,
	).Scan(&agentSwitchTable); err != nil {
		t.Fatalf("read agent-switch table: %v", err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'auto_inject_ci'`,
	).Scan(&autoInjectCIColumn); err != nil {
		t.Fatalf("read auto-inject-CI column: %v", err)
	}
	if agentSwitchTable != 1 || autoInjectCIColumn != 1 {
		t.Fatalf("later migrations not applied: agent_switches=%d auto_inject_ci=%d", agentSwitchTable, autoInjectCIColumn)
	}
}
