package sqlite

import (
	"testing"
	"time"
)

func TestMigration0107PreservesTurnsAndAllowsRecoveredHistory(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 106)
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)

	mustExec(t, db, `
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('recovered', '/tmp/recovered', 'Recovered', ?);
INSERT INTO sessions (
    id, project_id, num, harness, session_mode, activity_last_at,
    created_at, updated_at
) VALUES ('recovered-1', 'recovered', 1, 'pi', 'chat', ?, ?, ?);
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id,
    active_branch_id, created_at, updated_at
) VALUES (
    'conversation-1', 'session', 'recovered', 'recovered-1', 'recovered-1',
    'conversation-1:root', ?, ?
);
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id, created_at
) VALUES ('conversation-1:root', 'conversation-1', 'recovered-1', 'native-1', ?);
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    state, requested_at, completed_at, branch_id
) VALUES (
    'turn-known', 'conversation-1', 'recovered-1', 'provider-known',
    'completed', ?, ?, 'conversation-1:root'
);
INSERT INTO conversation_messages (
    id, conversation_id, turn_id, sequence, role, origin, text,
    created_at, updated_at, branch_id
) VALUES (
    'message-known', 'conversation-1', 'turn-known', 1, 'user', 'human',
    'known history', ?, ?, 'conversation-1:root'
);`, now, now, now, now, now, now, now, now, now, now, now)
	mustExec(t, db, `
INSERT INTO conversation_activities (
    id, conversation_id, turn_id, sequence, kind, status, summary,
    created_at, updated_at, branch_id
) VALUES (
    'activity-known', 'conversation-1', 'turn-known', 2, 'command',
    'completed', 'known outcome', ?, ?, 'conversation-1:root'
);`, now, now)

	upTo(t, db, 107)

	var knownState, messageTurn string
	if err := db.QueryRow(`SELECT state FROM conversation_turns WHERE id = 'turn-known'`).Scan(&knownState); err != nil {
		t.Fatalf("read preserved turn: %v", err)
	}
	if err := db.QueryRow(`SELECT turn_id FROM conversation_messages WHERE id = 'message-known'`).Scan(&messageTurn); err != nil {
		t.Fatalf("read preserved message: %v", err)
	}
	if knownState != "completed" || messageTurn != "turn-known" {
		t.Fatalf("preserved state/message turn = %q/%q", knownState, messageTurn)
	}

	mustExec(t, db, `
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    state, requested_at, completed_at
) VALUES (
    'turn-recovered', 'conversation-1', 'recovered-1', 'provider-recovered',
    'recovered', ?, ?
);`, now.Add(time.Minute), now.Add(time.Minute))
	var recoveredState, branchID string
	if err := db.QueryRow(`
SELECT state, branch_id FROM conversation_turns WHERE id = 'turn-recovered'
`).Scan(&recoveredState, &branchID); err != nil {
		t.Fatalf("read recovered turn: %v", err)
	}
	if recoveredState != "recovered" || branchID != "conversation-1:root" {
		t.Fatalf("recovered turn state/branch = %q/%q", recoveredState, branchID)
	}
	mustExec(t, db, `
INSERT INTO conversation_activities (
    id, conversation_id, turn_id, sequence, kind, status, summary,
    created_at, updated_at
) VALUES (
    'activity-recovered', 'conversation-1', 'turn-recovered', 3, 'command',
    'recovered', 'unknown outcome', ?, ?
);`, now.Add(time.Minute), now.Add(time.Minute))
	var activityStatus, activityBranch string
	if err := db.QueryRow(`
SELECT status, branch_id FROM conversation_activities WHERE id = 'activity-recovered'
`).Scan(&activityStatus, &activityBranch); err != nil {
		t.Fatalf("read recovered activity: %v", err)
	}
	if activityStatus != "recovered" || activityBranch != "conversation-1:root" {
		t.Fatalf("recovered activity status/branch = %q/%q", activityStatus, activityBranch)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check;`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check: %v", err)
	}
}
