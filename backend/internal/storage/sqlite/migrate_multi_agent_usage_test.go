package sqlite

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestMultiAgentUsageMigrationAcceptsCertifiedSourceValues catches a migration
// that reports success without actually widening the usage CHECK constraints.
// The production change that makes this fail is dropping any certified harness
// or source kind from the rebuilt schema.
func TestMultiAgentUsageMigrationAcceptsCertifiedSourceValues(t *testing.T) {
	db := openMigratedTestDB(t)
	now := time.Unix(100, 0).UTC()
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('usage-project', '/repo/usage-project', ?, '{}');
`, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	tests := []struct {
		harness domain.AgentHarness
		kind    domain.UsageSourceKind
	}{
		{domain.HarnessCopilot, domain.UsageSourceCopilotShutdown},
		{domain.HarnessKimi, domain.UsageSourceKimiWire},
		{domain.HarnessPi, domain.UsageSourcePiSession},
		{domain.HarnessQwen, domain.UsageSourceQwenMonthly},
	}
	for index, test := range tests {
		sessionID := "usage-session-" + string(rune('a'+index))
		if _, err := db.Exec(`
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES (?, 'usage-project', ?, ?, ?, ?, ?)
`, sessionID, index+1, test.harness, now, now, now); err != nil {
			t.Fatalf("insert %s session: %v", test.harness, err)
		}
		result, err := db.Exec(`
INSERT INTO usage_bindings
    (session_id, harness, native_root_id, state, updated_at)
VALUES (?, ?, ?, 'active', ?)
`, sessionID, test.harness, "native-"+sessionID, now)
		if err != nil {
			t.Fatalf("insert %s binding: %v", test.harness, err)
		}
		bindingID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read %s binding id: %v", test.harness, err)
		}
		if _, err := db.Exec(`
INSERT INTO usage_sources
    (binding_id, kind, native_session_id, artifact_path, state, updated_at)
VALUES (?, ?, ?, ?, 'active', ?)
`, bindingID, test.kind, "native-"+sessionID, "/tmp/"+sessionID+".jsonl", now); err != nil {
			t.Fatalf("insert %s source %s: %v", test.harness, test.kind, err)
		}
	}
}

func TestMultiAgentUsageMigrationRejectsUnknownValues(t *testing.T) {
	db := openMigratedTestDB(t)
	now := time.Unix(100, 0).UTC()
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('usage-project', '/repo/usage-project', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('usage-session', 'usage-project', 1, 'codex', ?, ?, ?);
INSERT INTO usage_bindings
    (session_id, harness, native_root_id, state, updated_at)
VALUES ('usage-session', 'codex', 'native-session', 'active', ?);
`, now, now, now, now, now); err != nil {
		t.Fatalf("seed usage binding: %v", err)
	}

	assertCheckFailure(t, db, `
INSERT INTO usage_bindings
    (session_id, harness, native_root_id, state, updated_at)
VALUES ('usage-session', 'unknown-agent', 'unknown-native', 'active', ?)
`, now)
	assertCheckFailure(t, db, `
INSERT INTO usage_sources
    (binding_id, kind, native_session_id, artifact_path, state, updated_at)
VALUES (1, 'unknown_source', 'native-session', '/tmp/unknown.jsonl', 'active', ?)
`, now)
}

func assertCheckFailure(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("error = %v, want CHECK constraint failure", err)
	}
}
