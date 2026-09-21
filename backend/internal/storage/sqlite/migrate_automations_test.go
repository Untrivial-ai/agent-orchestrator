package sqlite

import (
	"database/sql"
	"testing"
	"time"
)

// Removing the run-occurrence uniqueness or the durable enum constraints must
// make this test fail: they are what let duplicate pollers and restart recovery
// converge on one logical run instead of spawning independent work.
func TestMigration0149EnforcesAutomationRunIdentity(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 149)

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
func TestMigration0149LinksOneSessionAndPreservesItOnAutomationDelete(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 149)

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
