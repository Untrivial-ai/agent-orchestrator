package catalogsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Break caught: emitting raw upstream names, floating-point spellings, or a
// provider blob whose address is not its exact canonical JSON bytes.
func TestSyncWritesCanonicalContentAddressedCatalog(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
  "OpenAI/GPT-4o": {
    "litellm_provider": " OpenAI ",
    "mode": "responses",
    "input_cost_per_token": 1e-6,
    "output_cost_per_token": 0,
    "cache_read_input_token_cost": 0
  },
  "zai/GLM-4.5": {
    "litellm_provider": "z.ai",
    "mode": "chat",
    "input_cost_per_token": 0.000002,
    "output_cost_per_token": 0.000004
  },
  "anthropic/claude-test": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015,
    "cache_creation_input_token_cost": 0.00000375,
    "cache_creation_input_token_cost_above_1hr": 0.000006
  }
}`)

	result, err := Sync(root, upstream, Source{
		Repository: "BerriAI/litellm",
		Revision:   "0123456789abcdef0123456789abcdef01234567",
		Path:       "model_prices_and_context_window.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("Sync changed = false, want true")
	}

	manifest, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantManifest := `{"schemaVersion":1,"source":{"repository":"BerriAI/litellm","revision":"0123456789abcdef0123456789abcdef01234567","path":"model_prices_and_context_window.json"},"providers":[{"providerId":"anthropic","version":"ao-catalog:anthropic:sha256:030ece6928c2c0e57313e57395c506889aecfa047fb295f38832f0424c16dd2a","sha256":"030ece6928c2c0e57313e57395c506889aecfa047fb295f38832f0424c16dd2a","path":"providers/anthropic/030ece6928c2c0e57313e57395c506889aecfa047fb295f38832f0424c16dd2a.json","modelCount":1},{"providerId":"openai","version":"ao-catalog:openai:sha256:893e629ba2ea7af7f13466a95a07d60ba2506a9bdef72aa6380c6daa7332769d","sha256":"893e629ba2ea7af7f13466a95a07d60ba2506a9bdef72aa6380c6daa7332769d","path":"providers/openai/893e629ba2ea7af7f13466a95a07d60ba2506a9bdef72aa6380c6daa7332769d.json","modelCount":1},{"providerId":"zai","version":"ao-catalog:zai:sha256:6945a8b33e7133ba6c9ce96378ef834ecab1f9c9872b5f5d772a2e1b3fb8805a","sha256":"6945a8b33e7133ba6c9ce96378ef834ecab1f9c9872b5f5d772a2e1b3fb8805a","path":"providers/zai/6945a8b33e7133ba6c9ce96378ef834ecab1f9c9872b5f5d772a2e1b3fb8805a.json","modelCount":1}]}
`
	if string(manifest) != wantManifest {
		t.Fatalf("manifest = %s, want %s", manifest, wantManifest)
	}

	openAI, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1/providers/openai/893e629ba2ea7af7f13466a95a07d60ba2506a9bdef72aa6380c6daa7332769d.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantOpenAI := `{"schemaVersion":1,"providerId":"openai","models":[{"modelId":"gpt-4o","rates":{"uncachedInputUsdPerToken":"0.000001","cacheReadUsdPerToken":"0","outputUsdPerToken":"0"}}]}
`
	if string(openAI) != wantOpenAI {
		t.Fatalf("OpenAI blob = %s, want %s", openAI, wantOpenAI)
	}
}

// Break caught: treating conflicting provider/model records as independent
// models would silently select an arbitrary price.
func TestSyncRejectsConflictingCanonicalDuplicates(t *testing.T) {
	upstream := `{
"openai/gpt-test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":2},
"OPENAI/GPT-TEST":{"litellm_provider":" OPENAI ","mode":"responses","input_cost_per_token":3,"output_cost_per_token":2},
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`
	_, err := Sync(t.TempDir(), []byte(upstream), testSource("1"))
	if err == nil || !strings.Contains(err.Error(), "conflicting duplicate rates") {
		t.Fatalf("Sync error = %v, want conflicting duplicate rates", err)
	}
}

// Break caught: accepting unsupported LiteLLM variants as base prices, or
// allowing a reviewed provider to disappear without failing the catalog build.
func TestSyncFiltersUnsupportedRecordsAndRequiresEveryProvider(t *testing.T) {
	upstream := `{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"embedding","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"other/x":{"litellm_provider":"other","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`
	_, err := Sync(t.TempDir(), []byte(upstream), testSource("2"))
	if err == nil || !strings.Contains(err.Error(), `provider "openai" produced zero supported models`) {
		t.Fatalf("Sync error = %v, want missing openai provider error", err)
	}
}

// Break caught: a metadata-only LiteLLM entry preventing the catalog from
// including another, fully priced model from the same reviewed provider.
func TestSyncIgnoresSupportedModeRecordsWithoutBaseRates(t *testing.T) {
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/metadata-only":{"litellm_provider":"openai","mode":"chat"},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(t.TempDir(), upstream, testSource("metadata-only")); err != nil {
		t.Fatalf("Sync returned %v for an unpriced metadata entry", err)
	}
}

// Break caught: decimal and scientific whole-number rates emitting a trailing
// decimal point, then making the generated catalog fail its own validator.
func TestSyncCanonicalizesWholeDecimalAndScientificRates(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1.0,"output_cost_per_token":2e0},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(root, upstream, testSource("whole-rates")); err != nil {
		t.Fatal(err)
	}
	if err := Validate(root); err != nil {
		t.Fatalf("Validate generated catalog: %v", err)
	}

	manifestContents, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded manifest
	if err := json.Unmarshal(manifestContents, &decoded); err != nil {
		t.Fatal(err)
	}
	openAIPath := filepath.Join(root, "pricing/catalog/v1", findProvider(decoded.Providers, "openai").Path)
	contents, err := os.ReadFile(openAIPath)
	if err != nil {
		t.Fatal(err)
	}
	var blob providerBlob
	if err := json.Unmarshal(contents, &blob); err != nil {
		t.Fatal(err)
	}
	if got := blob.Models[0].Rates.UncachedInputUSDPerToken; got != "1" {
		t.Fatalf("input rate = %q, want %q", got, "1")
	}
	if got := blob.Models[0].Rates.OutputUSDPerToken; got != "2" {
		t.Fatalf("output rate = %q, want %q", got, "2")
	}
}

// Break caught: LiteLLM names Anthropic's one-hour cache-creation price with
// the above_1hr suffix. Reading a made-up key silently drops the rate.
func TestSyncMapsLiteLLMAnthropicOneHourCacheCreationRate(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1,"cache_creation_input_token_cost_above_1hr":0.000006},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(root, upstream, testSource("anthropic-one-hour")); err != nil {
		t.Fatal(err)
	}

	blob := readProviderBlob(t, root, "anthropic")
	if got := blob.Models[0].Rates.CacheWrite1HUSDPerToken; got == nil || *got != "0.000006" {
		t.Fatalf("one-hour cache write rate = %v, want 0.000006", got)
	}
}

// Break caught: sub-one-dollar decimal spellings kept insignificant trailing
// zeroes, producing blobs rejected by the stricter runtime decoder.
func TestNormalizeDecimalRemovesFractionalTrailingZeroes(t *testing.T) {
	for input, want := range map[string]string{
		"0.0010":     "0.001",
		"1.2300e-3":  "0.00123",
		"0.00000000": "0",
	} {
		got, err := normalizeDecimal(input)
		if err != nil {
			t.Fatalf("normalizeDecimal(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("normalizeDecimal(%q) = %q, want %q", input, got, want)
		}
	}
}

// Break caught: identical optional cache prices used distinct string pointers,
// so canonical duplicate records were incorrectly reported as conflicting.
func TestSyncDeduplicatesIdenticalOptionalRates(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/gpt-test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":2,"cache_read_input_token_cost":0.5,"cache_creation_input_token_cost":1.25},
"OPENAI/GPT-TEST":{"litellm_provider":" OPENAI ","mode":"responses","input_cost_per_token":1,"output_cost_per_token":2,"cache_read_input_token_cost":0.5,"cache_creation_input_token_cost":1.25},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(root, upstream, testSource("identical-optional-rates")); err != nil {
		t.Fatalf("Sync identical canonical records: %v", err)
	}

	blob := readProviderBlob(t, root, "openai")
	if got := len(blob.Models); got != 1 {
		t.Fatalf("OpenAI model count = %d, want 1", got)
	}
}

// Break caught: trusting an in-range int64 exponent could make normalization
// panic or attempt an enormous allocation while expanding scientific notation.
func TestNormalizeDecimalRejectsUnboundedScientificExponent(t *testing.T) {
	const helperEnv = "AO_CATALOGSYNC_HUGE_EXPONENT_HELPER"
	if os.Getenv(helperEnv) == "1" {
		if _, err := normalizeDecimal("1e9223372036854775807"); err == nil {
			os.Exit(2)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNormalizeDecimalRejectsUnboundedScientificExponent$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("normalization did not reject the huge exponent promptly: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("normalization panicked or failed its helper process: %v\n%s", err, output)
	}
}

// Break caught: treating an alternate rate spelling in a reviewed provider
// blob as canonical even though it changes the content-addressed contract.
func TestValidateBlobRejectsNoncanonicalRateSpelling(t *testing.T) {
	blob := providerBlob{
		SchemaVersion: schemaVersion,
		ProviderID:    "openai",
		Models: []pricedModel{{
			ModelID: "o",
			Rates: rates{
				UncachedInputUSDPerToken: "1.0",
				OutputUSDPerToken:        "2e0",
			},
		}},
	}
	err := validateBlob(blob, providerRef{ProviderID: "openai", ModelCount: 1})
	if err == nil || !strings.Contains(err.Error(), "noncanonical") {
		t.Fatalf("validateBlob error = %v, want noncanonical rate error", err)
	}
}

// Break caught: rewriting a reviewed manifest merely because unrelated
// upstream content changed, or rejecting append-only historical blobs.
func TestSyncIsSemanticNoOpForUnchangedProviderPayloads(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(root, upstream, testSource("3")); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "pricing/catalog/v1/manifest.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pricing/catalog/v1/providers/openai"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pricing/catalog/v1/providers/openai/historical.json"), []byte("not a catalog blob"), 0o600); err != nil {
		t.Fatal(err)
	}

	semanticNoopUpstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/batch":{"litellm_provider":"openai","mode":"batch","input_cost_per_token":99,"output_cost_per_token":99}
}`)
	changed, err := Sync(root, semanticNoopUpstream, testSource("4"))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Changed {
		t.Fatal("Sync changed = true, want semantic no-op")
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("manifest changed on semantic no-op\nbefore: %s\nafter: %s", before, after)
	}
	if err := Validate(root); err != nil {
		t.Fatalf("Validate rejected unreferenced historical blob: %v", err)
	}
}

// Break caught: accepting an editable referenced blob after its manifest hash
// has been reviewed.
func TestValidateRejectsChangedReferencedBlob(t *testing.T) {
	root := t.TempDir()
	upstream := []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`)
	if _, err := Sync(root, upstream, testSource("5")); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Providers []struct {
			Path string `json:"path"`
		}
	}
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pricing/catalog/v1", decoded.Providers[0].Path)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(root); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("Validate error = %v, want hash mismatch", err)
	}
}

func testSource(seed string) Source {
	sum := sha256.Sum256([]byte(seed))
	return Source{
		Repository: "BerriAI/litellm",
		Revision:   fmt.Sprintf("%040x", sum)[:40],
		Path:       "model_prices_and_context_window.json",
	}
}

func readProviderBlob(t *testing.T, root, providerID string) providerBlob {
	t.Helper()
	manifestContents, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded manifest
	if err := json.Unmarshal(manifestContents, &decoded); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "pricing/catalog/v1", findProvider(decoded.Providers, providerID).Path))
	if err != nil {
		t.Fatal(err)
	}
	var blob providerBlob
	if err := json.Unmarshal(contents, &blob); err != nil {
		t.Fatal(err)
	}
	return blob
}
