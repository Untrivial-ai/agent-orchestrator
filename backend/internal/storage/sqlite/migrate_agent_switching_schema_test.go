package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigration0083AgentSwitchIntegrityAndCDC(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 83)
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at)
VALUES ('switch-schema', '/repos/switch-schema', ?);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at
) VALUES ('switch-session', 'switch-schema', 1, 'claude-code', ?, ?, ?);
`, now, now, now, now); err != nil {
		t.Fatalf("seed switch parents: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO agent_switches (
    id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness, state,
    agent_handoff_status, source_generation_id, requested_at, updated_at
) VALUES (
    'switch-1', 'switch-session', 'switch-key', ?,
    'claude-code', 'codex', 'preparing_handoff',
    'not_attempted', 'source-generation', ?, ?
);
`, "v1:"+strings.Repeat("a", 64), now, now); err != nil {
		t.Fatalf("seed agent switch: %v", err)
	}

	if _, err := db.Exec(`
UPDATE agent_switches
SET agent_handoff_status = 'received',
    agent_handoff_path = '/ao/handoffs/switch-1/agent-handoff.json',
    agent_handoff_hash = 'BAD',
    updated_at = ?
WHERE id = 'switch-1';
`, now.Add(time.Minute)); err == nil {
		t.Fatal("agent_switches accepted a received handoff with a noncanonical SHA-256")
	}

	var before int
	if err := db.QueryRow(`
SELECT count(*)
FROM change_log
WHERE session_id = 'switch-session' AND event_type = 'session_updated';
`).Scan(&before); err != nil {
		t.Fatalf("count switch CDC rows before update: %v", err)
	}
	if _, err := db.Exec(`
UPDATE agent_switches
SET error_code = 'DURABLE_DIAGNOSTIC', updated_at = ?
WHERE id = 'switch-1';
`, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("update agent switch: %v", err)
	}

	var after int
	if err := db.QueryRow(`
SELECT count(*)
FROM change_log
WHERE session_id = 'switch-session' AND event_type = 'session_updated';
`).Scan(&after); err != nil {
		t.Fatalf("count switch CDC rows after update: %v", err)
	}
	if after != before+1 {
		t.Fatalf("switch CDC rows after update = %d, want %d", after, before+1)
	}
}
