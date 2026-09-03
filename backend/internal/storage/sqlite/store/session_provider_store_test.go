package store_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionProviderSelectionSurvivesStoreRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "provider-session")
	rec := sampleRecord("provider-session")
	rec.Metadata.ProviderID = domain.ProviderID("provider-deepseek")
	rec.Metadata.ProviderModelID = domain.ProviderModelID("model-deepseek-v4-pro")
	rec.Metadata.ProviderDisplayName = "DeepSeek"
	rec.Metadata.ProviderModelName = "deepseek-v4-pro"
	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	created.Metadata.RuntimeHandleID = "runtime-1"
	if err := s.UpdateSession(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if got.Metadata.ProviderID != rec.Metadata.ProviderID ||
		got.Metadata.ProviderModelID != rec.Metadata.ProviderModelID ||
		got.Metadata.ProviderDisplayName != rec.Metadata.ProviderDisplayName ||
		got.Metadata.ProviderModelName != rec.Metadata.ProviderModelName {
		t.Fatalf("Provider selection did not round-trip: %+v", got.Metadata)
	}
}
