package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsRenumberedUsageCostHistory(t *testing.T) {
	tests := []struct {
		name          string
		firstVersion  int64
		legacyApplied int
	}{
		{name: "only cost columns applied at 0110", firstVersion: 110, legacyApplied: 1},
		{name: "all usage migrations applied at 0110 through 0113", firstVersion: 110, legacyApplied: 4},
		{name: "all usage migrations applied at 0109 through 0112", firstVersion: 109, legacyApplied: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			upTo(t, db, tt.firstVersion-1)
			applyLegacyUsageCostMigrations(t, db, tt.firstVersion, tt.legacyApplied)

			if err := migrate(db); err != nil {
				t.Fatalf("migrate renumbered usage-cost database: %v", err)
			}

			for version := int64(109); version <= 116; version++ {
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

			for table, columns := range map[string][]string{
				"conversation_branches": {"strategy", "replay_cutoff_sequence", "replay_truncated", "provider_scope_id"},
				"app_settings":          {"cloud_offering"},
				"sessions":              {"latest_user_prompt_at"},
				"usage_bindings":        {"provider_hint"},
				"model_usage_events": {
					"billing_provider_id", "usage_measurement_kind", "provider_usage_json",
					"billing_provider_source",
				},
			} {
				for _, column := range columns {
					var present int
					if err := db.QueryRow(
						`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
					).Scan(&present); err != nil {
						t.Fatalf("read %s.%s: %v", table, column, err)
					}
					if present != 1 {
						t.Fatalf("%s.%s count = %d, want 1", table, column, present)
					}
				}
			}

			var triggerSQL string
			if err := db.QueryRow(
				`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'pr_cdc_update'`,
			).Scan(&triggerSQL); err != nil {
				t.Fatalf("read pr_cdc_update: %v", err)
			}
			if !strings.Contains(triggerSQL, "auto_inject_ci") {
				t.Fatalf("pr_cdc_update was not upgraded: %s", triggerSQL)
			}
			if err := migrate(db); err != nil {
				t.Fatalf("second migration pass: %v", err)
			}
		})
	}
}

func applyLegacyUsageCostMigrations(t *testing.T, db *sql.DB, firstVersion int64, count int) {
	t.Helper()
	canonicalPaths := []string{
		"migrations/0113_usage_cost_estimation.sql",
		"migrations/0114_usage_cost_candidate_canonical_index.sql",
		"migrations/0115_usage_measurement_and_provider_usage.sql",
		"migrations/0116_usage_billing_provider_source.sql",
	}
	legacySuffixes := []string{
		"usage_cost_estimation.sql",
		"usage_cost_candidate_canonical_index.sql",
		"usage_measurement_and_provider_usage.sql",
		"usage_billing_provider_source.sql",
	}
	legacyFS := fstest.MapFS{}
	for i := 0; i < count; i++ {
		contents, err := migrationsFS.ReadFile(canonicalPaths[i])
		if err != nil {
			t.Fatalf("read canonical usage migration %q: %v", canonicalPaths[i], err)
		}
		legacyPath := fmt.Sprintf("migrations/%04d_%s", firstVersion+int64(i), legacySuffixes[i])
		legacyFS[legacyPath] = &fstest.MapFile{Data: contents}
	}

	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(legacyFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatalf("apply %d legacy usage migrations: %v", count, err)
	}
}
