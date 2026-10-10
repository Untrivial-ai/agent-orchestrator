package zcode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRestoreRequiresExactExistingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, time_archived INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session VALUES (?, ?, NULL), (?, ?, 1)", "sess_exact", workspace, "sess_archived", workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE message (id TEXT, session_id TEXT, data TEXT);
CREATE TABLE part (message_id TEXT, session_id TEXT, data TEXT);
INSERT INTO message VALUES ('msg_1', 'sess_exact', '{"role":"user"}');
INSERT INTO part VALUES ('msg_1', 'sess_exact', '{"type":"text","text":"original task"}');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session VALUES ('sess_empty', ?, NULL)", workspace); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ZCODE_SESSION_DB_PATH": path, "ZCODE_SESSION_DB": ""}
	for _, tc := range []struct {
		name, id, workspace string
		wantError           bool
	}{
		{"exact", "sess_exact", workspace, false},
		{"prefix", "sess_ex", workspace, true},
		{"missing", "sess_missing", workspace, true},
		{"lost history", "sess_empty", workspace, true},
		{"archived", "sess_archived", workspace, true},
		{"foreign workspace", "sess_exact", t.TempDir(), true},
		{"absent workspace", "sess_exact", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNativeRestore(context.Background(), tc.workspace, tc.id, env)
			if (err != nil) != tc.wantError {
				t.Fatalf("restore error=%v", err)
			}
		})
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(workspace, alias); err == nil {
		if err := validateNativeRestore(context.Background(), alias, "sess_exact", env); err != nil {
			t.Fatal(err)
		}
	}
	absentDB := filepath.Join(t.TempDir(), "absent.db")
	env["ZCODE_SESSION_DB_PATH"] = absentDB
	if err := validateNativeRestore(context.Background(), workspace, "sess_exact", env); err == nil {
		t.Fatal("missing native store accepted")
	}
	if _, err := os.Stat(absentDB); !os.IsNotExist(err) {
		t.Fatal("read-only probe created native storage")
	}
}

func TestNativeSessionDatabaseConfigurationPrecedence(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ZCODE_SESSION_DB_PATH", "")
	t.Setenv("ZCODE_SESSION_DB", "")
	if err := os.MkdirAll(filepath.Join(home, ".zcode", "cli"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zcode", "cli", "config.json"), []byte(`{"storage":{"sessionDbPath":"user.db"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "zcode.json"), []byte(`{"storage":{"sessionDbPath":"project.db"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := nativeSessionDB(workspace, nil)
	if err != nil || path != filepath.Join(workspace, "project.db") {
		t.Fatalf("path=%q error=%v", path, err)
	}
	path, err = nativeSessionDB(workspace, map[string]string{"ZCODE_SESSION_DB_PATH": "~/custom.db"})
	if err != nil || path != filepath.Join(home, "custom.db") {
		t.Fatalf("path=%q error=%v", path, err)
	}
}
