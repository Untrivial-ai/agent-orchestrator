package sqlite

import (
	"testing"
	"time"
)

func TestMigration0142RemovesRetiredCodexAccountSwitchState(t *testing.T) {
	for _, row := range []struct {
		id, phase, failureCode, wantPhase, wantCode string
		terminal                                    bool
	}{
		{id: "pre-credential", phase: "stopping_sessions", wantPhase: "failed", wantCode: "legacy_session_switch_retired", terminal: true},
		{id: "post-credential", phase: "restarting_sessions", wantPhase: "recovery_required", wantCode: "legacy_switch_recovery"},
		{id: "stop-unconfirmed", phase: "recovery_required", failureCode: "stop_unconfirmed", wantPhase: "failed", wantCode: "legacy_session_switch_retired", terminal: true},
	} {
		t.Run(row.id, func(t *testing.T) {
			db := openTestDB(t)
			upTo(t, db, 141)
			now := time.Now().UTC().Truncate(time.Second)
			if _, err := db.Exec(`INSERT INTO codex_account_switches (
			id, source_account_id, target_account_id, idempotency_key,
			request_fingerprint, expected_account_revision, phase, failure_code,
			created_at, updated_at, source_kind
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.id, "source", "target", row.id+"-request", "v1:"+row.id,
				1, row.phase, row.failureCode, now, now, "managed"); err != nil {
				t.Fatalf("seed %s: %v", row.id, err)
			}

			upTo(t, db, 142)

			for _, name := range []string{"codex_account_switch_sessions", "restart_running_sessions"} {
				var count int
				query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
				if name == "restart_running_sessions" {
					query = `SELECT COUNT(*) FROM pragma_table_info('codex_account_switches') WHERE name = ?`
				}
				if err := db.QueryRow(query, name).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("retired schema %s still exists", name)
				}
			}

			var phase, code string
			var completedAt any
			if err := db.QueryRow(`SELECT phase, failure_code, completed_at FROM codex_account_switches WHERE id = ?`, row.id).
				Scan(&phase, &code, &completedAt); err != nil {
				t.Fatal(err)
			}
			if phase != row.wantPhase || code != row.wantCode || (completedAt != nil) != row.terminal {
				t.Fatalf("switch %s = (%s,%s,%v), want (%s,%s,%v)", row.id, phase, code, completedAt != nil, row.wantPhase, row.wantCode, row.terminal)
			}
		})
	}
}
