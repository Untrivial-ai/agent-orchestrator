package sqlite

import (
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsRenumberedCodexAccountSwitchHistory(t *testing.T) {
	for _, tt := range []struct {
		name              string
		includeSourceKind bool
		includeCleanup    bool
		baseVersion       int64
		restartVersion    int64
	}{
		{name: "restart_policy_only", baseVersion: 128, restartVersion: 129},
		{name: "restart_policy_and_source_kind", includeSourceKind: true, baseVersion: 128, restartVersion: 129},
		{name: "second_numbering_restart_only", baseVersion: 139, restartVersion: 140},
		{name: "second_numbering_source_kind", includeSourceKind: true, baseVersion: 139, restartVersion: 140},
		{name: "second_numbering_cleanup", includeSourceKind: true, includeCleanup: true, baseVersion: 139, restartVersion: 140},
		{name: "main_already_applied", includeSourceKind: true, baseVersion: 145, restartVersion: 146},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			db, err := sql.Open("sqlite", databaseURI(dataDir)+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			upTo(t, db, tt.baseVersion)

			restartMigration, err := migrationsFS.ReadFile("migrations/0146_codex_account_switch_restart_policy.sql")
			if err != nil {
				t.Fatal(err)
			}
			legacy := fstest.MapFS{
				fmt.Sprintf("migrations/%04d_codex_account_switch_restart_policy.sql", tt.restartVersion): &fstest.MapFile{Data: restartMigration},
			}
			if tt.includeSourceKind {
				sourceMigration, readErr := migrationsFS.ReadFile("migrations/0147_codex_switch_source_kind.sql")
				if readErr != nil {
					t.Fatal(readErr)
				}
				legacy[fmt.Sprintf("migrations/%04d_codex_switch_source_kind.sql", tt.restartVersion+1)] = &fstest.MapFile{Data: sourceMigration}
			}
			if tt.includeCleanup {
				cleanupMigration, readErr := migrationsFS.ReadFile("migrations/0148_codex_account_switch_cleanup.sql")
				if readErr != nil {
					t.Fatal(readErr)
				}
				legacy[fmt.Sprintf("migrations/%04d_codex_account_switch_cleanup.sql", tt.restartVersion+2)] = &fstest.MapFile{Data: cleanupMigration}
			}

			gooseMu.Lock()
			goose.SetBaseFS(legacy)
			goose.SetLogger(goose.NopLogger())
			if err := goose.SetDialect("sqlite3"); err != nil {
				gooseMu.Unlock()
				t.Fatal(err)
			}
			if err := goose.Up(db, "migrations"); err != nil {
				gooseMu.Unlock()
				t.Fatalf("apply legacy Codex migrations: %v", err)
			}
			gooseMu.Unlock()

			if err := migrate(db); err != nil {
				t.Fatalf("migrate legacy Codex database: %v", err)
			}
			if err := migrate(db); err != nil {
				t.Fatalf("reopen migrated Codex database: %v", err)
			}

			for _, column := range []string{"source_kind"} {
				var count int
				if err := db.QueryRow(
					`SELECT COUNT(*) FROM pragma_table_info('codex_account_switches') WHERE name = ?`, column,
				).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("column %s count = %d, want 1", column, count)
				}
			}
			var removedRestartColumn int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info('codex_account_switches') WHERE name = 'restart_running_sessions'`,
			).Scan(&removedRestartColumn); err != nil {
				t.Fatal(err)
			}
			if removedRestartColumn != 0 {
				t.Fatalf("restart_running_sessions count = %d, want 0", removedRestartColumn)
			}
			for _, version := range []int64{129, 130, 140, 141, 142, 146, 147, 148} {
				var applied int
				if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if applied != 1 {
					t.Fatalf("migration %d applied = %d, want 1", version, applied)
				}
			}

			var retentionIndex int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_change_log_created_at_seq'`,
			).Scan(&retentionIndex); err != nil {
				t.Fatal(err)
			}
			if retentionIndex != 1 {
				t.Fatalf("retention index count = %d, want 1", retentionIndex)
			}
			var reviewPartial int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info('pr') WHERE name = 'review_partial'`,
			).Scan(&reviewPartial); err != nil {
				t.Fatal(err)
			}
			if reviewPartial != 1 {
				t.Fatalf("review_partial count = %d, want 1", reviewPartial)
			}
			var nullableProject, checkpointState, checkpointUnsettled int
			if err := db.QueryRow(`
SELECT (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'project_id' AND "notnull" = 0),
       (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'conversation_checkpoint_state'),
       (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'conversation_checkpoint_unsettled')`).Scan(&nullableProject, &checkpointState, &checkpointUnsettled); err != nil {
				t.Fatal(err)
			}
			if nullableProject != 1 || checkpointState != 1 || checkpointUnsettled != 1 {
				t.Fatalf("collided main schema missing: nullable project=%d, checkpoint=%d, unsettled=%d", nullableProject, checkpointState, checkpointUnsettled)
			}
		})
	}
}
