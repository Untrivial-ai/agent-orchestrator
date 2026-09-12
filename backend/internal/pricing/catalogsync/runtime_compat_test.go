package catalogsync_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing/catalogsync"
)

// Break caught: the generator accepted fractional trailing zeroes that the
// runtime's stricter canonical decimal decoder rejected.
func TestGeneratedFractionalRatesLoadInRuntime(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.0010,"output_cost_per_token":1.2300e-3},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	_, err := catalogsync.Sync(root, upstream, catalogsync.Source{
		Repository: "BerriAI/litellm",
		Revision:   "0123456789abcdef0123456789abcdef01234567",
		Path:       "model_prices_and_context_window.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pricing.NewCache(root).Load(t.Context())
	if err != nil {
		t.Fatalf("runtime load generated catalog: %v", err)
	}
	one, zero := int64(1), int64(0)
	estimate, err := catalog.Snapshot().Estimate(domain.ModelUsageEvent{
		ProviderID:        domain.UsageProviderOpenAI,
		BillingProviderID: "openai",
		ModelID:           "o",
		MeasurementKind:   domain.UsageMeasurementNativeReported,
		Tokens: domain.UsageTokenMetrics{
			InputTokens: &one, CachedInputTokens: &zero, UncachedInputTokens: &one, OutputTokens: &one,
		},
	})
	if err != nil {
		t.Fatalf("runtime estimate generated rates: %v", err)
	}
	if estimate.TotalNanos == nil || *estimate.TotalNanos != 2_230_000 {
		t.Fatalf("runtime total = %v, want 2230000 nano-USD", estimate.TotalNanos)
	}
}

// TestGeneratedCatalogLoadsThroughRuntimeFetcherAndCache exercises the second
// boundary of the catalog supply chain end to end:
//
//	reviewed GitHub catalog -> runtime fetcher -> local last-known-good cache
//
// The generator's own validation cannot see this boundary, so a blob the
// generator happily writes can still be unreachable or unloadable at runtime —
// a wrong manifest path, an origin the fetcher refuses, a provider version the
// cache round-trips incorrectly. Serving the exact generated directory over
// httptest and installing the fetched result closes that gap without any
// production endpoint or external network request.
//
// Set AO_PRICING_CATALOG_ROOT to run the same contract against a catalog
// generated from a real pinned LiteLLM revision in CI; unset, the test
// generates its own deterministic fixture.
func TestGeneratedCatalogLoadsThroughRuntimeFetcherAndCache(t *testing.T) {
	root := os.Getenv("AO_PRICING_CATALOG_ROOT")
	if root == "" {
		root = t.TempDir()
		upstream := []byte(`{
"anthropic/claude-sonnet-4":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"cache_read_input_token_cost":0.0000003,"cache_creation_input_token_cost":0.00000375},
"openai/gpt-5":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.00000125,"output_cost_per_token":0.00001},
"zai/glm-4.6":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":0.0000006,"output_cost_per_token":0.0000022}
}`)
		if _, err := catalogsync.Sync(root, upstream, catalogsync.Source{
			Repository: "BerriAI/litellm",
			Revision:   "0123456789abcdef0123456789abcdef01234567",
			Path:       "model_prices_and_context_window.json",
		}); err != nil {
			t.Fatalf("generate catalog: %v", err)
		}
	}

	catalogDir := filepath.Join(root, "pricing", "catalog", "v1")
	generated, err := pricing.NewCache(root).Load(t.Context())
	if err != nil {
		t.Fatalf("load generated catalog from disk: %v", err)
	}
	providerIDs := manifestProviderIDs(t, filepath.Join(catalogDir, "manifest.json"))
	if len(providerIDs) == 0 {
		t.Fatal("generated manifest lists no providers")
	}

	server := httptest.NewServer(http.FileServer(http.Dir(catalogDir)))
	t.Cleanup(server.Close)

	fetcher, err := pricing.NewFetcher(server.Client(), server.URL+"/manifest.json")
	if err != nil {
		t.Fatalf("build runtime fetcher: %v", err)
	}
	result, err := fetcher.Fetch(t.Context(), "", false)
	if err != nil {
		t.Fatalf("fetch generated catalog: %v", err)
	}
	if result.NotModified || result.Catalog == nil {
		t.Fatalf("fetch result = %+v, want a complete catalog", result)
	}

	cache := pricing.NewCache(t.TempDir())
	if err := cache.Install(t.Context(), result.Catalog); err != nil {
		t.Fatalf("install fetched catalog: %v", err)
	}
	reloaded, err := cache.Load(t.Context())
	if err != nil {
		t.Fatalf("reload installed catalog: %v", err)
	}

	for _, providerID := range providerIDs {
		want := generated.Snapshot().ProviderVersion(providerID)
		if want == "" {
			t.Fatalf("generated catalog has no version for provider %q", providerID)
		}
		if got := result.Catalog.Snapshot().ProviderVersion(providerID); got != want {
			t.Errorf("fetched %s version = %q, want %q", providerID, got, want)
		}
		if got := reloaded.Snapshot().ProviderVersion(providerID); got != want {
			t.Errorf("reloaded %s version = %q, want %q", providerID, got, want)
		}
	}
}

func manifestProviderIDs(t *testing.T, path string) []string {
	t.Helper()
	contents, err := os.ReadFile(path) //nolint:gosec // test-controlled generated path
	if err != nil {
		t.Fatalf("read generated manifest: %v", err)
	}
	var manifest struct {
		Providers []struct {
			ProviderID string `json:"providerId"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatalf("decode generated manifest: %v", err)
	}
	ids := make([]string, 0, len(manifest.Providers))
	for _, provider := range manifest.Providers {
		ids = append(ids, provider.ProviderID)
	}
	return ids
}
