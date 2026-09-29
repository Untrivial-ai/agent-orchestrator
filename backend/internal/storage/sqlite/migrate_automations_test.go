package sqlite

import (
	"database/sql"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"
)

// Main owns 159/160, while older PR builds may have applied automations at
// 159. Every history must end with 159-162 applied, automations present, and
// the PR discussion columns dropped again by 162.
func TestMigrateRepairsInterleavedAutomationsHistory(t *testing.T) {
	for _, tt := range []struct {
		name      string
		base      int64
		legacy159 bool
		burned159 bool
		burned161 bool
		lost159   bool
	}{
		{name: "main 159 and 160 already applied", base: 160},
		{name: "main 159 marker lost with column present", base: 160, lost159: true},
		{name: "old automations at 159", base: 158, legacy159: true},
		{name: "burned 159 without either schema", base: 158, burned159: true},
		{name: "burned 161 without automations schema", base: 160, burned161: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			db := openMigratedDatabaseCopyAt(t, dataDir, tt.base, pragmas)
			if tt.legacy159 {
				contents, err := migrationsFS.ReadFile("migrations/0161_automations.sql")
				if err != nil {
					t.Fatalf("read automations migration: %v", err)
				}
				gooseMu.Lock()
				goose.SetBaseFS(fstest.MapFS{
					"migrations/0159_automations.sql": &fstest.MapFile{Data: contents},
				})
				err = goose.Up(db, "migrations")
				goose.SetBaseFS(migrationsFS)
				gooseMu.Unlock()
				if err != nil {
					t.Fatalf("apply old automations migration: %v", err)
				}
			}
			if tt.burned159 {
				if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (159, 1)`); err != nil {
					t.Fatalf("burn version 159: %v", err)
				}
			}
			if tt.burned161 {
				if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (161, 1)`); err != nil {
					t.Fatalf("burn version 161: %v", err)
				}
			}
			if tt.lost159 {
				if _, err := db.Exec(`DELETE FROM goose_db_version WHERE version_id = 159`); err != nil {
					t.Fatalf("remove version 159 marker: %v", err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := Open(dataDir)
			if err != nil {
				t.Fatalf("open upgraded database: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			// A second startup must not read 162's dropped columns as a lost 159.
			store, err = Open(dataDir)
			if err != nil {
				t.Fatalf("reopen upgraded database: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			check, err := sql.Open("sqlite", databaseURI(dataDir)+pragmas)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			t.Cleanup(func() { _ = check.Close() })

			for _, column := range []struct {
				table, name string
				want        int
			}{
				{"pr", "discussion_comment_count", 0},
				{"pr", "discussion_commenters_json", 0},
				{"sessions", "automation_run_id", 1},
			} {
				var present int
				if err := check.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, column.table, column.name).Scan(&present); err != nil {
					t.Fatalf("inspect %s.%s: %v", column.table, column.name, err)
				}
				if present != column.want {
					t.Errorf("%s.%s count = %d, want %d", column.table, column.name, present, column.want)
				}
			}
			for _, table := range []string{"automations", "automation_runs"} {
				var present int
				if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&present); err != nil {
					t.Fatalf("inspect %s: %v", table, err)
				}
				if present != 1 {
					t.Errorf("%s table count = %d, want 1", table, present)
				}
			}
			for _, version := range []int{159, 160, 161, 162} {
				var applied int
				if err := check.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1`, version).Scan(&applied); err != nil {
					t.Fatalf("inspect migration %d: %v", version, err)
				}
				if applied != 1 {
					t.Errorf("migration %d applied rows = %d, want 1", version, applied)
				}
			}
		})
	}
}

// Removing the run-occurrence uniqueness or the durable enum constraints must
// make this test fail: they are what let duplicate pollers and restart recovery
// converge on one logical run instead of spawning independent work.
func TestMigration0161EnforcesAutomationRunIdentity(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 161)

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
func TestMigration0161LinksOneSessionAndPreservesItOnAutomationDelete(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 161)

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
