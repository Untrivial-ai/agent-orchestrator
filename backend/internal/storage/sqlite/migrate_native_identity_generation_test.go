package sqlite

import (
	"testing"
	"time"
)

func TestMigration0098LeavesLegacyNativeIdentitiesUnverified(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 97)

	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	mustExec(t, db, `
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('identity-proof', '/tmp/identity-proof', 'identity proof', ?);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, first_signal_at,
    runtime_launch_id, agent_session_id, created_at, updated_at
) VALUES (
    'confirmed', 'identity-proof', 1, 'codex', ?, ?,
    'launch-confirmed', 'native-confirmed', ?, ?
);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at,
    runtime_launch_id, agent_session_id, created_at, updated_at
) VALUES (
    'ambiguous', 'identity-proof', 2, 'codex', ?,
    'launch-current', 'native-may-be-stale', ?, ?
);
`, now, now, now, now, now, now, now)

	upTo(t, db, 98)

	rows, err := db.Query(`
SELECT id, agent_session_id_launch_id
FROM sessions
ORDER BY id;
`)
	if err != nil {
		t.Fatalf("read migrated identity provenance: %v", err)
	}
	defer func() { _ = rows.Close() }()

	want := map[string]string{"ambiguous": "", "confirmed": ""}
	for rows.Next() {
		var id, launchID string
		if err := rows.Scan(&id, &launchID); err != nil {
			t.Fatalf("scan migrated identity provenance: %v", err)
		}
		if launchID != want[id] {
			t.Errorf("session %s identity launch = %q, want %q", id, launchID, want[id])
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated identity provenance: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("missing migrated sessions: %v", want)
	}
}
