package daemon

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing/catalogsync"
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
