package sqlite

import (
	"testing"
	"time"
)

func TestMigration0146SimplifiesCodexAccountManagement(t *testing.T) {
	rows := []struct {
		id, phase, failureCode, wantPhase, wantCode string
		terminal                                    bool
	}{
		{id: "pre-credential", phase: "stopping_sessions", wantPhase: "failed", wantCode: "legacy_session_switch_retired", terminal: true},
		{id: "post-credential", phase: "restarting_sessions", wantPhase: "recovery_required", wantCode: "legacy_switch_recovery"},
		{id: "verifying-target", phase: "verifying_target", wantPhase: "recovery_required", wantCode: "legacy_switch_recovery"},
		{id: "rollback-required", phase: "rollback_required", wantPhase: "recovery_required", wantCode: "legacy_switch_recovery"},
		{id: "stop-unconfirmed", phase: "recovery_required", failureCode: "stop_unconfirmed", wantPhase: "failed", wantCode: "legacy_session_switch_retired", terminal: true},
	}

	for _, row := range rows {
		t.Run(row.id, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 140)
			now := time.Now().UTC().Truncate(time.Second)
			if _, err := db.Exec(`INSERT INTO codex_active_account
				(singleton_id, account_id, revision, activated_at, updated_at)
				VALUES (1, 'account-a', 7, ?, ?)`, now, now); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO codex_account_switches (
			id, source_account_id, target_account_id, idempotency_key,
			request_fingerprint, expected_account_revision, phase, failure_code,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.id, "source", "target", row.id+"-request", "v1:"+row.id,
				1, row.phase, row.failureCode, now, now); err != nil {
				t.Fatalf("seed %s: %v", row.id, err)
			}

			upTo(t, db, 146)

			for _, name := range []string{"codex_active_account", "codex_account_switch_sessions"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("retired schema %s still exists", name)
				}
			}

			for _, column := range []string{"request_fingerprint", "expected_account_revision"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('codex_account_switches') WHERE name = ?`, column).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("retired column %s still exists", column)
				}
			}

			var phase, code, sourceKind string
			var completedAt any
			if err := db.QueryRow(`SELECT phase, failure_code, completed_at, source_kind FROM codex_account_switches WHERE id = ?`, row.id).
				Scan(&phase, &code, &completedAt, &sourceKind); err != nil {
				t.Fatal(err)
			}
			if phase != row.wantPhase || code != row.wantCode || (completedAt != nil) != row.terminal || sourceKind != "managed" {
				t.Fatalf("switch %s = (%s,%s,%v), want (%s,%s,%v)", row.id, phase, code, completedAt != nil, row.wantPhase, row.wantCode, row.terminal)
			}
		})
	}
}
