package omp

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestOMPAuthJSONStatusAuthorizedWithKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, path, `{"anthropic":{"type":"api-key","key":"sk-ant"}}`)

	status, ok, err := ompAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status=%q ok=%v, want authorized true", status, ok)
	}
}

func TestOMPAuthJSONStatusUnauthorizedWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, path, `{}`)

	status, ok, err := ompAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status=%q ok=%v, want unauthorized true", status, ok)
	}
}

func TestOMPAuthJSONStatusUnknownWhenMissing(t *testing.T) {
	status, ok, err := ompAuthJSONStatus(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status=%q ok=%v, want unknown false", status, ok)
	}
}

func TestOMPAgentDBAuthStatusAuthorizedWithEnabledCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()
	_, err = db.Exec(`
		CREATE TABLE auth_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			credential_type TEXT NOT NULL,
			data TEXT NOT NULL,
			disabled_cause TEXT DEFAULT NULL,
			identity_key TEXT DEFAULT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO auth_credentials (provider, credential_type, data, created_at, updated_at)
		VALUES ('zai', 'api_key', '{"key":"present"}', 1, 1);
	`)
	if err != nil {
		t.Fatal(err)
	}

	status, ok, err := ompAgentDBAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status=%q ok=%v, want authorized true", status, ok)
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
