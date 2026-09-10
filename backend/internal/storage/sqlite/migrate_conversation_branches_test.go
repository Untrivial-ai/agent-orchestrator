package sqlite

import (
	"fmt"
	"testing"
	"time"
)

func TestMigration0087BackfillsConversationBranchesAndTimelineRows(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 79)

	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	mustExec(t, db, `INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('branches', '/tmp/branches', 'Branches', ?)`, now)
	mustExec(t, db, `INSERT INTO sessions (
		id, project_id, num, kind, harness, activity_state, activity_last_at,
		is_terminated, session_mode, provider_conversation_id, controller_generation,
		created_at, updated_at
	) VALUES ('branches-1', 'branches', 1, 'worker', 'codex', 'idle', ?, 0,
		'chat', 'thread-root', 'generation-root', ?, ?)`, now, now, now)
	mustExec(t, db, `INSERT INTO conversations (
		id, scope, project_id, session_id, current_session_id, latest_sequence, created_at, updated_at
	) VALUES ('conversation-1', 'session', 'branches', 'branches-1', 'branches-1', 2, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_turns (
		id, conversation_id, handled_by_session_id, provider_turn_id,
		controller_generation, state, requested_at
	) VALUES ('turn-1', 'conversation-1', 'branches-1', 'provider-turn-1',
		'generation-root', 'completed', ?)`, now)
	mustExec(t, db, `INSERT INTO conversation_messages (
		id, conversation_id, turn_id, sequence, role, origin, text,
		delivery_content_json, created_at, updated_at
	) VALUES ('message-1', 'conversation-1', 'turn-1', 1, 'user', 'human',
		'hello', '[{"type":"text","text":"hello"}]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_activities (
		id, conversation_id, turn_id, sequence, kind, status, summary, created_at, updated_at
	) VALUES ('activity-1', 'conversation-1', 'turn-1', 2, 'command', 'completed', 'go test', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_provider_events (
		conversation_id, session_id, provider_event_id, method, payload_json, received_at
	) VALUES ('conversation-1', 'branches-1', 'event-1', 'turn.completed', '{}', ?)`, now)

	upTo(t, db, 87)

	var activeBranchID string
	if err := db.QueryRow(`SELECT active_branch_id FROM conversations WHERE id = 'conversation-1'`).Scan(&activeBranchID); err != nil {
		t.Fatalf("read active branch: %v", err)
	}
	if activeBranchID != "conversation-1:root" {
		t.Fatalf("active branch = %q", activeBranchID)
	}
	var branchConversationID, branchSessionID, providerConversationID string
	if err := db.QueryRow(`SELECT conversation_id, session_id, provider_conversation_id
		FROM conversation_branches WHERE id = ?`, activeBranchID).Scan(
		&branchConversationID, &branchSessionID, &providerConversationID,
	); err != nil {
		t.Fatalf("read root branch: %v", err)
	}
	if branchConversationID != "conversation-1" || branchSessionID != "branches-1" || providerConversationID != "thread-root" {
		t.Fatalf("root branch = conversation:%q session:%q provider:%q",
			branchConversationID, branchSessionID, providerConversationID)
	}

	for _, table := range []string{
		"conversation_turns", "conversation_messages", "conversation_activities", "conversation_provider_events",
	} {
		var branchID string
		if err := db.QueryRow(fmt.Sprintf(`SELECT branch_id FROM %s LIMIT 1`, table)).Scan(&branchID); err != nil {
			t.Fatalf("read %s branch: %v", table, err)
		}
		if branchID != activeBranchID {
			t.Errorf("%s branch = %q, want %q", table, branchID, activeBranchID)
		}
	}
}

func TestMigration0087AssignsNewTimelineRowsToActiveBranch(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 87)

	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	mustExec(t, db, `INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('branches', '/tmp/branches', 'Branches', ?)`, now)
	mustExec(t, db, `INSERT INTO sessions (
		id, project_id, num, kind, harness, activity_state, activity_last_at,
		is_terminated, session_mode, created_at, updated_at
	) VALUES ('branches-1', 'branches', 1, 'worker', 'codex', 'idle', ?, 0, 'chat', ?, ?)`, now, now, now)
	mustExec(t, db, `INSERT INTO conversations (
		id, scope, project_id, session_id, current_session_id, latest_sequence,
		active_branch_id, created_at, updated_at
	) VALUES ('conversation-1', 'session', 'branches', 'branches-1', 'branches-1', 2,
		'branch-child', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_branches (
		id, conversation_id, session_id, provider_conversation_id, created_at
	) VALUES ('branch-child', 'conversation-1', 'branches-1', 'thread-child', ?)`, now)
	mustExec(t, db, `INSERT INTO conversation_turns (
		id, conversation_id, handled_by_session_id, state, requested_at
	) VALUES ('turn-1', 'conversation-1', 'branches-1', 'running', ?)`, now)
	mustExec(t, db, `INSERT INTO conversation_messages (
		id, conversation_id, turn_id, sequence, role, origin, text, created_at, updated_at
	) VALUES ('message-1', 'conversation-1', 'turn-1', 1, 'user', 'human', 'hello', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_activities (
		id, conversation_id, turn_id, sequence, kind, status, summary, created_at, updated_at
	) VALUES ('activity-1', 'conversation-1', 'turn-1', 2, 'command', 'running', 'go test', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_provider_events (
		conversation_id, session_id, method, payload_json, received_at
	) VALUES ('conversation-1', 'branches-1', 'turn.started', '{}', ?)`, now)

	for _, table := range []string{
		"conversation_turns", "conversation_messages", "conversation_activities", "conversation_provider_events",
	} {
		var branchID string
		if err := db.QueryRow(fmt.Sprintf(`SELECT branch_id FROM %s LIMIT 1`, table)).Scan(&branchID); err != nil {
			t.Fatalf("read %s branch: %v", table, err)
		}
		if branchID != "branch-child" {
			t.Errorf("%s branch = %q, want branch-child", table, branchID)
		}
	}
}
