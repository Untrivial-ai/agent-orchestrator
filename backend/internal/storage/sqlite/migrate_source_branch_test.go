package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestMigratePreviewSourceBranchPreservesMainMigrations(t *testing.T) {
	for _, previewVersion := range []int64{126, 129, 140} {
		t.Run(fmt.Sprint(previewVersion), func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			upTo(t, db, previewVersion-1)
			ledgerVersion := previewVersion
			if previewVersion == 140 {
				// Main's retention index already exists; keep its version 129 ledger.
				ledgerVersion = 129
			}
			if _, err := db.Exec(`
 ALTER TABLE sessions ADD COLUMN source_branch TEXT NOT NULL DEFAULT '';
 CREATE INDEX idx_sessions_source_branch ON sessions(source_branch) WHERE source_branch <> '';
 INSERT INTO goose_db_version(version_id,is_applied) VALUES(?,1);
 INSERT INTO projects(id,path,registered_at,config) VALUES('preview','/preview',CURRENT_TIMESTAMP,'{}');
 INSERT INTO sessions(id,project_id,num,activity_last_at,created_at,updated_at,source_branch) VALUES('preview-1','preview',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'feature/keep');
 `, ledgerVersion); err != nil {
				t.Fatal(err)
			}
			// Later preview builds already ran canonical repository identity before
			// registering this project, so reproduce the project service's field.
			if previewVersion > 126 {
				if _, err := db.Exec(`UPDATE projects SET config=json_set(config,'$.canonicalRepoURL','') WHERE id='preview'`); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := migrate(db); err != nil {
					t.Fatalf("migration attempt %d: %v", i, err)
				}
				var branch string
				if err := db.QueryRow(`SELECT source_branch FROM sessions WHERE id='preview-1'`).Scan(&branch); err != nil || branch != "feature/keep" {
					t.Fatalf("saved source branch lost: %q %v", branch, err)
				}
				var canonical string
				if err := db.QueryRow(`SELECT json_extract(config,'$.canonicalRepoURL') FROM projects WHERE id='preview'`).Scan(&canonical); err != nil {
					t.Fatalf("main migration 126 missing: %v", err)
				}
				var columns int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name IN ('source_branch','session_permissions')`).Scan(&columns); err != nil {
					t.Fatal(err)
				}
				if columns != 2 {
					t.Fatalf("source branch and permissions columns=%d", columns)
				}
				var retentionIndex int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_change_log_created_at_seq'`).Scan(&retentionIndex); err != nil || retentionIndex != 1 {
					t.Fatalf("main retention index count=%d: %v", retentionIndex, err)
				}
				var versions int
				if err := db.QueryRow(`SELECT COUNT(DISTINCT version_id) FROM goose_db_version WHERE version_id BETWEEN 126 AND 140 AND is_applied=1`).Scan(&versions); err != nil {
					t.Fatal(err)
				}
				if versions != 15 {
					t.Fatalf("applied migration count=%d", versions)
				}
			}
		})
	}
}
