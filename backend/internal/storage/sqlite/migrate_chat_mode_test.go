package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The whole backward-compatibility promise of the Chat feature: a database that
// already has sessions must come out of the migration with every one of them in
// TUI mode, so an upgrade changes nobody's workflow.
func TestMigration0066BackfillsExistingSessionsToTUI(t *testing.T) {
	db := openTestDB(t)

	// Stop before the Chat migration: sessions exist, session_mode does not.
	upTo(t, db, 42)

	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('p1', '/tmp/p1', 'proj', ?)`, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for n, id := range []string{"ao-1", "ao-2"} {
		if _, err := db.Exec(`INSERT INTO sessions (id, project_id, num, kind, activity_state, activity_last_at, is_terminated, created_at, updated_at)
			VALUES (?, 'p1', ?, 'worker', 'idle', ?, 0, ?, ?)`, id, n+1, now, now, now); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}

	upTo(t, db, 66)

	rows, err := db.Query(`SELECT id, session_mode, provider_conversation_id, controller_generation FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	defer func() { _ = rows.Close() }()

	seen := 0
	for rows.Next() {
		var id, mode, providerConv, generation string
		if err := rows.Scan(&id, &mode, &providerConv, &generation); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if mode != "tui" {
			t.Errorf("session %s migrated to mode %q, want tui", id, mode)
		}
		// A pre-existing session has no structured controller, so these must be
		// empty rather than carrying a placeholder that looks like a real handle.
		if providerConv != "" || generation != "" {
			t.Errorf("session %s: provider_conversation_id=%q controller_generation=%q, want both empty",
				id, providerConv, generation)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen != 2 {
		t.Fatalf("found %d sessions, want 2", seen)
	}
}

// A fresh install must also default to TUI: the mode is only ever Chat because
// something explicitly asked for it.
func TestMigration0066FreshDatabaseDefaultsToTUI(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 66)

	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('p1', '/tmp/p1', 'proj', ?)`, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, num, kind, activity_state, activity_last_at, is_terminated, created_at, updated_at)
		VALUES ('ao-new', 'p1', 1, 'worker', 'idle', ?, 0, ?, ?)`, now, now, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	var mode string
	if err := db.QueryRow(`SELECT session_mode FROM sessions WHERE id = 'ao-new'`).Scan(&mode); err != nil {
		t.Fatalf("read mode: %v", err)
	}
	if mode != "tui" {
		t.Fatalf("new session mode = %q, want tui", mode)
	}
}

func TestReconcileSchemaRepairsMissingConversationCurrentSessionID(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 43)

	mustExec(t, db, `INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('p1', '/tmp/p1', 'proj', '2026-08-05T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, num, kind, activity_state, activity_last_at, is_terminated, created_at, updated_at)
		VALUES ('ao-1', 'p1', 1, 'worker', 'idle', '2026-08-05T00:00:00Z', 0, '2026-08-05T00:00:00Z', '2026-08-05T00:00:00Z')`)
	mustExec(t, db, `CREATE TABLE conversations (
		id TEXT PRIMARY KEY,
		scope TEXT NOT NULL,
		project_id TEXT NOT NULL,
		session_id TEXT,
		latest_sequence INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`)
	// reconcileSchema runs after Goose in production, where the chat migration has
	// also created conversation_turns. Keep this direct repair fixture at that same
	// boundary so later column repairs can be exercised without a phantom table.
	mustExec(t, db, `CREATE TABLE conversation_turns (id TEXT PRIMARY KEY)`)
	mustExec(t, db, `INSERT INTO conversations (id, scope, project_id, session_id, latest_sequence, created_at, updated_at)
		VALUES ('conv-1', 'session', 'p1', 'ao-1', 0, '2026-08-05T00:00:00Z', '2026-08-05T00:00:00Z')`)

	if err := reconcileSchema(db); err != nil {
		t.Fatalf("reconcile schema: %v", err)
	}

	var currentSessionID string
	if err := db.QueryRow(`SELECT current_session_id FROM conversations WHERE id = 'conv-1'`).Scan(&currentSessionID); err != nil {
		t.Fatalf("read current_session_id: %v", err)
	}
	if currentSessionID != "ao-1" {
		t.Fatalf("current_session_id = %q, want session_id backfill", currentSessionID)
	}
	if err := reconcileSchema(db); err != nil {
		t.Fatalf("repeat reconcile schema: %v", err)
	}
}

func TestMigration0066ConversationSchemaConstraints(t *testing.T) {
	db := openTestDB(t)
	upTo(t, db, 66)

	now := time.Now().UTC()
	mustExec(t, db, `INSERT INTO projects (id, path, display_name, registered_at) VALUES ('p1','/tmp/p1','proj',?)`, now)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, num, kind, activity_state, activity_last_at, is_terminated, session_mode, created_at, updated_at)
		VALUES ('ao-1','p1',1,'orchestrator','idle',?,0,'chat',?,?)`, now, now, now)
	mustExec(t, db, `INSERT INTO conversations (id, scope, project_id, session_id, current_session_id, latest_sequence, created_at, updated_at)
		VALUES ('conv-1','session','p1','ao-1','ao-1',0,?,?)`, now, now)

	t.Run("one conversation per session", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO conversations (id, scope, project_id, session_id, latest_sequence, created_at, updated_at)
			VALUES ('conv-dup','session','p1','ao-1',0,?,?)`, now, now)
		if err == nil {
			t.Fatal("a second conversation for the same session was accepted")
		}
	})

	t.Run("session scope requires a session", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO conversations (id, scope, project_id, session_id, latest_sequence, created_at, updated_at)
			VALUES ('conv-bad','session','p1',NULL,0,?,?)`, now, now)
		if err == nil {
			t.Fatal("a session-scoped conversation with no session was accepted")
		}
	})

	t.Run("project scope forbids a session", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO conversations (id, scope, project_id, session_id, latest_sequence, created_at, updated_at)
			VALUES ('conv-bad2','project','p1','ao-1',0,?,?)`, now, now)
		if err == nil {
			t.Fatal("a project-scoped conversation carrying a session was accepted")
		}
	})

	mustExec(t, db, `INSERT INTO conversation_turns (id, conversation_id, handled_by_session_id, provider_turn_id, state, requested_at)
		VALUES ('turn-1','conv-1','ao-1','provider-turn-1','running',?)`, now)

	t.Run("sequence is unique per conversation", func(t *testing.T) {
		mustExec(t, db, `INSERT INTO conversation_messages (id, conversation_id, turn_id, sequence, role, origin, text, created_at, updated_at)
			VALUES ('m1','conv-1','turn-1',1,'user','human','hello',?,?)`, now, now)
		_, err := db.Exec(`INSERT INTO conversation_messages (id, conversation_id, turn_id, sequence, role, origin, text, created_at, updated_at)
			VALUES ('m2','conv-1','turn-1',1,'assistant','provider','hi',?,?)`, now, now)
		if err == nil {
			t.Fatal("two messages claimed the same sequence")
		}
	})

	t.Run("provider item id folds deltas onto one message", func(t *testing.T) {
		mustExec(t, db, `INSERT INTO conversation_messages (id, conversation_id, turn_id, sequence, role, origin, text, provider_item_id, created_at, updated_at)
			VALUES ('m3','conv-1','turn-1',2,'assistant','provider','partial','msg_abc',?,?)`, now, now)
		_, err := db.Exec(`INSERT INTO conversation_messages (id, conversation_id, turn_id, sequence, role, origin, text, provider_item_id, created_at, updated_at)
			VALUES ('m4','conv-1','turn-1',3,'assistant','provider','dup','msg_abc',?,?)`, now, now)
		if err == nil {
			t.Fatal("the same provider item created a second message row")
		}
	})

	t.Run("a retried client message id cannot duplicate a turn", func(t *testing.T) {
		mustExec(t, db, `INSERT INTO conversation_messages (id, conversation_id, sequence, role, origin, text, client_message_id, created_at, updated_at)
			VALUES ('m5','conv-1',4,'user','human','retry me','client-7',?,?)`, now, now)
		_, err := db.Exec(`INSERT INTO conversation_messages (id, conversation_id, sequence, role, origin, text, client_message_id, created_at, updated_at)
			VALUES ('m6','conv-1',5,'user','human','retry me','client-7',?,?)`, now, now)
		if err == nil {
			t.Fatal("a duplicate client_message_id was accepted")
		}
	})

	t.Run("one activity per approval request id", func(t *testing.T) {
		mustExec(t, db, `INSERT INTO conversation_activities (id, conversation_id, turn_id, sequence, kind, status, summary, request_id, created_at, updated_at)
			VALUES ('a1','conv-1','turn-1',6,'command','pending','Run date -u','0',?,?)`, now, now)
		_, err := db.Exec(`INSERT INTO conversation_activities (id, conversation_id, turn_id, sequence, kind, status, summary, request_id, created_at, updated_at)
			VALUES ('a2','conv-1','turn-1',7,'command','pending','Run date -u','0',?,?)`, now, now)
		if err == nil {
			t.Fatal("a duplicate approval request_id was accepted")
		}
	})

	t.Run("provider events dedupe on provider event id", func(t *testing.T) {
		mustExec(t, db, `INSERT INTO conversation_provider_events (conversation_id, session_id, provider_event_id, method, payload_json, received_at)
			VALUES ('conv-1','ao-1','ev-1','item/completed','{}',?)`, now)
		_, err := db.Exec(`INSERT INTO conversation_provider_events (conversation_id, session_id, provider_event_id, method, payload_json, received_at)
			VALUES ('conv-1','ao-1','ev-1','item/completed','{}',?)`, now)
		if err == nil {
			t.Fatal("a duplicate provider_event_id was accepted")
		}
		// Events with no provider id are common and must all be retained.
		for range 2 {
			mustExec(t, db, `INSERT INTO conversation_provider_events (conversation_id, session_id, method, payload_json, received_at)
				VALUES ('conv-1','ao-1','turn/started','{}',?)`, now)
		}
	})
}

// Chat changes must reach clients through the existing trigger-backed CDC rather
// than a parallel emission path in store code.
func TestChatMigrationsEmitConversationCDC(t *testing.T) {
	db := openTestDB(t)
	// Exercise the final schema: later activity-kind migrations rebuild the table
	// and must preserve the trigger introduced with Chat mode.
	upTo(t, db, 79)

	now := time.Now().UTC()
	mustExec(t, db, `INSERT INTO projects (id, path, display_name, registered_at) VALUES ('p1','/tmp/p1','proj',?)`, now)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, num, kind, activity_state, activity_last_at, is_terminated, session_mode, created_at, updated_at)
		VALUES ('ao-1','p1',1,'orchestrator','idle',?,0,'chat',?,?)`, now, now, now)
	mustExec(t, db, `INSERT INTO conversations (id, scope, project_id, session_id, current_session_id, latest_sequence, created_at, updated_at)
		VALUES ('conv-1','session','p1','ao-1','ao-1',0,?,?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_turns (id, conversation_id, handled_by_session_id, state, requested_at)
		VALUES ('turn-1','conv-1','ao-1','running',?)`, now)

	before := countChangeLog(t, db, "session_updated")
	mustExec(t, db, `INSERT INTO conversation_messages (id, conversation_id, turn_id, sequence, role, origin, text, created_at, updated_at)
		VALUES ('m1','conv-1','turn-1',1,'assistant','provider','hi',?,?)`, now, now)
	mustExec(t, db, `INSERT INTO conversation_activities (id, conversation_id, turn_id, sequence, kind, status, summary, created_at, updated_at)
		VALUES ('a1','conv-1','turn-1',2,'command','completed','ran date',?,?)`, now, now)

	if got := countChangeLog(t, db, "session_updated"); got != before+2 {
		t.Fatalf("session_updated invalidations = %d, want %d", got, before+2)
	}
	mustExec(t, db, `UPDATE conversation_messages SET text='hi there', revision=revision+1, updated_at=? WHERE id='m1'`, now)
	mustExec(t, db, `UPDATE conversation_activities SET summary='ran date -u', revision=revision+1, updated_at=? WHERE id='a1'`, now)
	if got := countChangeLog(t, db, "session_updated"); got != before+4 {
		t.Fatalf("streamed-row invalidations = %d, want %d", got, before+4)
	}

	// A turn reaching a terminal state changes derived session activity even
	// though no timeline row was added, so it must invalidate too.
	turnsBefore := countChangeLog(t, db, "session_updated")
	mustExec(t, db, `UPDATE conversation_turns SET state='completed', completed_at=? WHERE id='turn-1'`, now)
	if got := countChangeLog(t, db, "session_updated"); got != turnsBefore+1 {
		t.Fatalf("turn-state invalidations = %d, want %d", got, turnsBefore+1)
	}

	// A no-op update must not emit: clients would refetch for nothing.
	mustExec(t, db, `UPDATE conversation_turns SET state='completed' WHERE id='turn-1'`)
	if got := countChangeLog(t, db, "session_updated"); got != turnsBefore+1 {
		t.Fatalf("an unchanged turn state emitted a CDC event (%d)", got)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func countChangeLog(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = ?`, eventType).Scan(&n); err != nil {
		t.Fatalf("count change_log %s: %v", eventType, err)
	}
	return n
}
