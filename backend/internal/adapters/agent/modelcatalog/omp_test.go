package modelcatalog

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type ompCredentialRow struct {
	provider      string
	data          string
	disabledCause any
}

func writeOMPAuthDB(t *testing.T, dir string, rows []ompCredentialRow) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatalf("open agent.db: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close agent.db: %v", err)
		}
	}()
	if _, err := db.Exec(`CREATE TABLE auth_credentials (
		id INTEGER PRIMARY KEY,
		provider TEXT NOT NULL,
		credential_type TEXT NOT NULL,
		data TEXT NOT NULL,
		disabled_cause TEXT
	)`); err != nil {
		t.Fatalf("create auth_credentials: %v", err)
	}
	for _, row := range rows {
		if _, err := db.Exec(
			`INSERT INTO auth_credentials (provider, credential_type, data, disabled_cause) VALUES (?, 'api_key', ?, ?)`,
			row.provider, row.data, row.disabledCause,
		); err != nil {
			t.Fatalf("insert %s credential: %v", row.provider, err)
		}
	}
}

func writeOMPAuthJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

func isolatedOMPEnv(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	return dir, map[string]string{"PI_CODING_AGENT_DIR": dir}
}

func providerSet(providers map[string]bool) []string {
	var out []string
	for provider := range providers {
		out = append(out, provider)
	}
	return out
}

func TestOMPConfiguredProvidersFromAgentDB(t *testing.T) {
	dir, env := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dir, []ompCredentialRow{
		{provider: "kimi-code", data: `{"key":"sk-one"}`},
		{provider: "moonshot", data: `{"key":"sk-two"}`, disabledCause: "quota"},
		{provider: "blank", data: ""},
	})

	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || !authoritative {
		t.Fatalf("ompConfiguredProviders = (%v, %v, %v), want authoritative result", providers, authoritative, err)
	}
	got := providerSet(providers)
	if len(got) != 1 || got[0] != "kimi-code" {
		t.Fatalf("providers = %v, want only [kimi-code]; disabled and empty credentials must not count", got)
	}
}

func TestOMPConfiguredProvidersMergesAuthJSON(t *testing.T) {
	dir, env := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dir, []ompCredentialRow{{provider: "kimi-code", data: `{"key":"sk-one"}`}})
	writeOMPAuthJSON(t, dir, `{"vercel-ai-gateway": {"type": "api_key", "key": "vg-two"}}`)

	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || !authoritative {
		t.Fatalf("ompConfiguredProviders = (%v, %v, %v), want authoritative result", providers, authoritative, err)
	}
	if !providers["kimi-code"] || !providers["vercel-ai-gateway"] || len(providers) != 2 {
		t.Fatalf("providers = %v, want both stores merged", providers)
	}
}

func TestOMPConfiguredProvidersFromAuthJSONOnly(t *testing.T) {
	dir, env := isolatedOMPEnv(t)
	writeOMPAuthJSON(t, dir, `{
		"vercel-ai-gateway": {"type": "api_key", "key": "vg-one"},
		"keyless": {"type": "oauth", "key": ""}
	}`)

	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || !authoritative {
		t.Fatalf("ompConfiguredProviders = (%v, %v, %v), want authoritative result", providers, authoritative, err)
	}
	got := providerSet(providers)
	if len(got) != 1 || got[0] != "vercel-ai-gateway" {
		t.Fatalf("providers = %v, want only [vercel-ai-gateway]; keyless entries must not count", got)
	}
}

func TestOMPConfiguredProvidersWithoutStoresIsNotAuthoritative(t *testing.T) {
	_, env := isolatedOMPEnv(t)
	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || authoritative || len(providers) != 0 {
		t.Fatalf("ompConfiguredProviders = (%v, %v, %v), want non-authoritative empty result", providers, authoritative, err)
	}
}

func TestOMPConfiguredProvidersEmptyDBIsNotDecisive(t *testing.T) {
	dir, env := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dir, nil)

	_, authoritative, err := ompConfiguredProviders(env)
	if err != nil {
		t.Fatalf("ompConfiguredProviders error = %v", err)
	}
	if authoritative {
		t.Fatal("empty credential store must not be authoritative; env-var-only setups would lose every model")
	}
}

func TestOMPConfiguredProvidersAllDisabledIsDecisive(t *testing.T) {
	dir, env := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dir, []ompCredentialRow{
		{provider: "moonshot", data: `{"key":"sk-one"}`, disabledCause: "quota"},
	})

	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || !authoritative {
		t.Fatalf("ompConfiguredProviders = (%v, %v, %v), want authoritative result", providers, authoritative, err)
	}
	if len(providers) != 0 {
		t.Fatalf("providers = %v, want empty; every credential is disabled", providers)
	}
}

func TestFilterModelsByConfiguredProviders(t *testing.T) {
	models := []ports.AgentModelInfo{
		{ID: "kimi-code/k3", Provider: "kimi-code"},
		{ID: "moonshot/k2", Provider: "moonshot"},
		{ID: "unattributed"},
	}
	got := filterModelsByConfiguredProviders(models, map[string]bool{"kimi-code": true})
	if len(got) != 2 || got[0].ID != "kimi-code/k3" || got[1].ID != "unattributed" {
		t.Fatalf("filtered = %v, want configured provider plus unattributed entries", got)
	}
	if out := filterModelsByConfiguredProviders(models, map[string]bool{}); out != nil {
		t.Fatalf("empty provider set filtered = %v, want nil", out)
	}
}

func TestOMPProvidersFingerprintTracksProviderSet(t *testing.T) {
	dirA, envA := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dirA, []ompCredentialRow{{provider: "kimi-code", data: `{"key":"sk-one"}`}})
	dirB, envB := isolatedOMPEnv(t)
	writeOMPAuthDB(t, dirB, []ompCredentialRow{
		{provider: "kimi-code", data: `{"key":"sk-one"}`},
		{provider: "vercel-ai-gateway", data: `{"key":"vg-two"}`},
	})
	dirEmpty, envEmpty := isolatedOMPEnv(t)
	_ = dirEmpty

	fpA := ompProvidersFingerprint(envA)
	if fpA == "" {
		t.Fatal("fingerprint is empty for a decisive store; credential changes would never invalidate the cache")
	}
	if fpA != ompProvidersFingerprint(envA) {
		t.Fatal("fingerprint is not stable for an unchanged provider set")
	}
	if fpA == ompProvidersFingerprint(envB) {
		t.Fatal("fingerprint did not change when a provider credential was added")
	}
	if got := ompProvidersFingerprint(envEmpty); got != "" {
		t.Fatalf("fingerprint = %q for a host without credential stores, want empty", got)
	}
}

func TestOMPDiscoveryRequiresBinary(t *testing.T) {
	_, err := Discover(context.Background(), "omp", "", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "agent binary is not installed") {
		t.Fatalf("Discover(omp, no binary) error = %v, want binary-missing error from the omp path", err)
	}
}
