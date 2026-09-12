package sqlite

import (
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsRenumberedCodexAccountSwitchHistory(t *testing.T) {
	for _, tt := range []struct {
		name              string
		includeSourceKind bool
	}{
		{name: "restart_policy_only"},
		{name: "restart_policy_and_source_kind", includeSourceKind: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			db, err := sql.Open("sqlite", databaseURI(dataDir)+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			upTo(t, db, 128)

			restartMigration, err := migrationsFS.ReadFile("migrations/0140_codex_account_switch_restart_policy.sql")
			if err != nil {
				t.Fatal(err)
			}
			legacy := fstest.MapFS{
				"migrations/0129_codex_account_switch_restart_policy.sql": &fstest.MapFile{Data: restartMigration},
			}
			if tt.includeSourceKind {
				sourceMigration, readErr := migrationsFS.ReadFile("migrations/0141_codex_switch_source_kind.sql")
				if readErr != nil {
					t.Fatal(readErr)
				}
				legacy["migrations/0130_codex_switch_source_kind.sql"] = &fstest.MapFile{Data: sourceMigration}
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

			for _, column := range []string{"restart_running_sessions", "source_kind"} {
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
			for _, version := range []int64{129, 130, 140, 141} {
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
		})
	}
}
