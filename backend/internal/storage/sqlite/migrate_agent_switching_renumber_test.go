package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsLegacyAgentSwitchSchemas(t *testing.T) {
	tests := []struct {
		name        string
		migrateTo   int64
		switchPath  string
		handoffPath string
	}{
		{
			name:        "versions 0080 and 0081",
			migrateTo:   79,
			switchPath:  "migrations/0080_agent_switching.sql",
			handoffPath: "migrations/0081_finalized_agent_handoff.sql",
		},
		{
			name:        "versions 0081 and 0082",
			migrateTo:   80,
			switchPath:  "migrations/0081_agent_switching.sql",
			handoffPath: "migrations/0082_finalized_agent_handoff.sql",
		},
		{
			name:        "pre-consolidation versions 0083 and 0084",
			migrateTo:   82,
			switchPath:  "migrations/0083_agent_switching.sql",
			handoffPath: "migrations/0084_finalized_agent_handoff.sql",
		},
		{
			name:       "pre-consolidation base without final handoff migration",
			migrateTo:  82,
			switchPath: "migrations/0083_agent_switching.sql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openAgentSwitchMigrationTestDB(t)
			upTo(t, db, tt.migrateTo)
			applyLegacyAgentSwitchMigrations(t, db, tt.switchPath, tt.handoffPath)
			assertAgentSwitchMigrationHistoryRepaired(t, db)
		})
	}
}

func openAgentSwitchMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyLegacyAgentSwitchMigrations(t *testing.T, db *sql.DB, switchPath, handoffPath string) {
	t.Helper()
	agentSwitchMigration, err := migrationsFS.ReadFile("migrations/0085_agent_switching.sql")
	if err != nil {
		t.Fatalf("read agent-switch migration: %v", err)
	}
	normalizedAgentSwitchMigration := strings.ReplaceAll(string(agentSwitchMigration), "\r\n", "\n")
	// Reconstruct the exact pre-consolidation table shape. The historical 0084
	// below then adds only the two finalized-handoff columns, leaving transcript
	// status for the compatibility repair to add.
	legacyAgentSwitchMigration := strings.ReplaceAll(normalizedAgentSwitchMigration, `    source_transcript_status   TEXT NOT NULL DEFAULT 'not_attempted'
        CHECK (source_transcript_status IN ('not_attempted', 'available', 'unavailable')),
`, "")
	legacyAgentSwitchMigration = strings.ReplaceAll(legacyAgentSwitchMigration, `    semantic_handoff_included INTEGER NOT NULL DEFAULT 0
        CHECK (semantic_handoff_included IN (0, 1)),
`, "")
	legacyAgentSwitchMigration = strings.ReplaceAll(legacyAgentSwitchMigration, `    final_handoff_path         TEXT NOT NULL DEFAULT '',
    final_handoff_hash         TEXT NOT NULL DEFAULT '',
`, "")
	legacyAgentSwitchMigration = strings.ReplaceAll(
		legacyAgentSwitchMigration,
		"    OR OLD.auto_inject_review <> NEW.auto_inject_review\n",
		"",
	)
	legacyAgentSwitchMigration = strings.ReplaceAll(
		legacyAgentSwitchMigration,
		`            'mode', NEW.session_mode,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END)
`,
		`            'mode', NEW.session_mode
`,
	)
	if legacyAgentSwitchMigration == normalizedAgentSwitchMigration ||
		strings.Contains(legacyAgentSwitchMigration, "source_transcript_status") ||
		strings.Contains(legacyAgentSwitchMigration, "semantic_handoff_included") ||
		strings.Contains(legacyAgentSwitchMigration, "final_handoff_path") ||
		strings.Contains(legacyAgentSwitchMigration, "final_handoff_hash") ||
		strings.Contains(legacyAgentSwitchMigration, "auto_inject_review") {
		t.Fatal("pre-consolidation fixture did not remove the consolidated agent-switch columns")
	}
	legacyFS := fstest.MapFS{
		switchPath: {Data: []byte(legacyAgentSwitchMigration)},
	}
	if handoffPath != "" {
		legacyFS[handoffPath] = &fstest.MapFile{Data: []byte(preConsolidationFinalHandoffMigration)}
	}
	goose.SetBaseFS(legacyFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatalf("apply legacy agent-switch migrations: %v", err)
	}
}

func assertAgentSwitchMigrationHistoryRepaired(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrate(db); err != nil {
		t.Fatalf("migrate legacy agent-switch database: %v", err)
	}
	for _, version := range []int64{80, 81, 82, 83, 84, 85} {
		var applied int
		if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied = %d, want 1", version, applied)
		}
	}
	if got, err := reviewHasSessionHarnessUnique(db); err != nil || !got {
		t.Fatalf("review per-harness shape = %v, err = %v", got, err)
	}
	for table, column := range map[string]string{
		"sessions.browser_verifier":        "browser_capability_verifier",
		"sessions.auto_inject_review":      "auto_inject_review",
		"review_run.auto_inject_review":    "auto_inject_review",
		"pr_reviews.auto_inject_review":    "auto_inject_review",
		"pr_comment.auto_inject_review":    "auto_inject_review",
		"agent_switches.final_path":        "final_handoff_path",
		"agent_switches.final_hash":        "final_handoff_hash",
		"agent_switches.transcript_status": "source_transcript_status",
		"agent_switches.semantic_included": "semantic_handoff_included",
	} {
		tableName := strings.SplitN(table, ".", 2)[0]
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tableName, column,
		).Scan(&count); err != nil {
			t.Fatalf("read %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Fatalf("%s.%s count = %d, want 1", table, column, count)
		}
	}
	var reconciledHarnessShape int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table'
  AND name = 'sessions'
  AND instr(COALESCE(sql, ''), '''kimchi''') > 0
  AND instr(COALESCE(sql, ''), '''prime-agent''') > 0`).Scan(&reconciledHarnessShape); err != nil {
		t.Fatalf("read reconciled Kimchi/Prime Agent session shape: %v", err)
	}
	if reconciledHarnessShape != 1 {
		t.Fatalf("reconciled Kimchi/Prime Agent session shape = %d, want 1", reconciledHarnessShape)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
}

const preConsolidationFinalHandoffMigration = `
-- +goose Up
-- +goose StatementBegin
ALTER TABLE agent_switches ADD COLUMN final_handoff_path TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE agent_switches ADD COLUMN final_handoff_hash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_switches DROP COLUMN final_handoff_hash;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE agent_switches DROP COLUMN final_handoff_path;
-- +goose StatementEnd
`
