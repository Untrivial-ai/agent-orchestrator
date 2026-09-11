package sqlite

import (
	"testing"
	"time"
)

func TestMigration0140PreservesHistoricalAccountSwitchRestartBehavior(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 139)

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`INSERT INTO codex_account_switches (
		id, source_account_id, target_account_id, idempotency_key,
		request_fingerprint, expected_account_revision, phase, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"historical-switch", "source", "target", "historical-request",
		"v1:historical", 1, "recovery_required", now, now); err != nil {
		t.Fatalf("seed historical switch: %v", err)
	}

	upTo(t, db, 140)

	var restartRunningSessions bool
	if err := db.QueryRow(`SELECT restart_running_sessions FROM codex_account_switches WHERE id = ?`, "historical-switch").Scan(&restartRunningSessions); err != nil {
		t.Fatalf("read migrated restart policy: %v", err)
	}
	if !restartRunningSessions {
		t.Fatal("historical switch did not retain restart-enabled behavior")
	}
}
