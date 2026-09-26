package sqlite

import (
	"database/sql"
	"strings"
	"testing"
)

func TestMigratePreservesPreviewCuesAtOldVersion149(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 148)
	seedPreviewCues(t, db, 149)
	assertPreviewCueMigration(t, db)
}

func TestMigratePreservesPreviewCuesAtOldVersion155(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 154)
	seedPreviewCues(t, db, 155)
	assertPreviewCueMigration(t, db)
}

func TestMigratePreservesPreviewCuesAtOldVersion156(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 155)
	seedPreviewCues(t, db, 156)
	assertPreviewCueMigration(t, db)
}

func seedPreviewCues(t *testing.T, db *sql.DB, version int64) {
	t.Helper()
	_, err := db.Exec(`
CREATE TABLE cues (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN ('command', 'agent')),
    command TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (project_id, name)
);
INSERT INTO projects (id, path, repo_origin_url, display_name, registered_at, config, kind)
VALUES ('scratch', 'C:\scratch', '', 'Scratch', '2026-09-24T00:00:00Z', '{}', 'single_repo');
INSERT INTO cues (id, project_id, name, type, command, created_at, updated_at)
VALUES ('cue-1', 'scratch', 'Check status', 'command', 'git status', '2026-09-24T00:00:00Z', '2026-09-24T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed preview cue schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version); err != nil {
		t.Fatalf("seed preview cue migration version %d: %v", version, err)
	}
}

func assertPreviewCueMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if err := migrate(db); err != nil {
			t.Fatalf("migrate preview cue database (pass %d): %v", i+1, err)
		}
	}
	var name, command string
	if err := db.QueryRow(`SELECT name, command FROM cues WHERE id = 'cue-1'`).Scan(&name, &command); err != nil {
		t.Fatalf("read preserved cue: %v", err)
	}
	if name != "Check status" || command != "git status" {
		t.Fatalf("preserved cue = (%q, %q)", name, command)
	}
	var reviewerColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review') WHERE name = 'interface_mode'`).Scan(&reviewerColumn); err != nil {
		t.Fatal(err)
	}
	if reviewerColumn != 1 {
		t.Fatalf("review.interface_mode count = %d, want reviewer migration applied", reviewerColumn)
	}
	var sessionsSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&sessionsSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionsSQL, "'unreal-agent'") {
		t.Fatal("upstream Unreal Agent migration was not applied")
	}
	for _, version := range []int{149, 155, 156, 157, 158, 159} {
		var applied int
		if err := db.QueryRow(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id = ? ORDER BY id DESC LIMIT 1), 0)`, version).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied = %d, want 1", version, applied)
		}
	}
}
