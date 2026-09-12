package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

// Break caught: a Codex token_count record may carry only cumulative totals.
// The parser derives the neutral counters from them but used to store the raw
// cumulative object, and the estimator reads its cache-write bucket from the
// optional per-event last_token_usage — so a model that publishes a cache-write
// rate priced its input, and therefore its total, as unknown. Every current
// gpt-5.6 variant publishes one, so this is the models Codex actually runs.
//
// The shipped catalog is the input on purpose: the set of write-rated models
// changes with the daily refresh, and a fixture would stop covering it.
func TestEveryWriteRatedOpenAIModelPricesBothCodexPayloadShapes(t *testing.T) {
	root := repositoryRoot(t)
	catalog, err := pricing.NewCache(root).Load(t.Context())
	if err != nil {
		t.Fatalf("load shipped catalog: %v", err)
	}
	snapshot := catalog.Snapshot()

	models := writeRatedOpenAIModels(t, root)
	if len(models) == 0 {
		t.Fatal("shipped catalog lists no write-rated OpenAI model; this test would prove nothing")
	}

	for _, modelID := range models {
		t.Run(modelID, func(t *testing.T) {
			// One logical event, 40 uncached input of which 10 are cache writes,
			// described the two ways Codex is allowed to describe it.
			last := codexTokenVector{
				InputTokens: 100, CachedInputTokens: 60, CacheWriteInputTokens: 10,
				OutputTokens: 20, ReasoningOutputTokens: 5, TotalTokens: 120,
			}
			shapes := map[string][]jsonlRecord{
				"cumulative only": {
					{Offset: 0, Data: []byte(`{"type":"session_meta","payload":{"model_provider":"openai"}}`)},
					{Offset: 100, Data: []byte(`{"type":"turn_context","payload":{"model":` + mustJSONString(modelID) + `}}`)},
					{Offset: 200, Data: codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 10, 20, 5)},
				},
				"with last_token_usage": {
					{Offset: 0, Data: []byte(`{"type":"session_meta","payload":{"model_provider":"openai"}}`)},
					{Offset: 100, Data: []byte(`{"type":"turn_context","payload":{"model":` + mustJSONString(modelID) + `}}`)},
					{Offset: 200, Data: codexTokenLineWithLast("2026-07-01T10:00:00Z", last, last)},
				},
			}

			priced := map[string]pricing.Estimate{}
			for name, records := range shapes {
				result := parseRecords(
					usageSource(domain.UsageSourceCodexRollout), records, 500, time.Unix(1700000000, 0).UTC(),
				)
				if len(result.Events) != 1 {
					t.Fatalf("%s: events = %d, want 1", name, len(result.Events))
				}
				estimate, err := snapshot.Estimate(result.Events[0])
				if err != nil {
					t.Fatalf("%s: estimate: %v", name, err)
				}
				// The write split is what the input charge needs; a model whose
				// rate card is missing some other bucket still leaves the total
				// unknown, and that is the honest answer rather than a defect.
				if estimate.InputNanos == nil {
					t.Fatalf("%s: input cost unknown for a write-rated model (provider usage %s)",
						name, result.Events[0].ProviderUsageJSON)
				}
				priced[name] = estimate
			}
			if !reflect.DeepEqual(priced["cumulative only"], priced["with last_token_usage"]) {
				t.Fatalf("the same event priced differently per payload shape:\n cumulative only:      %+v\n with last_token_usage: %+v",
					priced["cumulative only"], priced["with last_token_usage"])
			}
		})
	}
}

func writeRatedOpenAIModels(t *testing.T, root string) []string {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(root, "pricing", "catalog", "v1", "manifest.json"))
	if err != nil {
		t.Fatalf("read catalog manifest: %v", err)
	}
	var manifest struct {
		Providers []struct {
			ProviderID string `json:"providerId"`
			Path       string `json:"path"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode catalog manifest: %v", err)
	}
	blobPath := ""
	for _, provider := range manifest.Providers {
		if provider.ProviderID == "openai" {
			blobPath = provider.Path
		}
	}
	if blobPath == "" {
		t.Fatal("catalog manifest lists no openai provider")
	}
	blobBytes, err := os.ReadFile(filepath.Join(root, "pricing", "catalog", "v1", blobPath))
	if err != nil {
		t.Fatalf("read openai blob: %v", err)
	}
	var blob struct {
		Models []struct {
			ModelID string `json:"modelId"`
			Rates   struct {
				CacheWrite string `json:"cacheWriteUsdPerToken"`
			} `json:"rates"`
		} `json:"models"`
	}
	if err := json.Unmarshal(blobBytes, &blob); err != nil {
		t.Fatalf("decode openai blob: %v", err)
	}
	var models []string
	for _, model := range blob.Models {
		if model.Rates.CacheWrite != "" {
			models = append(models, model.ModelID)
		}
	}
	return models
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pricing", "catalog", "v1", "manifest.json")); err != nil {
		t.Fatalf("shipped catalog not found under %s: %v", root, err)
	}
	return root
}

func mustJSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
