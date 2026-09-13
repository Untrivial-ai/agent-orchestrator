package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRevisionCoversEveryWrite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	read := func(query string) int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec(`INSERT INTO projects (id, path, registered_at) VALUES ('rev', '/rev', CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO sessions (id, project_id, num, activity_last_at, created_at, updated_at)
		VALUES ('rev-1', 'rev', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	for _, recursive := range []string{"OFF", "ON"} {
		t.Run(recursive, func(t *testing.T) {
			exec("PRAGMA recursive_triggers = " + recursive)
			for _, query := range []string{
				`UPDATE sessions SET updated_at = updated_at WHERE id = 'rev-1'`,
				`UPDATE sessions SET session_permissions = 'acceptEdits' WHERE id = 'rev-1'`,
				`UPDATE sessions SET latest_user_prompt = 'new prompt' WHERE id = 'rev-1'`,
			} {
				before := read(`SELECT revision FROM sessions WHERE id = 'rev-1'`)
				exec(query)
				if got := read(`SELECT revision FROM sessions WHERE id = 'rev-1'`); got != before+1 {
					t.Fatalf("revision = %d, want %d after %s", got, before+1, query)
				}
			}
			before := read(`SELECT revision FROM sessions WHERE id = 'rev-1'`)
			changes := read(`SELECT COUNT(*) FROM change_log`)
			exec(`UPDATE sessions SET display_name = display_name || 'x' WHERE id = 'rev-1'`)
			if got := read(`SELECT COUNT(*) FROM change_log`); got != changes+1 {
				t.Fatalf("one update emitted %d CDC events, want 1", got-changes)
			}
			exec(`BEGIN`)
			exec(`UPDATE sessions SET display_name = 'rolled back' WHERE id = 'rev-1'`)
			exec(`ROLLBACK`)
			if got := read(`SELECT revision FROM sessions WHERE id = 'rev-1'`); got != before+1 {
				t.Fatalf("rollback changed revision: %d, want %d", got, before+1)
			}
		})
	}
	exec(`DROP TRIGGER sessions_revision_update`)
	if err := reconcileSchema(db); err == nil || !strings.Contains(err.Error(), "sessions_revision_update") {
		t.Fatalf("missing revision trigger silently admitted at startup: %v", err)
	}
}
