package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateRepairsRenumberedAgentInventoryHistory(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 118)
	seedCompletedPlanBeforeFinalization(t, db)
	applyLegacyCodexProfileMigrations(t, db, []legacyCodexProfileMigration{
		{version: 119, canonicalPath: "migrations/0122_drop_agent_inventory_cache.sql", legacyName: "drop_agent_inventory_cache.sql"},
	})

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with legacy agent-inventory 0119: %v", err)
	}

	assertAppliedMigrations(t, db, 119, 120, 121, 122)
	var status string
	if err := db.QueryRow(`
SELECT json_extract(plan_json, '$.steps[0].status')
FROM conversation_turns WHERE id = 'legacy-0119-turn'`).Scan(&status); err != nil {
		t.Fatalf("read completed plan after migration repair: %v", err)
	}
	if status != "completed" {
		t.Fatalf("completed plan status = %q, want completed", status)
	}

	var applied119ID int64
	if err := db.QueryRow(`
SELECT id FROM goose_db_version
WHERE version_id = 119 AND is_applied = 1
ORDER BY id DESC LIMIT 1`).Scan(&applied119ID); err != nil {
		t.Fatalf("read canonical 0119 ledger row: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
	var reapplied119ID int64
	if err := db.QueryRow(`
SELECT id FROM goose_db_version
WHERE version_id = 119 AND is_applied = 1
ORDER BY id DESC LIMIT 1`).Scan(&reapplied119ID); err != nil {
		t.Fatalf("read canonical 0119 ledger row after second pass: %v", err)
	}
	if reapplied119ID != applied119ID {
		t.Fatalf("canonical 0119 ledger row changed from %d to %d on second pass", applied119ID, reapplied119ID)
	}
}

func TestMigrateRepairsAgentInventoryHistoryFromCollidedVersion120(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 119)
	now := time.Now().UTC()
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('legacy-0120-project', '/repo/legacy-0120', ?, '{}');
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at, session_mode
) VALUES (
    'legacy-0120-session', 'legacy-0120-project', 1, 'codex',
    '2026-06-28 18:45:08.349363 +0800 CST m=+25660.013723251', ?, ?, 'chat'
);`, now, now, now); err != nil {
		t.Fatalf("seed local-zone activity timestamp: %v", err)
	}
	applyLegacyCodexProfileMigrations(t, db, []legacyCodexProfileMigration{
		{version: 120, canonicalPath: "migrations/0122_drop_agent_inventory_cache.sql", legacyName: "drop_agent_inventory_cache.sql"},
	})

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with legacy agent-inventory 0120: %v", err)
	}

	assertAppliedMigrations(t, db, 119, 120, 121, 122)
	var activityLastAt string
	if err := db.QueryRow(`
SELECT CAST(activity_last_at AS TEXT) FROM sessions WHERE id = 'legacy-0120-session'`).Scan(&activityLastAt); err != nil {
		t.Fatalf("read normalized activity timestamp: %v", err)
	}
	if activityLastAt != "2026-06-28 10:45:08.349363 +0000 UTC" {
		t.Fatalf("activity_last_at = %q, want canonical UTC", activityLastAt)
	}
}

func TestMigrateRepairsAgentInventoryHistoryFromCollidedVersion121(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 120)
	applyLegacyCodexProfileMigrations(t, db, []legacyCodexProfileMigration{
		{version: 121, canonicalPath: "migrations/0122_drop_agent_inventory_cache.sql", legacyName: "drop_agent_inventory_cache.sql"},
	})

	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with legacy agent-inventory 0121: %v", err)
	}

	assertAppliedMigrations(t, db, 119, 120, 121, 122)
	var reviewerConfigColumn int
	if err := db.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'reviewer_agent_config'`).Scan(&reviewerConfigColumn); err != nil {
		t.Fatalf("read reviewer agent-config column: %v", err)
	}
	if reviewerConfigColumn != 1 {
		t.Fatalf("reviewer_agent_config columns = %d, want 1", reviewerConfigColumn)
	}
}

func seedCompletedPlanBeforeFinalization(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('legacy-0119-project', '/repo/legacy-0119', ?, '{}');
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at, session_mode
) VALUES ('legacy-0119-session', 'legacy-0119-project', 1, 'codex', ?, ?, ?, 'chat');
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id, latest_sequence,
    created_at, updated_at, active_branch_id
) VALUES (
    'legacy-0119-conversation', 'session', 'legacy-0119-project', 'legacy-0119-session',
    'legacy-0119-session', 0, ?, ?, 'legacy-0119-branch'
);
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id, created_at
) VALUES (
    'legacy-0119-branch', 'legacy-0119-conversation', 'legacy-0119-session',
    'legacy-0119-thread', ?
);
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id, state,
    requested_at, completed_at, plan_json, branch_id
) VALUES (
    'legacy-0119-turn', 'legacy-0119-conversation', 'legacy-0119-session',
    'legacy-0119-provider-turn', 'completed', ?, ?,
    '{"steps":[{"text":"one","status":"in_progress"}]}', 'legacy-0119-branch'
);`, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed completed plan before canonical 0119: %v", err)
	}
}
