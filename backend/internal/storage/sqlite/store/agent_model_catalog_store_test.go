package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestListAgentModelCatalogsByAgentReturnsOnlyRequestedScopes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, record := range []ports.CachedAgentModelCatalog{
		{AgentID: "claude-code", ProjectID: "project-b", CatalogJSON: `{}`, FetchedAt: now},
		{AgentID: "claude-code", ProjectID: "project-a", CatalogJSON: `{}`, FetchedAt: now},
		{AgentID: "muse", ProjectID: "project-c", CatalogJSON: `{}`, FetchedAt: now},
	} {
		if err := store.UpsertAgentModelCatalog(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	records, err := store.ListAgentModelCatalogsByAgent(ctx, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ProjectID != "project-a" || records[1].ProjectID != "project-b" {
		t.Fatalf("records = %#v", records)
	}
}

func TestAgentModelCatalogPersistsRefreshMetadataAndRejectsStaleGeneration(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	newer := ports.CachedAgentModelCatalog{
		AgentID: "codex", ProjectID: "project-a", BinaryVersion: "fp-2",
		CatalogJSON: `{"models":[{"id":"new"}]}`, Source: "app-server", FetchedAt: now,
		MetadataJSON: `{"binary":"/bin/codex"}`, InputFingerprint: "fp-2", LastSuccessAt: now,
		RefreshState: "error", RefreshError: "offline", RetryCount: 2,
		RetryAt: now.Add(time.Minute), Generation: 20,
	}
	if err := store.UpsertAgentModelCatalog(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older := newer
	older.CatalogJSON = `{"models":[{"id":"old"}]}`
	older.Generation = 19
	if err := store.UpsertAgentModelCatalog(ctx, older); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetAgentModelCatalog(ctx, "codex", "project-a")
	if err != nil || !ok {
		t.Fatalf("get = (%#v, %v, %v)", got, ok, err)
	}
	if got.Generation != 20 || got.CatalogJSON != newer.CatalogJSON || got.InputFingerprint != "fp-2" || got.LastSuccessAt != now || got.RefreshError != "offline" {
		t.Fatalf("catalog = %#v, want newer generation and metadata", got)
	}
	all, err := store.ListAgentModelCatalogs(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list all = (%#v, %v)", all, err)
	}
}

func TestAgentModelCatalogWriteEmitsProjectScopedDatabaseChange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-a", Path: t.TempDir(), RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAgentModelCatalog(ctx, ports.CachedAgentModelCatalog{AgentID: "codex", ProjectID: "project-a", CatalogJSON: `{}`, FetchedAt: now}); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != cdc.EventSessionUpdated || events[0].ProjectID != "project-a" || string(events[0].Payload) != `{"kind":"model_catalog","agentId":"codex","projectId":"project-a"}` {
		t.Fatalf("events = %#v", events)
	}
}
