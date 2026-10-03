package sqlite

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsSideChatMigrationAtOldVersion(t *testing.T) {
	for _, oldVersion := range []int{156, 163, 169, 171} {
		t.Run(fmt.Sprintf("version-%d", oldVersion), func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, int64(oldVersion-1))
			contents, err := migrationsFS.ReadFile("migrations/0174_conversation_side_chats.sql")
			if err != nil {
				t.Fatal(err)
			}
			gooseMu.Lock()
			goose.SetBaseFS(fstest.MapFS{
				fmt.Sprintf("migrations/%04d_conversation_side_chats.sql", oldVersion): &fstest.MapFile{Data: contents},
			})
			goose.SetLogger(goose.NopLogger())
			if err := goose.SetDialect("sqlite3"); err != nil {
				gooseMu.Unlock()
				t.Fatal(err)
			}
			err = goose.Up(db, "migrations", goose.WithAllowMissing())
			goose.SetBaseFS(migrationsFS)
			gooseMu.Unlock()
			if err != nil {
				t.Fatalf("apply old side-chat migration: %v", err)
			}
			if err := migrate(db); err != nil {
				t.Fatalf("migrate old side-chat database: %v", err)
			}
			for _, check := range []struct{ table, column string }{
				{"conversation_branches", "purpose"},
				{"conversation_branches", "label"},
				{"sessions", "provision_state"},
				{"sessions", "provision_error"},
				{"shell_terminals", "preview_capability_verifier"},
				{"conversation_messages", "client_payload_hash"},
			} {
				var present int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, check.table, check.column).Scan(&present); err != nil {
					t.Fatal(err)
				}
				if present != 1 {
					t.Errorf("missing %s.%s", check.table, check.column)
				}
			}
			for _, version := range []int{156, 163, 169, 170, 171, 172, 173, 174} {
				var applied int
				if err := db.QueryRow(`SELECT is_applied FROM goose_db_version WHERE version_id = ? ORDER BY id DESC LIMIT 1`, version).Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if applied != 1 {
					t.Errorf("migration %d was not applied", version)
				}
			}
			var trigger int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'report_outputs_pr_created_cdc'`).Scan(&trigger); err != nil {
				t.Fatal(err)
			}
			if trigger != 1 {
				t.Fatal("upstream reported-PR CDC migration was skipped")
			}
			var schema string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sessions'`).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(schema, "'fx'") {
				t.Fatal("upstream fx migration was skipped")
			}
		})
	}
}
