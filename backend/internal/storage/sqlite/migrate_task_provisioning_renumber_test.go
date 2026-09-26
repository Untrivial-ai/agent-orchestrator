package sqlite

import (
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsRenumberedTaskProvisioningHistory(t *testing.T) {
	for _, tt := range []struct {
		name              string
		baseVersion       int64
		legacyProvision   int64
		legacyPreparation int64
		legacyCreation    int64
		preapplyThrough   int64
	}{
		{name: "old full install", baseVersion: 148, legacyProvision: 149, legacyPreparation: 150, preapplyThrough: 150},
		{name: "current full install", baseVersion: 149, legacyProvision: 150, legacyPreparation: 151, preapplyThrough: 151},
		{name: "interrupted after provisioning", baseVersion: 149, legacyProvision: 150, preapplyThrough: 150},
		{name: "old preparation only", baseVersion: 149, legacyPreparation: 150, preapplyThrough: 150},
		{name: "current preparation only", baseVersion: 149, legacyPreparation: 151, preapplyThrough: 151},
		{name: "old preparation only after 0155", baseVersion: 149, legacyPreparation: 150, preapplyThrough: 155},
		{name: "current preparation only after 0155", baseVersion: 149, legacyPreparation: 151, preapplyThrough: 155},
		{name: "previous PR provisioning only", baseVersion: 154, legacyProvision: 155, preapplyThrough: 155},
		{name: "previous PR preparation", baseVersion: 154, legacyProvision: 155, legacyPreparation: 156, preapplyThrough: 156},
		{name: "previous PR full install", baseVersion: 154, legacyProvision: 155, legacyPreparation: 156, legacyCreation: 157, preapplyThrough: 157},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, tt.baseVersion)
			legacyFS := fstest.MapFS{}
			for version := tt.baseVersion + 1; version <= tt.preapplyThrough; version++ {
				var sourcePath, legacyPath string
				switch version {
				case tt.legacyProvision:
					sourcePath = "migrations/0156_session_provisioning.sql"
					legacyPath = fmt.Sprintf("migrations/%04d_session_provisioning.sql", version)
				case tt.legacyPreparation:
					sourcePath = "migrations/0157_task_preparations.sql"
					legacyPath = fmt.Sprintf("migrations/%04d_task_preparations.sql", version)
				case tt.legacyCreation:
					sourcePath = "migrations/0158_prepared_worktree_creation_sha.sql"
					legacyPath = fmt.Sprintf("migrations/%04d_prepared_worktree_creation_sha.sql", version)
				default:
					matches, err := fs.Glob(migrationsFS, fmt.Sprintf("migrations/%04d_*.sql", version))
					if err != nil || len(matches) != 1 {
						t.Fatalf("find migration %d: matches=%v err=%v", version, matches, err)
					}
					sourcePath, legacyPath = matches[0], matches[0]
				}
				contents, err := migrationsFS.ReadFile(sourcePath)
				if err != nil {
					t.Fatalf("read migration %q: %v", sourcePath, err)
				}
				legacyFS[legacyPath] = &fstest.MapFile{Data: contents}
			}

			gooseMu.Lock()
			goose.SetBaseFS(legacyFS)
			goose.SetLogger(goose.NopLogger())
			if err := goose.SetDialect("sqlite3"); err != nil {
				gooseMu.Unlock()
				t.Fatalf("set goose dialect: %v", err)
			}
			if err := goose.Up(db, "migrations"); err != nil {
				gooseMu.Unlock()
				t.Fatalf("apply historical migrations: %v", err)
			}
			gooseMu.Unlock()

			if err := migrate(db); err != nil {
				t.Fatalf("migrate historical database: %v", err)
			}
			for table, columns := range map[string][]string{
				"sessions":            {"provision_state", "provision_error", "is_task_preparation", "effort"},
				"review":              {"interface_mode"},
				"agent_model_catalog": {"metadata_json"},
				"session_worktrees":   {"creation_sha"},
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
			for version := int64(149); version <= 158; version++ {
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
			var unrealHarness int
			if err := db.QueryRow(`SELECT instr(sql, 'unreal-agent') FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&unrealHarness); err != nil || unrealHarness == 0 {
				t.Fatalf("main's 0155 harness migration missing: position=%d err=%v", unrealHarness, err)
			}

			if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at)
VALUES ('catalog-a', '/tmp/catalog-a', CURRENT_TIMESTAMP),
       ('catalog-b', '/tmp/catalog-b', CURRENT_TIMESTAMP)`); err != nil {
				t.Fatalf("create projects for catalog CDC: %v", err)
			}
			if _, err := db.Exec(`
INSERT INTO agent_model_catalog (agent_id, project_id, catalog_json, source, fetched_at)
VALUES ('codex', '', '{}', 'test', CURRENT_TIMESTAMP)`); err != nil {
				t.Fatalf("create global catalog: %v", err)
			}
			var globalEvents int
			if err := db.QueryRow(`
SELECT COUNT(*) FROM change_log
WHERE event_type = 'session_updated'
  AND json_extract(payload, '$.kind') = 'model_catalog'
  AND project_id IN ('catalog-a', 'catalog-b')`).Scan(&globalEvents); err != nil {
				t.Fatalf("read catalog CDC: %v", err)
			}
			if globalEvents != 2 {
				t.Fatalf("global catalog CDC events = %d, want one per active project", globalEvents)
			}

			if err := migrate(db); err != nil {
				t.Fatalf("second migration pass: %v", err)
			}
		})
	}
}
