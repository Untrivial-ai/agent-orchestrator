package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestShellTerminalLifetimeMigrationPreservesLegacyShell(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 127)
	if _, err := db.Exec(`INSERT INTO shell_terminals (handle_id, working_dir, title, app_run_id, created_at) VALUES ('legacy-shell', '/worktree', 'Build', 'previous-launch', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 128)
	var handle, title, dir string
	if err := db.QueryRow(`SELECT handle_id, title, working_dir FROM shell_terminals WHERE transient = FALSE OR app_run_id = 'next-launch'`).Scan(&handle, &title, &dir); err != nil {
		t.Fatal(err)
	}
	if handle != "legacy-shell" || title != "Build" || dir != "/worktree" {
		t.Fatalf("legacy shell changed: %q %q %q", handle, title, dir)
	}
}
