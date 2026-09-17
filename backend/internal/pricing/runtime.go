package pricing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const runtimeSchemaVersion = 1

var (
	sha40Pattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	decimalPattern = regexp.MustCompile(`^(0|[1-9]\d*)(\.\d*[1-9])?$`)
)

type catalogManifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Source        catalogManifestSource `json:"source"`
	Providers     []catalogProviderRef  `json:"providers"`
}

type catalogManifestSource struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Path       string `json:"path"`
}

type catalogProviderRef struct {
	ProviderID string `json:"providerId"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Path       string `json:"path"`
	ModelCount int    `json:"modelCount"`
}

type catalogProviderBlob struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ProviderID    string               `json:"providerId"`
	Models        []catalogPricedModel `json:"models"`
}

type catalogPricedModel struct {
	ModelID string       `json:"modelId"`
	Rates   catalogRates `json:"rates"`
}

type catalogRates struct {
	UncachedInputUSDPerToken string  `json:"uncachedInputUsdPerToken"`
	CacheReadUSDPerToken     *string `json:"cacheReadUsdPerToken,omitempty"`
	CacheWriteUSDPerToken    *string `json:"cacheWriteUsdPerToken,omitempty"`
	CacheWrite1HUSDPerToken  *string `json:"cacheWrite1hUsdPerToken,omitempty"`
	OutputUSDPerToken        string  `json:"outputUsdPerToken"`
}

type exactRates struct {
	input   *big.Rat
	read    *big.Rat
	write   *big.Rat
	write1H *big.Rat
	output  *big.Rat
}

type providerSnapshot struct {
	version string
	models  map[string]exactRates
}

// Snapshot is an immutable, validated view of all provider catalogs. Its maps
// and exact rational rates are never exposed or mutated after construction.
type Snapshot struct {
	providers map[string]providerSnapshot
}

type decodedCatalog struct {
	manifest catalogManifest
	snapshot *Snapshot
}

// Catalog is a complete validated catalog candidate together with its exact
// transport bytes. The byte slices are private defensive copies.
type Catalog struct {
	manifestBytes []byte
	providerBytes map[string][]byte
	manifest      catalogManifest
	snapshot      *Snapshot
}

// ParseCatalog validates and retains a complete candidate for cache install or
// activation.
func ParseCatalog(manifestBytes []byte, providerBytes map[string][]byte) (*Catalog, error) {
	decoded, err := decodeCatalog(manifestBytes, providerBytes)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{
		manifestBytes: bytes.Clone(manifestBytes),
		providerBytes: make(map[string][]byte, len(providerBytes)),
		manifest:      decoded.manifest,
		snapshot:      decoded.snapshot,
	}
	for providerPath, contents := range providerBytes {
		catalog.providerBytes[providerPath] = bytes.Clone(contents)
	}
	return catalog, nil
}

// Snapshot returns the catalog's immutable validated snapshot.
func (c *Catalog) Snapshot() *Snapshot {
	if c == nil {
		return nil
	}
	return c.snapshot
}

// DecodeCandidate strictly validates a complete manifest and all blobs it
// references, returning an immutable pricing snapshot.
func DecodeCandidate(manifestBytes []byte, providerBytes map[string][]byte) (*Snapshot, error) {
	catalog, err := ParseCatalog(manifestBytes, providerBytes)
	if err != nil {
		return nil, err
	}
	return catalog.snapshot, nil
}

func decodeCatalog(manifestBytes []byte, providerBytes map[string][]byte) (decodedCatalog, error) {
	var manifest catalogManifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return decodedCatalog{}, fmt.Errorf("decode pricing manifest: %w", err)
	}
	if err := validateRuntimeManifest(manifest); err != nil {
		return decodedCatalog{}, err
	}
	if len(providerBytes) != len(manifest.Providers) {
		return decodedCatalog{}, fmt.Errorf("provider payload count = %d, want %d", len(providerBytes), len(manifest.Providers))
	}
	snapshot := &Snapshot{providers: make(map[string]providerSnapshot, len(manifest.Providers))}
	for _, ref := range manifest.Providers {
		contents, ok := providerBytes[ref.Path]
		if !ok {
			return decodedCatalog{}, fmt.Errorf("provider %q payload is missing", ref.ProviderID)
		}
		digest := sha256.Sum256(contents)
		if hex.EncodeToString(digest[:]) != ref.SHA256 {
			return decodedCatalog{}, fmt.Errorf("provider %q hash mismatch", ref.ProviderID)
		}
		var blob catalogProviderBlob
		if err := decodeStrictJSON(contents, &blob); err != nil {
			return decodedCatalog{}, fmt.Errorf("decode provider %q: %w", ref.ProviderID, err)
		}
		provider, err := buildProviderSnapshot(blob, ref)
		if err != nil {
			return decodedCatalog{}, err
		}
		snapshot.providers[ref.ProviderID] = provider
	}
	return decodedCatalog{manifest: manifest, snapshot: snapshot}, nil
}

func decodeStrictJSON(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("must contain exactly one JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func validateRuntimeManifest(manifest catalogManifest) error {
	if manifest.SchemaVersion != runtimeSchemaVersion {
		return fmt.Errorf("unsupported pricing manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.Source.Repository != "BerriAI/litellm" || manifest.Source.Path != "model_prices_and_context_window.json" || !sha40Pattern.MatchString(manifest.Source.Revision) {
		return errors.New("invalid pricing manifest source")
	}
	wantProviders := []string{"anthropic", "openai", "zai"}
	if len(manifest.Providers) != len(wantProviders) {
		return fmt.Errorf("pricing manifest has %d providers, want %d", len(manifest.Providers), len(wantProviders))
	}
	for index, providerID := range wantProviders {
		ref := manifest.Providers[index]
		if ref.ProviderID != providerID || !sha256Pattern.MatchString(ref.SHA256) || ref.ModelCount < 1 {
			return fmt.Errorf("invalid pricing manifest provider %q", providerID)
		}
		wantPath := "providers/" + providerID + "/" + ref.SHA256 + ".json"
		if ref.Path != wantPath || !safeProviderPath(ref.Path) {
			return fmt.Errorf("provider %q has unsafe or invalid path %q", providerID, ref.Path)
		}
		wantVersion := "ao-catalog:" + providerID + ":sha256:" + ref.SHA256
		if ref.Version != wantVersion {
			return fmt.Errorf("provider %q has invalid version", providerID)
		}
	}
	return nil
}

func safeProviderPath(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.HasPrefix(value, "../") && !strings.Contains(value, `\`)
}

func buildProviderSnapshot(blob catalogProviderBlob, ref catalogProviderRef) (providerSnapshot, error) {
	if blob.SchemaVersion != runtimeSchemaVersion || blob.ProviderID != ref.ProviderID || len(blob.Models) != ref.ModelCount {
		return providerSnapshot{}, fmt.Errorf("invalid provider blob %q", ref.ProviderID)
	}
	provider := providerSnapshot{version: ref.Version, models: make(map[string]exactRates, len(blob.Models))}
	previous := ""
	for _, model := range blob.Models {
		canonicalID := CanonicalModelID(ref.ProviderID, model.ModelID)
		if canonicalID == "" || canonicalID != model.ModelID || canonicalID <= previous {
			return providerSnapshot{}, fmt.Errorf("provider %q has duplicate, unsorted, or noncanonical model %q", ref.ProviderID, model.ModelID)
		}
		previous = canonicalID
		rates, err := parseExactRates(model.Rates)
		if err != nil {
			return providerSnapshot{}, fmt.Errorf("provider %q model %q: %w", ref.ProviderID, model.ModelID, err)
		}
		provider.models[canonicalID] = rates
	}
	return provider, nil
}

func parseExactRates(rates catalogRates) (exactRates, error) {
	input, err := parseRate(rates.UncachedInputUSDPerToken)
	if err != nil {
		return exactRates{}, fmt.Errorf("uncached input rate: %w", err)
	}
	output, err := parseRate(rates.OutputUSDPerToken)
	if err != nil {
		return exactRates{}, fmt.Errorf("output rate: %w", err)
	}
	read, err := parseOptionalRate(rates.CacheReadUSDPerToken)
	if err != nil {
		return exactRates{}, fmt.Errorf("cache read rate: %w", err)
	}
	write, err := parseOptionalRate(rates.CacheWriteUSDPerToken)
	if err != nil {
		return exactRates{}, fmt.Errorf("cache write rate: %w", err)
	}
	write1H, err := parseOptionalRate(rates.CacheWrite1HUSDPerToken)
	if err != nil {
		return exactRates{}, fmt.Errorf("1h cache write rate: %w", err)
	}
	return exactRates{input: input, read: read, write: write, write1H: write1H, output: output}, nil
}

func parseOptionalRate(value *string) (*big.Rat, error) {
	if value == nil {
		return nil, nil
	}
	return parseRate(*value)
}

func parseRate(value string) (*big.Rat, error) {
	if !decimalPattern.MatchString(value) {
		return nil, fmt.Errorf("noncanonical nonnegative decimal %q", value)
	}
	rate, ok := new(big.Rat).SetString(value)
	if !ok || rate.Sign() < 0 {
		return nil, fmt.Errorf("invalid decimal %q", value)
	}
	return rate, nil
}

// ProviderVersion returns the exact active catalog version for a provider.
func (s *Snapshot) ProviderVersion(providerID string) string {
	if s == nil {
		return ""
	}
	provider, ok := s.providers[CanonicalProviderID(providerID)]
	if !ok {
		return ""
	}
	return provider.version
}

// ProviderForModel returns the one catalog provider that lists modelID, or ""
// when no provider does or more than one does.
//
// This is the last resort for an event nothing else could attribute: a Claude
// transcript names no provider, so a session collected before its first hook
// has only the served model name to go on. That name is a recorded fact — it is
// what the provider actually answered with — so a lookup is evidence rather
// than the harness-shaped guess the design forbids.
//
// Ambiguity resolves to "". Sentinels like "unknown" and "<synthetic>" appear in
// no catalog and fall out here for free, as does any model AO has no rates for,
// which is exactly the event that must stay unpriced anyway.
func (s *Snapshot) ProviderForModel(modelID string) string {
	if s == nil || strings.TrimSpace(modelID) == "" {
		return ""
	}
	found := ""
	for providerID, provider := range s.providers {
		// CanonicalModelID strips this provider's own prefix, so a stored
		// "anthropic/claude-opus-5" still matches its unprefixed catalog entry.
		if _, ok := provider.models[CanonicalModelID(providerID, modelID)]; !ok {
			continue
		}
		if found != "" {
			return ""
		}
		found = providerID
	}
	return found
}

// Estimate contains final per-component integer nano-USD estimates. A nil
// component is unknown. TotalNanos is present only when every component is
// known.
//
// InputNanos covers every non-cache-read input charge. Cache writes are input
// that happens to be written to the cache, so folding their charge in here is
// what keeps the three components disjoint and additive.
type Estimate struct {
	InputNanos       *int64
	CachedInputNanos *int64
	OutputNanos      *int64
	TotalNanos       *int64
	PricingVersion   string
}

// cacheWriteSplit is the provider-specific detail the neutral token vector
// deliberately folds away. Each field is nil when the stored provider usage
// object never carried it; a non-nil zero is a known zero.
type cacheWriteSplit struct {
	total *int64
	fiveM *int64
	oneH  *int64
}

// Estimate prices one normalized usage event against this immutable snapshot.
func (s *Snapshot) Estimate(event domain.ModelUsageEvent) (Estimate, error) {
	providerID := CanonicalProviderID(event.BillingProviderID)
	modelID := CanonicalModelID(providerID, event.ModelID)
	var rates exactRates
	version := ""
	if s != nil {
		if provider, ok := s.providers[providerID]; ok {
			version = provider.version
			rates = provider.models[modelID]
		}
	}
	for _, value := range []*int64{
		event.Tokens.UncachedInputTokens, event.Tokens.CachedInputTokens, event.Tokens.OutputTokens,
	} {
		if value != nil && *value < 0 {
			return Estimate{}, errors.New("usage tokens must be nonnegative")
		}
	}
	input, err := estimateInput(event, rates)
	if err != nil {
		return Estimate{}, fmt.Errorf("input cost: %w", err)
	}
	cached, err := chargeOptional(event.Tokens.CachedInputTokens, rates.read)
	if err != nil {
		return Estimate{}, fmt.Errorf("cached input cost: %w", err)
	}
	output, err := chargeOptional(event.Tokens.OutputTokens, rates.output)
	if err != nil {
		return Estimate{}, fmt.Errorf("output cost: %w", err)
	}
	estimate := Estimate{InputNanos: input, CachedInputNanos: cached, OutputNanos: output, PricingVersion: version}
	if input != nil && cached != nil && output != nil {
		total, err := checkedCostSum(*input, *cached, *output)
		if err != nil {
			return Estimate{}, err
		}
		estimate.TotalNanos = &total
	}
	return estimate, nil
}

// estimateInput charges every non-cache-read input token.
//
// A catalog without a cache-write rate for the model is not missing data: the
// provider does not bill writes separately, so the whole uncached bucket is
// charged at the plain input rate. Only when a distinct write rate exists — as
// Anthropic publishes, with its own five-minute and one-hour tiers — does the
// split matter, and only then can an unavailable split leave the cost unknown.
func estimateInput(event domain.ModelUsageEvent, rates exactRates) (*int64, error) {
	uncached := event.Tokens.UncachedInputTokens
	if rates.write == nil && rates.write1H == nil {
		return chargeOptional(uncached, rates.input)
	}
	if uncached == nil {
		return nil, nil
	}
	split := cacheWriteSplitFor(event)
	if split.total == nil || *split.total > *uncached {
		return nil, nil
	}
	write, err := chargeCacheWrite(split, rates)
	if err != nil || write == nil {
		return nil, err
	}
	fresh, err := charge(*uncached-*split.total, rates.input)
	if err != nil || fresh == nil {
		return nil, err
	}
	total, err := checkedCostSum(*fresh, *write)
	if err != nil {
		return nil, err
	}
	return costPointer(total), nil
}

func chargeCacheWrite(split cacheWriteSplit, rates exactRates) (*int64, error) {
	if *split.total == 0 {
		return costPointer(0), nil
	}
	// Without a distinct one-hour rate the tiers are indistinguishable, so the
	// whole write bucket takes the single write rate.
	if rates.write1H == nil {
		return charge(*split.total, rates.write)
	}
	if split.fiveM == nil || split.oneH == nil {
		return nil, nil
	}
	if *split.fiveM < 0 || *split.oneH < 0 ||
		*split.fiveM > math.MaxInt64-*split.oneH || *split.fiveM+*split.oneH != *split.total {
		return nil, nil
	}
	return combinedCharge([]tokenRate{{*split.fiveM, rates.write}, {*split.oneH, rates.write1H}})
}

// cacheWriteSplitFor reads the write buckets back out of the bounded provider
// usage object. An event stored before that object was captured, or one whose
// object omits the counters, simply reports an unknown split.
func cacheWriteSplitFor(event domain.ModelUsageEvent) cacheWriteSplit {
	if event.ProviderUsageJSON == "" {
		return cacheWriteSplit{}
	}
	switch event.ProviderID {
	case domain.UsageProviderAnthropic:
		var usage struct {
			CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5mInputTokens *int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1hInputTokens *int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		}
		if err := json.Unmarshal([]byte(event.ProviderUsageJSON), &usage); err != nil {
			return cacheWriteSplit{}
		}
		split := cacheWriteSplit{total: usage.CacheCreationInputTokens}
		if usage.CacheCreation != nil {
			split.fiveM = usage.CacheCreation.Ephemeral5mInputTokens
			split.oneH = usage.CacheCreation.Ephemeral1hInputTokens
		}
		return split
	case domain.UsageProviderOpenAI:
		// The neutral counters come from last_token_usage when Codex emits it,
		// so the write bucket must be read from the same per-event object rather
		// than from the cumulative totals beside it. When Codex emitted no such
		// object the parser derived the bucket from those cumulative readings
		// and stored it under its own key; without that fallback every
		// write-rated model prices its input as unknown.
		var usage struct {
			Last *struct {
				CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
			} `json:"last_token_usage"`
			Derived *int64 `json:"ao_derived_cache_write_input_tokens"`
		}
		if err := json.Unmarshal([]byte(event.ProviderUsageJSON), &usage); err != nil {
			return cacheWriteSplit{}
		}
		if usage.Last != nil {
			return cacheWriteSplit{total: usage.Last.CacheWriteInputTokens}
		}
		return cacheWriteSplit{total: usage.Derived}
	default:
		return cacheWriteSplit{}
	}
}

// chargeOptional prices an uncollected bucket as unknown rather than as zero.
func chargeOptional(tokens *int64, rate *big.Rat) (*int64, error) {
	if tokens == nil {
		return nil, nil
	}
	return charge(*tokens, rate)
}

type tokenRate struct {
	tokens int64
	rate   *big.Rat
}

func charge(tokens int64, rate *big.Rat) (*int64, error) {
	if tokens == 0 {
		return costPointer(0), nil
	}
	if rate == nil {
		return nil, nil
	}
	return roundNanoUSD(new(big.Rat).Mul(new(big.Rat).SetInt64(tokens), rate))
}

func combinedCharge(parts []tokenRate) (*int64, error) {
	total := new(big.Rat)
	for _, part := range parts {
		if part.tokens == 0 {
			continue
		}
		if part.rate == nil {
			return nil, nil
		}
		total.Add(total, new(big.Rat).Mul(new(big.Rat).SetInt64(part.tokens), part.rate))
	}
	return roundNanoUSD(total)
}

func roundNanoUSD(usd *big.Rat) (*int64, error) {
	nanos := new(big.Rat).Mul(usd, new(big.Rat).SetInt64(1_000_000_000))
	quotient, remainder := new(big.Int).QuoRem(nanos.Num(), nanos.Denom(), new(big.Int))
	twiceRemainder := new(big.Int).Lsh(remainder, 1)
	if twiceRemainder.Cmp(nanos.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() < 0 {
		return nil, errors.New("estimated cost overflows int64 nano-USD")
	}
	return costPointer(quotient.Int64()), nil
}

func checkedCostSum(values ...int64) (int64, error) {
	var result int64
	for _, value := range values {
		if value > math.MaxInt64-result {
			return 0, errors.New("estimated total cost overflows int64 nano-USD")
		}
		result += value
	}
	return result, nil
}

func costPointer(value int64) *int64 { return &value }

// ActivationFence serializes snapshot activation with ingestion/backfill
// critical sections while allowing waiting callers to cancel. Explicit FIFO
// admission prevents a newly arriving page from overtaking a catalog swap that
// is already waiting at the transaction boundary.
type ActivationFence struct {
	mu      sync.Mutex
	held    bool
	waiters []*activationFenceWaiter
}

type activationFenceWaiter struct {
	ready   chan struct{}
	granted bool
}

// NewActivationFence creates an unlocked activation fence.
func NewActivationFence() *ActivationFence {
	return &ActivationFence{}
}

// Acquire waits for exclusive admission or returns the context error. The
// returned release function is idempotent.
func (f *ActivationFence) Acquire(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	waiter := &activationFenceWaiter{ready: make(chan struct{})}
	f.mu.Lock()
	if !f.held && len(f.waiters) == 0 {
		f.held = true
		waiter.granted = true
		close(waiter.ready)
	} else {
		f.waiters = append(f.waiters, waiter)
	}
	f.mu.Unlock()

	// Recheck after registering so cancellation cannot strand a waiter, and so
	// release always observes the same FIFO order callers entered here.
	if err := ctx.Err(); err != nil {
		f.cancelOrRelease(waiter)
		return nil, err
	}
	select {
	case <-ctx.Done():
		f.cancelOrRelease(waiter)
		return nil, ctx.Err()
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			f.release()
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(f.release) }, nil
	}
}

func (f *ActivationFence) cancelOrRelease(waiter *activationFenceWaiter) {
	f.mu.Lock()
	if waiter.granted {
		f.mu.Unlock()
		f.release()
		return
	}
	for index, queued := range f.waiters {
		if queued == waiter {
			f.waiters = append(f.waiters[:index], f.waiters[index+1:]...)
			break
		}
	}
	f.mu.Unlock()
}

func (f *ActivationFence) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.waiters) == 0 {
		f.held = false
		return
	}
	next := f.waiters[0]
	f.waiters = f.waiters[1:]
	next.granted = true
	close(next.ready)
}

// ProviderActivation identifies one provider version made active by a swap.
type ProviderActivation struct {
	ProviderID      string
	PreviousVersion string
	Version         string
}

// Manager owns the current immutable snapshot and the shared activation fence.
type Manager struct {
	fence   *ActivationFence
	current atomic.Pointer[Snapshot]
}

// NewManager creates a manager with an empty snapshot unless initial is a
// complete validated catalog snapshot.
func NewManager(initial *Snapshot) *Manager {
	manager := &Manager{fence: NewActivationFence()}
	if !validSnapshot(initial) {
		initial = &Snapshot{providers: map[string]providerSnapshot{}}
	}
	manager.current.Store(initial)
	return manager
}

// Fence returns the shared fence Task 4 uses around durable usage writes.
func (m *Manager) Fence() *ActivationFence { return m.fence }

// Snapshot returns the current immutable snapshot.
func (m *Manager) Snapshot() *Snapshot { return m.current.Load() }

// WithSnapshot runs fn while holding the activation fence, guaranteeing that
// the returned snapshot cannot be superseded until fn completes.
func (m *Manager) WithSnapshot(ctx context.Context, fn func(*Snapshot) error) error {
	release, err := m.fence.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(m.current.Load())
}

// Activate atomically publishes a fully validated candidate under the fence.
func (m *Manager) Activate(ctx context.Context, candidate *Snapshot) ([]ProviderActivation, error) {
	if !validSnapshot(candidate) {
		return nil, errors.New("pricing snapshot is not a complete validated candidate")
	}
	release, err := m.fence.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	previous := m.current.Load()
	activations := changedProviders(previous, candidate)
	m.current.Store(candidate)
	return activations, nil
}

func validSnapshot(snapshot *Snapshot) bool {
	if snapshot == nil || len(snapshot.providers) != 3 {
		return false
	}
	for _, providerID := range []string{"anthropic", "openai", "zai"} {
		provider, ok := snapshot.providers[providerID]
		if !ok || len(provider.models) == 0 {
			return false
		}
		prefix := "ao-catalog:" + providerID + ":sha256:"
		if !strings.HasPrefix(provider.version, prefix) || !sha256Pattern.MatchString(strings.TrimPrefix(provider.version, prefix)) {
			return false
		}
	}
	return true
}

func changedProviders(previous, next *Snapshot) []ProviderActivation {
	activations := make([]ProviderActivation, 0, len(next.providers))
	for _, providerID := range []string{"anthropic", "openai", "zai"} {
		version := next.ProviderVersion(providerID)
		previousVersion := previous.ProviderVersion(providerID)
		if version != "" && version != previousVersion {
			activations = append(activations, ProviderActivation{ProviderID: providerID, PreviousVersion: previousVersion, Version: version})
		}
	}
	return activations
}
