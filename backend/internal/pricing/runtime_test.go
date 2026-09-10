package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDecodeCandidateBuildsExactCanonicalSnapshot(t *testing.T) {
	candidate := testCandidate(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.000003", Read: strptr("0.0000003"), Write: strptr("0.00000375"), Write1H: strptr("0.000006"), Output: "0.000015"}},
		"openai":    {{ID: "gpt-test", Input: "0.000001", Output: "0.000004"}},
		"zai":       {{ID: "glm-test", Input: "0.0000005", Output: "0.000002"}},
	})

	snapshot, err := DecodeCandidate(candidate.manifest, candidate.providers)
	if err != nil {
		t.Fatalf("DecodeCandidate: %v", err)
	}
	if got, want := snapshot.ProviderVersion(" Z.AI "), candidate.versions["zai"]; got != want {
		t.Fatalf("ProviderVersion = %q, want %q", got, want)
	}
	event := domain.ModelUsageEvent{BillingProviderID: " ANTHROPIC ", ModelID: "anthropic/CLAUDE-TEST"}
	estimate, err := snapshot.Estimate(event)
	if err != nil {
		t.Fatalf("Estimate exact canonical lookup: %v", err)
	}
	if estimate.PricingVersion != candidate.versions["anthropic"] {
		t.Fatalf("PricingVersion = %q, want %q", estimate.PricingVersion, candidate.versions["anthropic"])
	}
}

func TestDecodeCandidateRejectsMalformedCatalogFacts(t *testing.T) {
	base := map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.1", Output: "0.2"}},
		"openai":    {{ID: "gpt-test", Input: "0.1", Output: "0.2"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *testCatalog)
	}{
		{name: "wrong hash", mutate: func(t *testing.T, c *testCatalog) {
			providerPath, contents := c.provider(t, "anthropic")
			c.providers[providerPath] = append(contents, ' ')
		}},
		{name: "unsafe path", mutate: func(t *testing.T, c *testCatalog) {
			c.manifest = replaceJSONField(t, c.manifest, "providers", func(providers []any) { providers[0].(map[string]any)["path"] = "../secret.json" })
		}},
		{name: "wrong model count", mutate: func(t *testing.T, c *testCatalog) {
			c.manifest = replaceJSONField(t, c.manifest, "providers", func(providers []any) { providers[0].(map[string]any)["modelCount"] = float64(99) })
		}},
		{name: "unknown manifest field", mutate: func(t *testing.T, c *testCatalog) { c.manifest = addJSONField(t, c.manifest, "surprise", true) }},
		{name: "duplicate canonical model", mutate: func(t *testing.T, c *testCatalog) {
			_, contents := c.provider(t, "anthropic")
			blob := decodeJSONMap(t, contents)
			models := blob["models"].([]any)
			models = append(models, map[string]any{"modelId": "CLAUDE-TEST", "rates": map[string]any{"uncachedInputUsdPerToken": "0.1", "outputUsdPerToken": "0.2"}})
			blob["models"] = models
			c.replaceProvider(t, "anthropic", blob)
		}},
		{name: "noncanonical decimal", mutate: func(t *testing.T, c *testCatalog) {
			_, contents := c.provider(t, "anthropic")
			blob := decodeJSONMap(t, contents)
			blob["models"].([]any)[0].(map[string]any)["rates"].(map[string]any)["uncachedInputUsdPerToken"] = "0.10"
			c.replaceProvider(t, "anthropic", blob)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := testCandidate(t, base)
			tc.mutate(t, &candidate)
			if _, err := DecodeCandidate(candidate.manifest, candidate.providers); err == nil {
				t.Fatal("DecodeCandidate error = nil")
			}
		})
	}
}

func TestEstimateRoundsComponentsHalfUpAndSumsChecked(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.0000000005", Read: strptr("0.0000000015"), Write: strptr("0.0000000025"), Write1H: strptr("0.0000000035"), Output: "0.0000000045"}},
		"openai":    {{ID: "gpt-test", Input: "0", Output: "0"}},
		"zai":       {{ID: "glm-test", Input: "0", Output: "0"}},
	})
	five, one := int64(1), int64(1)
	estimate, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderAnthropic, BillingProviderID: "anthropic", ModelID: "claude-test",
		Tokens:            pricingTokens(4, 1, 3, 1),
		ProviderUsageJSON: anthropicUsageJSON(2, &five, &one),
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	// One fresh token at 0.5 nano rounds half up to 1; the two cache-write
	// tokens charge 2.5 and 3.5 and round to 3 each. Input is their sum.
	assertCost(t, "input", estimate.InputNanos, 7)
	assertCost(t, "cached input", estimate.CachedInputNanos, 2)
	assertCost(t, "output", estimate.OutputNanos, 5)
	assertCost(t, "total", estimate.TotalNanos, 14)
}

// Only 5 of 112 OpenAI models publish a cache-write rate. A catalog without one
// is not missing data: the provider bills writes as ordinary input, so the whole
// uncached bucket takes the input rate and the event still prices completely.
func TestEstimateChargesCacheWritesAsInputWithoutAWriteRate(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.1", Output: "0.2"}},
		"openai":    {{ID: "gpt-test", Input: "0.000001", Read: strptr("0.0000001"), Output: "0.000002"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	})
	estimate, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai", ModelID: "gpt-test",
		Tokens:            pricingTokens(1000, 400, 600, 200),
		ProviderUsageJSON: codexUsageJSON(150),
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	assertCost(t, "input", estimate.InputNanos, 600_000)
	assertCost(t, "cached input", estimate.CachedInputNanos, 40_000)
	assertCost(t, "output", estimate.OutputNanos, 400_000)
	assertCost(t, "total", estimate.TotalNanos, 1_040_000)

	// The same event prices identically with no provider object at all: without
	// a write rate the split is never consulted.
	withoutJSON, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai", ModelID: "gpt-test",
		Tokens: pricingTokens(1000, 400, 600, 200),
	})
	if err != nil {
		t.Fatalf("Estimate without provider usage: %v", err)
	}
	assertCost(t, "total without provider usage", withoutJSON.TotalNanos, 1_040_000)
}

// A model that does publish a write rate needs the split, and an event stored
// before the bounded object was captured cannot supply one.
func TestEstimateLeavesInputUnknownWhenAPricedWriteSplitIsMissing(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.000003", Read: strptr("0.0000003"), Write: strptr("0.00000375"), Output: "0.000015"}},
		"openai":    {{ID: "gpt-test", Input: "0.1", Output: "0.2"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	})
	estimate, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderAnthropic, BillingProviderID: "anthropic", ModelID: "claude-test",
		Tokens: pricingTokens(1000, 400, 600, 200),
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if estimate.InputNanos != nil || estimate.TotalNanos != nil {
		t.Fatalf("input cost = %v, total = %v, want both unknown", estimate.InputNanos, estimate.TotalNanos)
	}
	// The other two components are still known and still contribute to the
	// aggregate lower bound.
	assertCost(t, "cached input", estimate.CachedInputNanos, 120_000)
	assertCost(t, "output", estimate.OutputNanos, 3_000_000)

	// With the object present the single write rate applies to the whole bucket.
	priced, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderAnthropic, BillingProviderID: "anthropic", ModelID: "claude-test",
		Tokens:            pricingTokens(1000, 400, 600, 200),
		ProviderUsageJSON: anthropicUsageJSON(100, nil, nil),
	})
	if err != nil {
		t.Fatalf("Estimate with provider usage: %v", err)
	}
	assertCost(t, "input", priced.InputNanos, 1_875_000)
	assertCost(t, "total", priced.TotalNanos, 4_995_000)
}

func TestEstimateKeepsUnknownBucketsUnknownAndZeroBucketsKnown(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.1", Output: "0.2"}},
		"openai":    {{ID: "gpt-test", Input: "0.1", Output: "0.2"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	})

	// An uncollected counter prices as unknown, never as zero.
	estimate, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderAnthropic, BillingProviderID: "anthropic", ModelID: "claude-test",
		Tokens: domain.UsageTokenMetrics{CachedInputTokens: costInt64(0), OutputTokens: costInt64(0)},
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if estimate.InputNanos != nil || estimate.TotalNanos != nil {
		t.Fatalf("uncollected input must be unknown: %#v", estimate)
	}
	assertCost(t, "zero cached input", estimate.CachedInputNanos, 0)
	assertCost(t, "zero output", estimate.OutputNanos, 0)

	// A known zero stays a known zero even for a model the catalog never priced:
	// zero tokens cost zero at any rate, including an absent one.
	missing, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai", ModelID: "missing",
		Tokens: pricingTokens(0, 0, 0, 0),
	})
	if err != nil {
		t.Fatalf("Estimate missing model zero vector: %v", err)
	}
	assertCost(t, "missing input zero", missing.InputNanos, 0)
	assertCost(t, "missing cached input zero", missing.CachedInputNanos, 0)
	assertCost(t, "missing output zero", missing.OutputNanos, 0)
	assertCost(t, "missing total zero", missing.TotalNanos, 0)

	// A nonzero bucket for an unpriced model is unknown, not zero.
	unpriced, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai", ModelID: "missing",
		Tokens: pricingTokens(1, 0, 1, 0),
	})
	if err != nil {
		t.Fatalf("Estimate missing model nonzero vector: %v", err)
	}
	if unpriced.InputNanos != nil || unpriced.TotalNanos != nil {
		t.Fatalf("unpriced model must leave input unknown: %#v", unpriced)
	}
}

func TestEstimateRejectsInvalidTokensAndOverflow(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "9223372036.854775807", Output: "0"}},
		"openai":    {{ID: "gpt-test", Input: "0", Output: "0"}},
		"zai":       {{ID: "glm-test", Input: "0", Output: "0"}},
	})
	if _, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderAnthropic, BillingProviderID: "anthropic", ModelID: "claude-test",
		Tokens: pricingTokens(2, 0, 2, 0),
	}); err == nil {
		t.Fatal("overflow error = nil")
	}
	if _, err := snapshot.Estimate(domain.ModelUsageEvent{
		ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai", ModelID: "gpt-test",
		Tokens: pricingTokens(0, 0, 0, -1),
	}); err == nil {
		t.Fatal("negative token error = nil")
	}
	if math.MaxInt64 == 0 {
		t.Fatal("unreachable")
	}
}

func TestActivationFenceAdmissionHonorsContext(t *testing.T) {
	fence := NewActivationFence()
	release, err := fence.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := fence.Acquire(ctx); err == nil {
		t.Fatal("blocked Acquire error = nil")
	}
	release()
	second, err := fence.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	second()
}

// Break caught: a waiter that arrived while the fence was held could lose the
// released token to a later caller. That let an old backfill job reacquire the
// fence for another page before an already-pending catalog activation.
func TestActivationFenceAdmitsWaitersInArrivalOrder(t *testing.T) {
	fence := NewActivationFence()
	release, err := fence.Acquire(context.Background())
	if err != nil {
		t.Fatalf("hold fence: %v", err)
	}

	firstAdmitted := make(chan struct{})
	allowFirstToWait := make(chan struct{})
	firstCtx := &fenceAdmissionContext{
		Context:  context.Background(),
		admitted: firstAdmitted,
		proceed:  allowFirstToWait,
	}
	secondAdmitted := make(chan struct{})
	secondCtx := &fenceAdmissionContext{Context: context.Background(), admitted: secondAdmitted}
	order := make(chan int, 2)
	errs := make(chan error, 2)
	wait := func(ctx context.Context, id int) {
		nextRelease, acquireErr := fence.Acquire(ctx)
		if acquireErr == nil {
			order <- id
			nextRelease()
		}
		errs <- acquireErr
	}

	go wait(firstCtx, 1)
	<-firstAdmitted
	go wait(secondCtx, 2)
	<-secondAdmitted
	close(allowFirstToWait)
	release()

	if first, second := <-order, <-order; first != 1 || second != 2 {
		t.Fatalf("admission order = [%d %d], want [1 2]", first, second)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
}

type fenceAdmissionContext struct {
	context.Context
	admitted chan struct{}
	proceed  <-chan struct{}
	once     sync.Once
}

func (c *fenceAdmissionContext) Err() error {
	c.once.Do(func() {
		close(c.admitted)
		if c.proceed != nil {
			<-c.proceed
		}
	})
	return c.Context.Err()
}

func TestActivationFenceNeverAdmitsAlreadyCanceledContext(t *testing.T) {
	fence := NewActivationFence()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for attempt := 0; attempt < 1_000; attempt++ {
		release, err := fence.Acquire(ctx)
		if err == nil {
			release()
			t.Fatalf("Acquire admitted canceled context on attempt %d", attempt)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire error = %v, want context canceled", err)
		}
	}
}

func TestActivationFenceReturnsTokenWhenCancellationWinsAfterAdmission(t *testing.T) {
	fence := NewActivationFence()
	ctx := &cancelAfterAdmissionContext{done: make(chan struct{})}
	if release, err := fence.Acquire(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("Acquire = release %v, error %v; want context canceled", release != nil, err)
	}
	release, err := fence.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after canceled admission: %v", err)
	}
	release()
}

func TestManagerActivatesOnlyChangedProviders(t *testing.T) {
	first := decodeTestSnapshot(t, testBaseModels("0.1"))
	secondModels := testBaseModels("0.1")
	secondModels["openai"] = []testModel{{ID: "gpt-test", Input: "0.2", Output: "0.2"}}
	second := decodeTestSnapshot(t, secondModels)
	manager := NewManager(nil)
	activations, err := manager.Activate(context.Background(), first)
	if err != nil || len(activations) != 3 {
		t.Fatalf("first Activate = %#v, %v; want 3", activations, err)
	}
	activations, err = manager.Activate(context.Background(), second)
	if err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	if len(activations) != 1 || activations[0].ProviderID != "openai" || activations[0].PreviousVersion == activations[0].Version {
		t.Fatalf("second activations = %#v, want changed openai", activations)
	}
}

func TestManagerRejectsUnvalidatedSnapshot(t *testing.T) {
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), &Snapshot{}); err == nil {
		t.Fatal("Activate unvalidated snapshot error = nil")
	}
}

func TestNewManagerNeverPublishesUnvalidatedInitialSnapshot(t *testing.T) {
	invalid := &Snapshot{}
	manager := NewManager(invalid)
	if manager.Snapshot() == invalid {
		t.Fatal("NewManager published unvalidated initial snapshot")
	}
	if manager.Snapshot() == nil {
		t.Fatal("NewManager fallback snapshot is nil")
	}
	valid := decodeTestSnapshot(t, testBaseModels("0.1"))
	initialized := NewManager(valid)
	if initialized.Snapshot() != valid {
		t.Fatal("NewManager did not retain validated initial snapshot")
	}
}

type cancelAfterAdmissionContext struct {
	done   chan struct{}
	calls  int
	closed bool
}

func (c *cancelAfterAdmissionContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterAdmissionContext) Done() <-chan struct{}       { return c.done }
func (c *cancelAfterAdmissionContext) Value(any) any               { return nil }
func (c *cancelAfterAdmissionContext) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	if !c.closed {
		close(c.done)
		c.closed = true
	}
	return context.Canceled
}

type testModel struct {
	ID                   string
	Input, Output        string
	Read, Write, Write1H *string
}

type testCatalog struct {
	manifest  []byte
	providers map[string][]byte
	versions  map[string]string
}

func testBaseModels(rate string) map[string][]testModel {
	return map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: rate, Output: rate}},
		"openai":    {{ID: "gpt-test", Input: rate, Output: rate}},
		"zai":       {{ID: "glm-test", Input: rate, Output: rate}},
	}
}

func decodeTestSnapshot(t *testing.T, models map[string][]testModel) *Snapshot {
	t.Helper()
	candidate := testCandidate(t, models)
	snapshot, err := DecodeCandidate(candidate.manifest, candidate.providers)
	if err != nil {
		t.Fatalf("DecodeCandidate fixture: %v", err)
	}
	return snapshot
}

func testCandidate(t *testing.T, models map[string][]testModel) testCatalog {
	t.Helper()
	candidate := testCatalog{providers: map[string][]byte{}, versions: map[string]string{}}
	refs := make([]map[string]any, 0, 3)
	for _, providerID := range []string{"anthropic", "openai", "zai"} {
		entries := make([]map[string]any, 0, len(models[providerID]))
		for _, model := range models[providerID] {
			rates := map[string]any{"uncachedInputUsdPerToken": model.Input, "outputUsdPerToken": model.Output}
			if model.Read != nil {
				rates["cacheReadUsdPerToken"] = *model.Read
			}
			if model.Write != nil {
				rates["cacheWriteUsdPerToken"] = *model.Write
			}
			if model.Write1H != nil {
				rates["cacheWrite1hUsdPerToken"] = *model.Write1H
			}
			entries = append(entries, map[string]any{"modelId": model.ID, "rates": rates})
		}
		blob, err := json.Marshal(map[string]any{"schemaVersion": 1, "providerId": providerID, "models": entries})
		if err != nil {
			t.Fatal(err)
		}
		blob = append(blob, '\n')
		digest := sha256.Sum256(blob)
		hash := hex.EncodeToString(digest[:])
		path := "providers/" + providerID + "/" + hash + ".json"
		candidate.providers[path] = blob
		version := "ao-catalog:" + providerID + ":sha256:" + hash
		candidate.versions[providerID] = version
		refs = append(refs, map[string]any{"providerId": providerID, "version": version, "sha256": hash, "path": path, "modelCount": len(entries)})
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"source":        map[string]any{"repository": "BerriAI/litellm", "revision": "0123456789abcdef0123456789abcdef01234567", "path": "model_prices_and_context_window.json"},
		"providers":     refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate.manifest = append(manifest, '\n')
	return candidate
}

func (c *testCatalog) replaceProvider(t *testing.T, providerID string, blob map[string]any) {
	t.Helper()
	contents, err := json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	sum := sha256.Sum256(contents)
	digest := hex.EncodeToString(sum[:])
	var manifest map[string]any
	if err := json.Unmarshal(c.manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, item := range manifest["providers"].([]any) {
		ref := item.(map[string]any)
		if ref["providerId"] != providerID {
			continue
		}
		oldPath := ref["path"].(string)
		delete(c.providers, oldPath)
		path := "providers/" + providerID + "/" + digest + ".json"
		ref["sha256"], ref["path"], ref["version"], ref["modelCount"] = digest, path, "ao-catalog:"+providerID+":sha256:"+digest, len(blob["models"].([]any))
		c.providers[path] = contents
	}
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	c.manifest = append(updated, '\n')
}

func (c *testCatalog) provider(t *testing.T, providerID string) (string, []byte) {
	t.Helper()
	manifest := decodeJSONMap(t, c.manifest)
	for _, item := range manifest["providers"].([]any) {
		ref := item.(map[string]any)
		if ref["providerId"] == providerID {
			providerPath := ref["path"].(string)
			return providerPath, c.providers[providerPath]
		}
	}
	t.Fatalf("provider %q missing", providerID)
	return "", nil
}

func replaceJSONField(t *testing.T, raw []byte, key string, mutate func([]any)) []byte {
	t.Helper()
	value := decodeJSONMap(t, raw)
	mutate(value[key].([]any))
	updated, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(updated, '\n')
}

func addJSONField(t *testing.T, raw []byte, key string, value any) []byte {
	t.Helper()
	object := decodeJSONMap(t, raw)
	object[key] = value
	updated, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return append(updated, '\n')
}

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func strptr(value string) *string { return &value }

func assertCost(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}

func costInt64(value int64) *int64 { return &value }

// pricingTokens builds the canonical vector the estimator reads alongside a
// provider detail block.
func pricingTokens(input, cachedInput, uncachedInput, output int64) domain.UsageTokenMetrics {
	return domain.UsageTokenMetrics{
		InputTokens: &input, CachedInputTokens: &cachedInput,
		UncachedInputTokens: &uncachedInput, OutputTokens: &output,
	}
}

// anthropicUsageJSON is the bounded object Claude emits, reduced to the fields
// pricing reads back out of it.
func anthropicUsageJSON(cacheCreation int64, fiveM, oneH *int64) string {
	usage := map[string]any{"cache_creation_input_tokens": cacheCreation}
	if fiveM != nil && oneH != nil {
		usage["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": *fiveM, "ephemeral_1h_input_tokens": *oneH,
		}
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// codexUsageJSON is payload.info reduced to the per-event vector pricing reads.
func codexUsageJSON(cacheWrite int64) string {
	encoded, err := json.Marshal(map[string]any{
		"last_token_usage": map[string]any{"cache_write_input_tokens": cacheWrite},
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// ProviderForModel is the last resort for an event nothing else could attribute,
// so its failure mode has to be silence. A wrong answer here is written into a
// write-once column and priced against the wrong catalog, permanently.
func TestProviderForModelResolvesOnlyAnUnambiguousOwner(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "claude-test", Input: "0.1", Output: "0.2"}},
		"openai":    {{ID: "gpt-test", Input: "0.1", Output: "0.2"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	})
	for _, test := range []struct {
		name, modelID, want string
	}{
		{name: "anthropic model", modelID: "claude-test", want: "anthropic"},
		{name: "openai model", modelID: "gpt-test", want: "openai"},
		{name: "zai model", modelID: "glm-test", want: "zai"},
		// The stored id keeps whatever form the provider reported, so a
		// provider-prefixed id still has to reach its unprefixed catalog entry.
		{name: "provider-prefixed", modelID: "anthropic/claude-test", want: "anthropic"},
		{name: "case and padding", modelID: "  CLAUDE-TEST  ", want: "anthropic"},
		// A model no catalog lists says nothing about who billed it, and neither
		// do the sentinels the parser falls back to.
		{name: "unlisted model", modelID: "glm-5.3"},
		{name: "parser sentinel", modelID: "unknown"},
		{name: "synthetic", modelID: "<synthetic>"},
		{name: "empty", modelID: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshot.ProviderForModel(test.modelID); got != test.want {
				t.Fatalf("ProviderForModel(%q) = %q, want %q", test.modelID, got, test.want)
			}
		})
	}

	// Two providers listing the same id makes the model useless as evidence.
	ambiguous := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {{ID: "shared-model", Input: "0.1", Output: "0.2"}},
		"openai":    {{ID: "shared-model", Input: "0.9", Output: "0.9"}},
		"zai":       {{ID: "glm-test", Input: "0.1", Output: "0.2"}},
	})
	if got := ambiguous.ProviderForModel("shared-model"); got != "" {
		t.Fatalf("ambiguous model resolved to %q, want no owner", got)
	}

	if got := (*Snapshot)(nil).ProviderForModel("claude-test"); got != "" {
		t.Fatalf("nil snapshot resolved to %q", got)
	}
}
