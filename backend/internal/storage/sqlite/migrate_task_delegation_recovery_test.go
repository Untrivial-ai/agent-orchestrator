package sqlite

import (
	"strings"
	"testing"
)

func TestMigrateKeepsLegacyDelegationOutcomeUnknown(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 155)
	if _, err := db.Exec(`INSERT INTO task_delegations
		(idempotency_key, request_fingerprint, state, created_at, updated_at)
		VALUES ('legacy', ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "v1:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var recoverable int
	var state string
	if err := db.QueryRow(`SELECT recoverable, state FROM task_delegations WHERE idempotency_key = 'legacy'`).Scan(&recoverable, &state); err != nil {
		t.Fatal(err)
	}
	if recoverable != 0 || state != "pending" {
		t.Fatalf("legacy outcome changed: recoverable=%d state=%s", recoverable, state)
	}
}

func TestMigrateTaskDelegationStartupCheckpoints(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 156)
	if _, err := db.Exec(`INSERT INTO projects(id, path, display_name, registered_at)
		VALUES ('startup', '/tmp/startup', 'startup', CURRENT_TIMESTAMP);
		INSERT INTO sessions(id, project_id, num, harness, activity_last_at, created_at, updated_at)
		VALUES ('startup-1', 'startup', 1, 'codex', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key         string
		recoverable int
		state       string
		worker      any
	}{
		{"legacy", 0, "completed", "startup-1"},
		{"ambiguous", 1, "completed", "startup-1"},
		{"reserved", 1, "pending", nil},
	} {
		if _, err := db.Exec(`INSERT INTO task_delegations
			(idempotency_key, request_fingerprint, state, worker_id, recoverable, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, tc.key, "v1:"+strings.Repeat("a", 64), tc.state, tc.worker, tc.recoverable); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"legacy": "legacy", "ambiguous": "starting", "reserved": "seeded"} {
		var state string
		if err := db.QueryRow(`SELECT startup_state FROM task_delegations WHERE idempotency_key = ?`, key).Scan(&state); err != nil || state != want {
			t.Fatalf("%s: state=%s want=%s err=%v", key, state, want, err)
		}
	}
}
