package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestStandaloneMigrationConvertsLegacyScratchOwnership(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// Build the boundary shape directly and mark the preceding migration as
	// applied. Replaying the complete migration history here adds minutes to the
	// SQLite race suite without testing any extra standalone behavior.
	if _, err := db.Exec(`
CREATE TABLE goose_db_version (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL,
    is_applied INTEGER NOT NULL,
    tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
WITH RECURSIVE versions(version_id) AS (
    SELECT 1
    UNION ALL
    SELECT version_id + 1 FROM versions WHERE version_id < 139
)
INSERT INTO goose_db_version (version_id, is_applied)
SELECT version_id, 1 FROM versions;
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    display_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    registered_at TIMESTAMP NOT NULL,
    archived_at TIMESTAMP
);
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    num INTEGER NOT NULL,
    kind TEXT NOT NULL,
    harness TEXT NOT NULL,
    activity_state TEXT NOT NULL DEFAULT 'idle',
    activity_last_at TIMESTAMP NOT NULL,
    is_terminated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE TABLE change_log (
    id INTEGER PRIMARY KEY,
    project_id TEXT NOT NULL,
    session_id TEXT
);
CREATE TABLE notifications (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    project_id TEXT NOT NULL,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);
CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    project_id TEXT NOT NULL,
    session_id TEXT,
    current_session_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE TABLE usage_bindings (
    session_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    native_root_id TEXT NOT NULL,
    state TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
`); err != nil {
		t.Fatal(err)
	}

	const timestamp = "2026-09-02T12:00:00Z"
	if _, err := db.Exec(`
INSERT INTO projects (id, path, display_name, kind, registered_at)
VALUES ('scratch', '/legacy/scratch', 'Scratch', 'scratch', ?);
INSERT INTO sessions (id, project_id, num, kind, harness, activity_last_at, created_at, updated_at)
VALUES ('scratch-orchestrator', 'scratch', 0, 'orchestrator', 'codex', ?, ?, ?);
INSERT INTO sessions (id, project_id, num, kind, harness, activity_last_at, created_at, updated_at)
VALUES ('scratch-1', 'scratch', 1, 'worker', 'codex', ?, ?, ?);
INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at)
VALUES
  ('scratch-orchestrator', 'codex', 'root-active', 'active', ?),
  ('scratch-orchestrator', 'codex', 'root-discovering', 'discovering', ?),
  ('scratch-orchestrator', 'codex', 'root-complete', 'complete', ?),
  ('scratch-1', 'codex', 'worker-active', 'active', ?);
INSERT INTO notifications (id, session_id, project_id, type, title, created_at)
VALUES ('notice-1', 'scratch-1', 'scratch', 'needs_input', 'Input needed', ?);
INSERT INTO conversations (id, scope, project_id, session_id, current_session_id, created_at, updated_at)
VALUES ('conversation-1', 'session', 'scratch', 'scratch-1', 'scratch-1', ?, ?);
INSERT INTO change_log (id, project_id, session_id)
VALUES (1, 'scratch', 'scratch-1');
`, timestamp,
		timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	gooseMu.Lock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		gooseMu.Unlock()
		t.Fatalf("set dialect: %v", err)
	}
	err = goose.UpTo(db, "migrations", 140)
	gooseMu.Unlock()
	if err != nil {
		t.Fatalf("apply standalone migration: %v", err)
	}

	for _, table := range []string{"sessions", "change_log", "notifications", "conversations"} {
		var nullableColumns int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'project_id' AND \"notnull\" = 0", table).Scan(&nullableColumns); err != nil {
			t.Fatalf("%s table info: %v", table, err)
		}
		if nullableColumns != 1 {
			t.Fatalf("%s.project_id remains NOT NULL", table)
		}
	}

	for _, query := range []string{
		"SELECT project_id FROM sessions WHERE id = 'scratch-1'",
		"SELECT project_id FROM change_log WHERE id = 1",
		"SELECT project_id FROM notifications WHERE id = 'notice-1'",
		"SELECT project_id FROM conversations WHERE id = 'conversation-1'",
	} {
		var projectID any
		if err := db.QueryRow(query).Scan(&projectID); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if projectID != nil {
			t.Fatalf("%s returned project_id = %#v, want NULL", query, projectID)
		}
	}
	var archivedAt any
	if err := db.QueryRow("SELECT archived_at FROM projects WHERE id = 'scratch'").Scan(&archivedAt); err != nil {
		t.Fatal(err)
	}
	if archivedAt == nil {
		t.Fatal("legacy Scratch project was not archived")
	}
	for root, want := range map[string]string{
		"root-active":      "finalizing",
		"root-discovering": "finalizing",
		"root-complete":    "complete",
		"worker-active":    "active",
	} {
		var got string
		if err := db.QueryRow("SELECT state FROM usage_bindings WHERE native_root_id = ?", root).Scan(&got); err != nil {
			t.Fatalf("usage binding %s: %v", root, err)
		}
		if got != want {
			t.Fatalf("usage binding %s state = %q, want %q", root, got, want)
		}
	}
}
