package postgres

import (
	"strings"
	"testing"
)

func TestSessionBranchTipMigrationContract(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/00056_session_branch_tip.sql")
	if err != nil {
		t.Fatalf("read session branch tip migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"ALTER TABLE ao_session_transcripts",
		"ADD COLUMN session_branch_tip TEXT NOT NULL DEFAULT ''",
		"DROP COLUMN IF EXISTS session_branch_tip",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("session branch tip migration missing %q", required)
		}
	}
}
