package kilocode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestKilocodeLocalAuthStatusAuthorizedWithProviderEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")

	status, ok, err := kilocodeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKilocodeLocalAuthStatusAuthorizedWithNVIDIAEnv(t *testing.T) {
	for _, name := range kilocodeAPIKeyEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("NVIDIA_API_KEY", "test-only-placeholder")

	status, ok, err := kilocodeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKilocodeAuthListStatusAuthorizedWithEnvironmentVariable(t *testing.T) {
	output := `
log stream error: EPERM: operation not permitted
Credentials ~/.local/share/kilo/auth.json
0 credentials

Environment
OpenAI OPENAI_API_KEY
1 environment variable
`
	status, ok := kilocodeAuthListStatus(output)
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKilocodeAuthListStatusAuthorizedWithCredentials(t *testing.T) {
	status, ok := kilocodeAuthListStatus("2 credentials\n0 environment variables")
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKilocodeAuthJSONStatusAuthorizedWithProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"zai":{"type":"api","key":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	authorized, known, err := kilocodeAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !known || !authorized {
		t.Fatalf("authorized, known = %v, %v; want true, true", authorized, known)
	}
}

func TestKilocodeAuthJSONStatusUnknownWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"zai":{"type":"api","key":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	authorized, known, err := kilocodeAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !known || authorized {
		t.Fatalf("authorized, known = %v, %v; want false, true", authorized, known)
	}
}

func TestKilocodeDBHasAuthorizedAccount(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE account (
			id text PRIMARY KEY,
			email text NOT NULL,
			url text NOT NULL,
			access_token text NOT NULL,
			refresh_token text NOT NULL,
			token_expiry integer,
			time_created integer NOT NULL,
			time_updated integer NOT NULL
		);
		CREATE TABLE account_state (
			id integer PRIMARY KEY NOT NULL,
			active_account_id text,
			active_org_id text
		);
		INSERT INTO account (id, email, url, access_token, refresh_token, time_created, time_updated)
		VALUES ('acct_1', 'user@example.com', 'https://kilo.ai', 'token', 'refresh', 1, 1);
		INSERT INTO account_state (id, active_account_id) VALUES (1, 'acct_1');
	`); err != nil {
		t.Fatal(err)
	}

	authorized, known, err := kilocodeDBHasAuthorizedAccount(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !known || !authorized {
		t.Fatalf("authorized, known = %v, %v; want true, true", authorized, known)
	}
}

func TestAuthStatusUnknownWhenKeyOnlyComesFromInteractiveShell(t *testing.T) {
	dir := t.TempDir()
	shellPath := filepath.Join(dir, "fake-shell")
	if err := os.WriteFile(shellPath, []byte(`#!/bin/sh
/usr/bin/touch "$AO_SHELL_PROBE_MARKER"
if [ "$1" = "-ic" ]; then
	OPENAI_API_KEY=from-shell /bin/sh -c "$2"
fi
`), 0o755); err != nil {
		t.Fatal(err)
	}
	kilocodePath := filepath.Join(dir, "kilocode")
	if err := os.WriteFile(kilocodePath, []byte(`#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "list" ]; then
	printf 'auth status unavailable\n'
	exit 1
fi
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dir, "shell-probe-marker")

	t.Setenv("SHELL", shellPath)
	t.Setenv("PATH", dir)
	t.Setenv("KILO_DATA_DIR", filepath.Join(dir, "missing-kilo-data"))
	t.Setenv("AO_SHELL_PROBE_MARKER", markerPath)
	for _, name := range kilocodeAPIKeyEnvVars {
		t.Setenv(name, "")
	}

	status, err := (&Plugin{resolvedBinary: kilocodePath}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("interactive shell probe ran; marker stat error = %v", err)
	}
}

func TestKilocodeAuthListStatusUnknownWhenEmpty(t *testing.T) {
	status, ok := kilocodeAuthListStatus("0 credentials\n0 environment variables")
	if !ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusUnknown)
	}
}
