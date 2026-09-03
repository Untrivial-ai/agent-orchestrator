package store_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestProviderStoreCRUDAndCiphertextOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := domain.Provider{ID: "p1", DisplayName: "Kimi mock", APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: "https://mock.invalid/anthropic", SecretRef: "provider:p1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	cipher := []byte{0x01, 0x04, 0x09, 0x10}
	if err := s.PutProviderSecret(ctx, p.SecretRef, cipher, now); err != nil {
		t.Fatal(err)
	}
	m := domain.ProviderModel{ID: "m1", ProviderID: p.ID, DisplayName: "Kimi model", ModelName: "kimi-mock", Enabled: true, SortOrder: 2, CreatedAt: now, UpdatedAt: now}
	if err := s.PutProviderModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, models, err := func() (domain.Provider, []domain.ProviderModel, error) {
		p, ok, e := s.GetProvider(ctx, "p1")
		if !ok && e == nil {
			t.Fatal("provider missing")
		}
		ms, me := s.ListProviderModels(ctx, "p1")
		if e != nil {
			return p, nil, e
		}
		return p, ms, me
	}()
	if err != nil {
		t.Fatal(err)
	}
	if !got.SecretConfigured || got.SecretRef != "provider:p1" || len(models) != 1 || models[0].ModelName != "kimi-mock" {
		t.Fatalf("unexpected records: %#v %#v", got, models)
	}
	stored, ok, err := s.GetProviderSecret(ctx, p.SecretRef)
	if err != nil || !ok || !bytes.Equal(stored, cipher) {
		t.Fatalf("ciphertext mismatch: %v %v %v", stored, ok, err)
	}
	if err := s.DeleteProviderSecret(ctx, p.SecretRef); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetProviderSecret(ctx, p.SecretRef); err != nil || ok {
		t.Fatalf("secret delete failed: ok=%v err=%v", ok, err)
	}
}
