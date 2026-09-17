package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	usagepipeline "github.com/aoagents/agent-orchestrator/backend/internal/observe/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing/catalogsync"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Break caught: ingestion could start against an empty manager while a valid
// last-known-good catalog was still being loaded asynchronously.
func TestUsagePricingRuntimePublishesLKGBeforeStartReturnsAndWaitsOnShutdown(t *testing.T) {
	dataDir := t.TempDir()
	_, err := catalogsync.Sync(dataDir, []byte(`{
  "anthropic/claude-test": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0,
    "output_cost_per_token": 0
  },
  "openai/gpt-test": {
    "litellm_provider": "openai",
    "mode": "responses",
    "input_cost_per_token": 0.000001,
    "output_cost_per_token": 0.000002
  },
  "zai/glm-test": {
    "litellm_provider": "zai",
    "mode": "chat",
    "input_cost_per_token": 0,
    "output_cost_per_token": 0
  }
}`), catalogsync.Source{
		Repository: "BerriAI/litellm",
		Revision:   "0123456789abcdef0123456789abcdef01234567",
		Path:       "model_prices_and_context_window.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := sqlitetest.MustOpenAt(t, dataDir)
	fetcher := &blockingDaemonPricingFetcher{started: make(chan struct{}), stopped: make(chan struct{})}
	runtime, err := newUsagePricingRuntime(usagePricingRuntimeConfig{
		DataDir: dataDir, Store: store, Fetcher: fetcher, Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Manager().Snapshot().ProviderVersion("openai"); got == "" {
		t.Fatal("Start returned before publishing the cached OpenAI catalog")
	}
	<-fetcher.started
	cancel()
	runtime.Wait()
	select {
	case <-fetcher.stopped:
	default:
		t.Fatal("Wait returned before the remote refresher stopped")
	}
}

// Break caught: an absent cache plus offline catalog endpoint must degrade to
// unavailable estimates, never make daemon startup fail.
func TestUsagePricingRuntimeCatalogFailureIsNonfatal(t *testing.T) {
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	fetcher := &failingDaemonPricingFetcher{called: make(chan struct{})}
	runtime, err := newUsagePricingRuntime(usagePricingRuntimeConfig{
		DataDir: dataDir, Store: store, Fetcher: fetcher, Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("offline Start: %v", err)
	}
	<-fetcher.called
	if got := runtime.Manager().Snapshot().ProviderVersion("openai"); got != "" {
		t.Fatalf("offline manager version = %q, want unavailable", got)
	}
	cancel()
	runtime.Wait()
}

// Break caught: a provider-null row whose model first appears in a later daily
// catalog needs another attribution pass; cost backfill cannot select it yet.
func TestUsagePricingRuntimeRepairsHistoryAfterLaterCatalogActivation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	runtime, err := newUsagePricingRuntime(usagePricingRuntimeConfig{
		DataDir: dataDir,
		Store:   store,
		Fetcher: &failingDaemonPricingFetcher{called: make(chan struct{})},
		Logger:  slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot := daemonClaudePricingSnapshot(t, false)
	if _, err := runtime.manager.Activate(ctx, oldSnapshot); err != nil {
		t.Fatal(err)
	}

	// Insert the late model first so the sentinel's successful attribution
	// proves the initial pass has already visited and skipped it.
	lateSourceID := seedDaemonClaudeUsage(t, store, "late", "claude-late")
	sentinelSourceID := seedDaemonClaudeUsage(t, store, "sentinel", "claude-old")
	if err := runtime.backfiller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.repairer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		runtime.repairer.Wait()
		runtime.backfiller.Wait()
	})
	waitForDaemonUsageProvider(t, store, sentinelSourceID, "anthropic")
	if provider := daemonUsageProvider(t, store, lateSourceID); provider != "" {
		t.Fatalf("late model provider before catalog activation = %q, want empty", provider)
	}

	newSnapshot := daemonClaudePricingSnapshot(t, true)
	activations, err := runtime.manager.Activate(ctx, newSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runtime.onCatalogActivation(ctx, activations)
	waitForDaemonUsageProvider(t, store, lateSourceID, "anthropic")
}

func seedDaemonClaudeUsage(t *testing.T, store *sqlite.Store, projectID, model string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: projectID, Path: t.TempDir(), RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(projectID),
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:    session.ID,
		Harness:      domain.HarnessClaudeCode,
		NativeRootID: projectID + "-root",
		State:        domain.UsageBindingActive,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	transcript := `{"type":"assistant","uuid":"one","message":{"id":"msg-1","model":"` + model + `","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := usagesvc.SourceIdentity(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceClaudeMain,
		NativeSessionID: projectID + "-root",
		ArtifactPath:    path,
		FileIdentity:    identity,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ingestor := usagepipeline.NewIngestor(store, usagepipeline.IngestorConfig{Clock: func() time.Time { return now }})
	for {
		result, err := ingestor.Ingest(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !result.More {
			break
		}
	}
	return source.ID
}

func daemonUsageProvider(t *testing.T, store *sqlite.Store, sourceID int64) string {
	t.Helper()
	events, err := store.ListLegacyUsageEvents(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("legacy events for source %d = %d, want 1", sourceID, len(events))
	}
	return events[0].BillingProviderID
}

func waitForDaemonUsageProvider(t *testing.T, store *sqlite.Store, sourceID int64, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := daemonUsageProvider(t, store, sourceID); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("source %d billing provider never became %q", sourceID, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func daemonClaudePricingSnapshot(t *testing.T, includeLate bool) *pricing.Snapshot {
	t.Helper()
	late := ""
	if includeLate {
		late = `,
  "anthropic/claude-late": {"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}`
	}
	root := t.TempDir()
	upstream := []byte(`{
  "anthropic/claude-old": {"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}` + late + `,
  "openai/gpt-test": {"litellm_provider":"openai","mode":"chat","input_cost_per_token":0,"output_cost_per_token":0},
  "zai/glm-test": {"litellm_provider":"zai","mode":"chat","input_cost_per_token":0,"output_cost_per_token":0}
}`)
	if _, err := catalogsync.Sync(root, upstream, catalogsync.Source{
		Repository: "BerriAI/litellm",
		Revision:   "0123456789abcdef0123456789abcdef01234567",
		Path:       "model_prices_and_context_window.json",
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := pricing.NewCache(root).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return catalog.Snapshot()
}

type blockingDaemonPricingFetcher struct {
	started chan struct{}
	stopped chan struct{}
}

func (f *blockingDaemonPricingFetcher) Fetch(ctx context.Context, _ string, _ bool) (pricing.FetchResult, error) {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return pricing.FetchResult{}, ctx.Err()
}

type failingDaemonPricingFetcher struct {
	called chan struct{}
}

func (f *failingDaemonPricingFetcher) Fetch(context.Context, string, bool) (pricing.FetchResult, error) {
	close(f.called)
	return pricing.FetchResult{}, errors.New("offline")
}
