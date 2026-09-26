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
			if _, err := db.Exec(`
 ALTER TABLE sessions ADD COLUMN source_branch TEXT NOT NULL DEFAULT '';
 CREATE INDEX idx_sessions_source_branch ON sessions(source_branch) WHERE source_branch <> '';
 INSERT INTO goose_db_version(version_id,is_applied) VALUES(?,1);
 INSERT INTO projects(id,path,registered_at,config) VALUES('preview','/preview',CURRENT_TIMESTAMP,'{}');
 INSERT INTO sessions(id,project_id,num,activity_last_at,created_at,updated_at,source_branch) VALUES('preview-1','preview',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'feature/keep');
 `, previewVersion); err != nil {
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
				var projectIDNotNull int
				if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('sessions') WHERE name='project_id'`).Scan(&projectIDNotNull); err != nil || projectIDNotNull != 0 {
					t.Fatalf("standalone migration did not make project_id nullable: notnull=%d err=%v", projectIDNotNull, err)
				}
				var retentionIndex int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_change_log_created_at_seq'`).Scan(&retentionIndex); err != nil || retentionIndex != 1 {
					t.Fatalf("main retention index count=%d: %v", retentionIndex, err)
				}
				var versions int
				if err := db.QueryRow(`SELECT COUNT(DISTINCT version_id) FROM goose_db_version WHERE version_id BETWEEN 126 AND 142 AND is_applied=1`).Scan(&versions); err != nil {
					t.Fatal(err)
				}
				if versions != 17 {
					t.Fatalf("applied migration count=%d", versions)
				}
			}
		})
	}
}

func TestMigratePreviewImportIndexesReplaysCheckpointMigrations(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 140)
	if _, err := db.Exec(`
ALTER TABLE sessions ADD COLUMN source_branch TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_sessions_source_branch ON sessions(source_branch) WHERE source_branch <> '';
CREATE INDEX sessions_import_conversation ON sessions(harness, provider_conversation_id) WHERE is_terminated = 0;
CREATE INDEX sessions_import_agent ON sessions(harness, agent_session_id) WHERE is_terminated = 0;
INSERT INTO goose_db_version(version_id,is_applied) VALUES(141,1),(142,1);
`); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := migrate(db); err != nil {
			t.Fatalf("migration attempt %d: %v", i, err)
		}
	}

	var checkpointColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name IN (
		'conversation_checkpoint_state',
		'conversation_checkpoint_generation',
		'conversation_checkpoint_native_id',
		'conversation_checkpoint_unsettled'
	)`).Scan(&checkpointColumns); err != nil {
		t.Fatal(err)
	}
	if checkpointColumns != 4 {
		t.Fatalf("checkpoint columns=%d", checkpointColumns)
	}
	var importIndexes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('sessions_import_conversation','sessions_import_agent')`).Scan(&importIndexes); err != nil {
		t.Fatal(err)
	}
	if importIndexes != 2 {
		t.Fatalf("import indexes=%d", importIndexes)
	}
}
