package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigration0085AgentSwitchIntegrityAndCDC(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 85)
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at)
VALUES ('switch-schema', '/repos/switch-schema', ?);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at
) VALUES ('switch-session', 'switch-schema', 1, 'claude-code', ?, ?, ?);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at
) VALUES ('switch-error-session', 'switch-schema', 2, 'claude-code', ?, ?, ?);
`, now, now, now, now, now, now, now); err != nil {
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

	var transcriptStatus string
	if err := db.QueryRow(`
SELECT source_transcript_status FROM agent_switches WHERE id = 'switch-1';
`).Scan(&transcriptStatus); err != nil {
		t.Fatalf("read source transcript status: %v", err)
	}
	if transcriptStatus != "not_attempted" {
		t.Fatalf("source transcript status = %q, want not_attempted", transcriptStatus)
	}
	if _, err := db.Exec(`
UPDATE agent_switches
SET source_transcript_status = 'maybe', updated_at = ?
WHERE id = 'switch-1';
`, now.Add(time.Minute)); err == nil {
		t.Fatal("agent_switches accepted an invalid source transcript status")
	}
	if _, err := db.Exec(`
UPDATE agent_switches
SET semantic_handoff_included = 2, updated_at = ?
WHERE id = 'switch-1';
`, now.Add(time.Minute)); err == nil {
		t.Fatal("agent_switches accepted an invalid semantic handoff inclusion fact")
	}

	errorCodeCases := []struct {
		name             string
		state            string
		errorCode        string
		targetGeneration string
		targetHandle     string
		wantErr          bool
	}{
		{
			name:      "unknown code",
			state:     "failed",
			errorCode: "unknown_failure",
			wantErr:   true,
		},
		{
			name:    "failed without code",
			state:   "failed",
			wantErr: true,
		},
		{
			name:      "recovery marker in wrong state",
			state:     "preparing_handoff",
			errorCode: "target_start_unconfirmed",
			wantErr:   true,
		},
		{
			name:             "recovery marker with target handle",
			state:            "starting_target",
			errorCode:        "target_start_unconfirmed",
			targetGeneration: "target-generation",
			targetHandle:     "target-handle",
			wantErr:          true,
		},
		{
			name:      "recovery marker as failure code",
			state:     "failed",
			errorCode: "target_start_unconfirmed",
			wantErr:   true,
		},
		{
			name:      "failure code on active switch",
			state:     "preparing_handoff",
			errorCode: "failed_pre_stop",
			wantErr:   true,
		},
		{
			name:      "terminal failure",
			state:     "failed",
			errorCode: "failed_pre_stop",
		},
		{
			name:      "target-start recovery",
			state:     "starting_target",
			errorCode: "target_start_unconfirmed",
		},
	}
	for i, tc := range errorCodeCases {
		t.Run(tc.name, func(t *testing.T) {
			switchID := fmt.Sprintf("switch-error-%d", i)
			defer func() {
				if _, err := db.Exec(`DELETE FROM agent_switches WHERE id = ?`, switchID); err != nil {
					t.Errorf("clean up error-code switch: %v", err)
				}
			}()
			_, err := db.Exec(`
INSERT INTO agent_switches (
    id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness, state,
    agent_handoff_status, source_generation_id,
    target_generation_id, target_runtime_handle_id, error_code,
    requested_at, updated_at
) VALUES (?, 'switch-error-session', ?, ?, 'claude-code', 'codex', ?,
          'not_attempted', 'source-generation', ?, ?, ?, ?, ?);
`,
				switchID,
				fmt.Sprintf("switch-error-key-%d", i),
				"v1:"+strings.Repeat("b", 64),
				tc.state,
				tc.targetGeneration,
				tc.targetHandle,
				tc.errorCode,
				now,
				now,
			)
			if tc.wantErr && err == nil {
				t.Fatal("agent_switches accepted an invalid error-code/state combination")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("agent_switches rejected a valid error-code/state combination: %v", err)
			}
		})
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
SET state = 'failed', error_code = 'failed_pre_stop', updated_at = ?
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

func TestMigration0125AgentSwitchFailureConstraintCDCAndIndexes(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 118)

	now := time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at) VALUES ('failure-schema', '/repos/failure-schema', ?);
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('failure-session', 'failure-schema', 1, 'claude-code', ?, ?, ?);
INSERT INTO projects (id,path,registered_at) VALUES ('copy-project','/repos/copy-project',?);
INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at)
VALUES ('copy-session','copy-project',1,'claude-code',?,?,?);
INSERT INTO projects (id,path,registered_at) VALUES ('legacy-marker-project','/repos/legacy-marker',?);
INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at)
VALUES ('legacy-marker-session','legacy-marker-project',1,'claude-code',?,?,?);
INSERT INTO projects (id,path,registered_at) VALUES ('legacy-active-project','/repos/legacy-active',?);
INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at)
VALUES ('legacy-active-session','legacy-active-project',1,'claude-code',?,?,?);
INSERT INTO projects (id,path,registered_at) VALUES ('legacy-multiple-project','/repos/legacy-multiple',?);
INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at)
VALUES ('legacy-multiple-session','legacy-multiple-project',1,'claude-code',?,?,?);
`, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed parents: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO agent_switches (
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,
 target_native_session_ref,target_start_mode,state,agent_handoff_status,
 source_transcript_status,semantic_handoff_included,agent_handoff_path,agent_handoff_hash,
 source_generation_id,target_generation_id,target_runtime_handle_id,target_acknowledged_at,
 error_code,requested_at,updated_at,final_handoff_path,final_handoff_hash
) VALUES (
 'copy-switch','copy-session','copy-key',?,'claude-code','codex',NULL,'resumed','failed','received',
 'available',1,'handoff.json',?,'source-copy','target-copy','handle-copy',?,
 'delivery_unconfirmed',?,?, 'final.json',?
);`, "v1:"+strings.Repeat("c", 64), strings.Repeat("a", 64),
		now.Add(time.Second), now, now.Add(2*time.Second), strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed pre-0125 switch: %v", err)
	}
	const projection = `json_array(
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,
 target_native_session_ref,target_start_mode,state,agent_handoff_status,
 source_transcript_status,semantic_handoff_included,agent_handoff_path,agent_handoff_hash,
 source_generation_id,target_generation_id,target_runtime_handle_id,target_acknowledged_at,
 error_code,requested_at,updated_at,final_handoff_path,final_handoff_hash)`
	var copyBefore string
	if err := db.QueryRow(`SELECT ` + projection + ` FROM agent_switches WHERE id='copy-switch'`).Scan(&copyBefore); err != nil {
		t.Fatalf("read switch before 0125: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_switches (
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,state,
 agent_handoff_status,source_generation_id,error_code,requested_at,updated_at
) VALUES (
 'legacy-marker-switch','legacy-marker-session','legacy-marker-key',?,'claude-code','codex','failed',
 'not_attempted','source-generation','source_stop_unconfirmed',?,?
);`, "v1:"+strings.Repeat("d", 64), now, now); err != nil {
		t.Fatalf("seed legacy retained marker: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO agent_switches (
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,state,
 agent_handoff_status,source_generation_id,error_code,requested_at,updated_at
) VALUES
 ('legacy-active-marker','legacy-active-session','legacy-active-marker-key',?,'claude-code','codex','failed',
  'not_attempted','source-generation','source_stop_unconfirmed',?,?),
 ('legacy-active-current','legacy-active-session','legacy-active-current-key',?,'claude-code','codex','preparing_handoff',
  'not_attempted','source-generation','',?,?),
 ('legacy-multiple-older','legacy-multiple-session','legacy-multiple-older-key',?,'claude-code','codex','failed',
  'not_attempted','source-generation','source_stop_unconfirmed',?,?),
 ('legacy-multiple-newer','legacy-multiple-session','legacy-multiple-newer-key',?,'claude-code','codex','failed',
  'not_attempted','source-generation','source_stop_unconfirmed',?,?);`,
		"v1:"+strings.Repeat("e", 64), now, now,
		"v1:"+strings.Repeat("f", 64), now.Add(time.Second), now.Add(time.Second),
		"v1:"+strings.Repeat("1", 64), now, now,
		"v1:"+strings.Repeat("2", 64), now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatalf("seed conflicting legacy retained markers: %v", err)
	}

	upTo(t, db, 125)
	var copyAfter, copyFailurePoint string
	if err := db.QueryRow(`SELECT `+projection+`,failure_point FROM agent_switches WHERE id='copy-switch'`).Scan(&copyAfter, &copyFailurePoint); err != nil {
		t.Fatalf("read switch after 0125: %v", err)
	}
	if copyAfter != copyBefore {
		t.Fatalf("0125 changed an existing switch column:\nbefore=%s\nafter=%s", copyBefore, copyAfter)
	}
	if copyFailurePoint != "" {
		t.Fatalf("migrated failure_point = %q, want empty", copyFailurePoint)
	}
	var legacyState, legacyCode, legacyFailurePoint string
	if err := db.QueryRow(`SELECT state,error_code,failure_point FROM agent_switches WHERE id='legacy-marker-switch'`).Scan(&legacyState, &legacyCode, &legacyFailurePoint); err != nil {
		t.Fatalf("read migrated retained marker: %v", err)
	}
	if legacyState != "stopping_source" || legacyCode != "source_stop_unconfirmed" || legacyFailurePoint != "" {
		t.Fatalf("migrated marker = (%q,%q,%q), want (stopping_source,source_stop_unconfirmed,empty)", legacyState, legacyCode, legacyFailurePoint)
	}
	for _, tc := range []struct {
		id, wantState, wantCode string
	}{
		{"legacy-active-marker", "failed", "failed_post_stop"},
		{"legacy-active-current", "preparing_handoff", ""},
		{"legacy-multiple-older", "failed", "failed_post_stop"},
		{"legacy-multiple-newer", "stopping_source", "source_stop_unconfirmed"},
	} {
		var state, code string
		if err := db.QueryRow(`SELECT state, error_code FROM agent_switches WHERE id = ?`, tc.id).Scan(&state, &code); err != nil {
			t.Fatalf("read migrated %s: %v", tc.id, err)
		}
		if state != tc.wantState || code != tc.wantCode {
			t.Fatalf("migrated %s = (%q,%q), want (%q,%q)", tc.id, state, code, tc.wantState, tc.wantCode)
		}
	}
	for _, sessionID := range []string{"legacy-active-session", "legacy-multiple-session"} {
		var active int
		if err := db.QueryRow(`SELECT count(*) FROM agent_switches WHERE session_id = ? AND state NOT IN ('completed', 'failed')`, sessionID).Scan(&active); err != nil {
			t.Fatalf("count active switches for %s: %v", sessionID, err)
		}
		if active != 1 {
			t.Fatalf("active switches for %s = %d, want 1", sessionID, active)
		}
	}

	type stateCase struct {
		name, state, code, targetGeneration, targetHandle string
		wantErr                                           bool
	}
	cases := []stateCase{
		{name: "failed rejects source stop marker", state: "failed", code: "source_stop_unconfirmed", wantErr: true},
		{name: "failed rejects source restore marker", state: "failed", code: "source_restore_unconfirmed", wantErr: true},
		{name: "failed rejects target start marker", state: "failed", code: "target_start_unconfirmed", wantErr: true},
		{name: "stopping source marker", state: "stopping_source", code: "source_stop_unconfirmed"},
		{name: "source stopped restore marker", state: "source_stopped", code: "source_restore_unconfirmed"},
		{name: "starting target restore marker", state: "starting_target", code: "source_restore_unconfirmed", targetGeneration: "target-1"},
		{name: "starting target marker without handle", state: "starting_target", code: "target_start_unconfirmed", targetGeneration: "target-1"},
		{name: "starting target marker rejects handle", state: "starting_target", code: "target_start_unconfirmed", targetGeneration: "target-1", targetHandle: "handle", wantErr: true},
		{name: "delivery uncertainty remains terminal", state: "failed", code: "delivery_unconfirmed"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(`
INSERT INTO agent_switches (
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,state,
 agent_handoff_status,source_generation_id,target_generation_id,target_runtime_handle_id,
 error_code,failure_point,requested_at,updated_at
) VALUES (?, 'failure-session', ?, ?, 'claude-code', 'codex', ?, 'not_attempted',
          'source-1', ?, ?, ?, 'classification_unknown', ?, ?)`,
				fmt.Sprintf("constraint-%d", i), fmt.Sprintf("key-%d", i), "v1:"+strings.Repeat("a", 64),
				tc.state, tc.targetGeneration, tc.targetHandle, tc.code, now, now)
			if tc.wantErr && err == nil {
				t.Fatal("invalid state/error combination was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid state/error combination was rejected: %v", err)
			}
			if !tc.wantErr {
				_, _ = db.Exec(`DELETE FROM agent_switches WHERE id=?`, fmt.Sprintf("constraint-%d", i))
			}
		})
	}

	wantObjects := map[string]string{
		"idx_agent_switches_one_active_per_session":    "WHERE state NOT IN ('completed', 'failed')",
		"idx_agent_switches_session_history":           "requested_at DESC, id DESC",
		"agent_switches_target_native_scope_insert":    "scope mismatch",
		"agent_switches_target_native_scope_update":    "scope mismatch",
		"agent_switches_cdc_insert":                    "json_object('id', NEW.session_id)",
		"agent_switches_cdc_update":                    "json_object('id', NEW.session_id)",
		"agent_switches_failed_recovery_marker_insert": "recovery marker requires a nonterminal state",
		"agent_switches_failed_recovery_marker_update": "recovery marker requires a nonterminal state",
	}
	for name, fragment := range wantObjects {
		var definition string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil {
			t.Errorf("read sqlite object %s: %v", name, err)
			continue
		}
		if !strings.Contains(definition, fragment) {
			t.Errorf("sqlite object %s does not preserve latest definition: %s", name, definition)
		}
	}

	if _, err := db.Exec(`
INSERT INTO agent_switches (
 id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,state,
 agent_handoff_status,source_generation_id,requested_at,updated_at
) VALUES ('cdc-switch','failure-session','cdc-key',?,'claude-code','codex','preparing_handoff',
          'not_attempted','source-1',?,?)`, "v1:"+strings.Repeat("b", 64), now, now); err != nil {
		t.Fatalf("insert CDC switch: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM change_log WHERE session_id='failure-session'`).Scan(&before); err != nil {
		t.Fatalf("count CDC before: %v", err)
	}
	if _, err := db.Exec(`UPDATE agent_switches SET state='failed',error_code='failed_pre_stop',failure_point='worker_start_refused',updated_at=? WHERE id='cdc-switch'`, now.Add(time.Second)); err != nil {
		t.Fatalf("update CDC switch: %v", err)
	}
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM change_log WHERE session_id='failure-session'`).Scan(&after); err != nil {
		t.Fatalf("count CDC after: %v", err)
	}
	if after != before+1 {
		t.Fatalf("switch mutation emitted %d change rows, want exactly one trigger-owned row", after-before)
	}

	var fkViolations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkViolations); err != nil || fkViolations != 0 {
		t.Fatalf("foreign key violations = %d, err=%v", fkViolations, err)
	}
}
