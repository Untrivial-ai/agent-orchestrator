package store_test

import (
	"context"
	"testing"
	"time"
)

func TestAgentInventoryCacheStoreMissing(t *testing.T) {
	s := newTestStore(t)

	inventoryJSON, observedAt, ok, err := s.GetAgentInventoryCache(context.Background())
	if err != nil {
		t.Fatalf("get missing agent inventory cache: %v", err)
	}
	if ok {
		t.Fatalf("get missing agent inventory cache: ok = true, inventory = %q, observed at = %v", inventoryJSON, observedAt)
	}
}

func TestAgentInventoryCacheStoreRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wantJSON := `{"installed":[{"id":"codex"}],"authorized":[]}`
	wantObservedAt := time.Date(2026, time.August, 21, 12, 34, 56, 0, time.FixedZone("UTC+05:30", 5*60*60+30*60))

	if err := s.UpsertAgentInventoryCache(ctx, wantJSON, wantObservedAt); err != nil {
		t.Fatalf("upsert agent inventory cache: %v", err)
	}
	gotJSON, gotObservedAt, ok, err := s.GetAgentInventoryCache(ctx)
	if err != nil {
		t.Fatalf("get agent inventory cache: %v", err)
	}
	if !ok {
		t.Fatal("get agent inventory cache: ok = false")
	}
	if gotJSON != wantJSON {
		t.Fatalf("inventory JSON = %q, want %q", gotJSON, wantJSON)
	}
	if !gotObservedAt.Equal(wantObservedAt.UTC()) || gotObservedAt.Location() != time.UTC {
		t.Fatalf("observed at = %v (%v), want %v (UTC)", gotObservedAt, gotObservedAt.Location(), wantObservedAt.UTC())
	}
}

func TestAgentInventoryCacheStoreUpsertReplacesSingleton(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	firstObservedAt := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	wantObservedAt := firstObservedAt.Add(time.Hour)
	wantJSON := `{"installed":[{"id":"claude-code"}],"authorized":[{"id":"claude-code"}]}`

	if err := s.UpsertAgentInventoryCache(ctx, `{"installed":[],"authorized":[]}`, firstObservedAt); err != nil {
		t.Fatalf("insert first agent inventory cache: %v", err)
	}
	if err := s.UpsertAgentInventoryCache(ctx, wantJSON, wantObservedAt); err != nil {
		t.Fatalf("replace agent inventory cache: %v", err)
	}
	gotJSON, gotObservedAt, ok, err := s.GetAgentInventoryCache(ctx)
	if err != nil {
		t.Fatalf("get replaced agent inventory cache: %v", err)
	}
	if !ok {
		t.Fatal("get replaced agent inventory cache: ok = false")
	}
	if gotJSON != wantJSON || !gotObservedAt.Equal(wantObservedAt) {
		t.Fatalf("replaced cache = (%q, %v), want (%q, %v)", gotJSON, gotObservedAt, wantJSON, wantObservedAt)
	}
}
