//go:build windows

package provider_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/providersecret"
	providersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/provider"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSQLiteContainsOnlyDPAPICiphertext(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	svc := providersvc.New(store, providersecret.New(), nil)
	key := "phase19b2-dpapi-sqlite-sentinel"
	p, err := svc.Create(ctx, providersvc.ProviderInput{DisplayName: "DPAPI test", APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: "https://provider.invalid/anthropic", Enabled: true, APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, ok, err := store.GetProviderSecret(ctx, "provider:"+string(p.ID))
	if err != nil || !ok {
		t.Fatalf("stored secret: ok=%v err=%v", ok, err)
	}
	if bytes.Contains(ciphertext, []byte(key)) {
		t.Fatal("SQLite provider secret contains plaintext")
	}
	m, err := svc.PutModel(ctx, p.ID, "", providersvc.ModelInput{DisplayName: "Model", ModelName: "model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.ResolveRuntimeProvider(ctx, p.ID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != key {
		t.Fatal("resolved DPAPI secret mismatch")
	}
}
