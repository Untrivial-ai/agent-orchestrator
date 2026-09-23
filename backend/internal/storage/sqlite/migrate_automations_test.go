package sqlite

import (
	"database/sql"
	"testing"
	"time"
)

// Parallel feature branches commonly burn goose version 153 without the
// automations schema. Open must release that ledger entry and apply
// 0153_automations.sql, or daemon boot dies on automation_run_id.
func TestMigrateRepairsBurnedAutomationsVersion(t *testing.T) {
	dataDir := t.TempDir()
	db := openMigratedDatabaseCopyAt(t, dataDir, 152, pragmas)
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (153, 1)`); err != nil {
		t.Fatalf("burn version 153: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open burned automations database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	check, err := sql.Open("sqlite", databaseURI(dataDir)+pragmas)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = check.Close() })

	var automationsTable, runIDColumn int
	if err := check.QueryRow(`SELECT
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'automations'),
		(SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'automation_run_id')`).Scan(
		&automationsTable, &runIDColumn,
	); err != nil {
		t.Fatalf("inspect repaired schema: %v", err)
	}
	if automationsTable != 1 || runIDColumn != 1 {
		t.Fatalf("repaired schema automations=%d automation_run_id=%d, want both 1", automationsTable, runIDColumn)
	}
}

// Removing the run-occurrence uniqueness or the durable enum constraints must
// make this test fail: they are what let duplicate pollers and restart recovery
// converge on one logical run instead of spawning independent work.
func TestMigration0153EnforcesAutomationRunIdentity(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 153)

	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	mustExec(t, db, `
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('scheduled', '/tmp/scheduled', 'Scheduled', ?);
INSERT INTO automations (
    id, project_id, display_name, prompt, kind, rrule_text, timezone,
    enabled, next_run_at, created_at, updated_at
) VALUES (
    'auto-1', 'scheduled', 'Morning triage', 'Review new issues', 'worker',
    'FREQ=DAILY', 'UTC', 1, ?, ?, ?
);
INSERT INTO automation_runs (
    id, automation_id, scheduled_for, status, created_at, updated_at
) VALUES ('run-1', 'auto-1', ?, 'pending', ?, ?);`,
		now, now, now, now, now, now, now)

	if _, err := db.Exec(`
INSERT INTO automation_runs (
    id, automation_id, scheduled_for, status, created_at, updated_at
) VALUES ('run-duplicate', 'auto-1', ?, 'pending', ?, ?)`, now, now, now); err == nil {
		t.Fatal("duplicate scheduled occurrence was accepted")
	}
	if _, err := db.Exec(`
INSERT INTO automation_runs (
    id, automation_id, scheduled_for, status, created_at, updated_at
) VALUES ('run-invalid', 'auto-1', ?, 'unknown', ?, ?)`, now.Add(time.Minute), now, now); err == nil {
		t.Fatal("invalid automation run status was accepted")
	}
	if _, err := db.Exec(`
INSERT INTO automations (
    id, project_id, display_name, prompt, kind, rrule_text, timezone,
    enabled, next_run_at, created_at, updated_at
) VALUES (
    'auto-invalid', 'scheduled', 'Invalid', 'Prompt', 'reviewer',
    'FREQ=DAILY', 'UTC', 1, ?, ?, ?
)`, now, now, now); err == nil {
		t.Fatal("invalid automation kind was accepted")
	}
}

// Removing the unique session origin or changing its delete action must make
// this test fail: one run may create at most one session, while deleting
// automation history must never delete the user's already-created session.
func TestMigration0153LinksOneSessionAndPreservesItOnAutomationDelete(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 153)

	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	mustExec(t, db, `
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('scheduled', '/tmp/scheduled', 'Scheduled', ?);
INSERT INTO automations (
    id, project_id, display_name, prompt, kind, rrule_text, timezone,
    enabled, next_run_at, created_at, updated_at
) VALUES (
    'auto-1', 'scheduled', 'Morning triage', 'Review new issues', 'worker',
    'FREQ=DAILY', 'UTC', 1, ?, ?, ?
);
INSERT INTO automation_runs (
    id, automation_id, scheduled_for, status, created_at, updated_at
) VALUES ('run-1', 'auto-1', ?, 'spawning', ?, ?);
INSERT INTO sessions (
    id, project_id, num, activity_last_at, automation_run_id, created_at, updated_at
) VALUES ('scheduled-1', 'scheduled', 1, ?, 'run-1', ?, ?);`,
		now, now, now, now, now, now, now, now, now, now)

	if _, err := db.Exec(`
INSERT INTO sessions (
    id, project_id, num, activity_last_at, automation_run_id, created_at, updated_at
) VALUES ('scheduled-2', 'scheduled', 2, ?, 'run-1', ?, ?)`, now, now, now); err == nil {
		t.Fatal("a second session accepted the same automation run origin")
	}

	mustExec(t, db, `DELETE FROM automations WHERE id = 'auto-1'`)
	var runID sql.NullString
	if err := db.QueryRow(`SELECT automation_run_id FROM sessions WHERE id = 'scheduled-1'`).Scan(&runID); err != nil {
		t.Fatalf("read preserved session: %v", err)
	}
	if runID.Valid {
		t.Fatalf("preserved session automation_run_id = %q, want NULL", runID.String)
	}
}
