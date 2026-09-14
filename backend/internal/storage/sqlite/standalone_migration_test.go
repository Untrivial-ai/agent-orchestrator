package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestStandaloneProjectColumnsAreNullable(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"sessions", "change_log", "notifications", "conversations"} {
		func() {
			rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
			if err != nil {
				t.Fatalf("%s table info: %v", table, err)
			}
			defer rows.Close()
			found := false
			for rows.Next() {
				var cid, notNull, primaryKey int
				var name, columnType string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
					t.Fatal(err)
				}
				if name == "project_id" {
					found = true
					if notNull != 0 {
						t.Fatalf("%s.project_id remains NOT NULL", table)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatalf("%s.project_id not found", table)
			}
		}()
	}
}

func TestStandaloneMigrationConvertsLegacyScratchOwnership(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 122)

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
`, timestamp,
		timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp, timestamp,
		timestamp, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		"SELECT project_id FROM sessions WHERE id = 'scratch-1'",
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
	var remainingScratchEvents int
	if err := db.QueryRow("SELECT COUNT(*) FROM change_log WHERE project_id = 'scratch' AND session_id = 'scratch-1'").Scan(&remainingScratchEvents); err != nil {
		t.Fatal(err)
	}
	if remainingScratchEvents != 0 {
		t.Fatalf("change_log retains %d Scratch-owned events", remainingScratchEvents)
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
