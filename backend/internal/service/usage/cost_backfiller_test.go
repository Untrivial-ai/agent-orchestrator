package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

// Break caught: an unbounded or off-by-one worker page could monopolize the
// SQLite writer and make restart progress depend on map/row iteration order.
func TestCostBackfillerProcessesExact256RowPages(t *testing.T) {
	tests := []struct {
		count       int
		want        string
		wantAfterID string
		wantLists   int
	}{
		{count: 256, want: "[256]", wantAfterID: "[0 256]", wantLists: 2},
		{count: 257, want: "[256 1]", wantAfterID: "[0 256]", wantLists: 2},
		{count: 513, want: "[256 256 1]", wantAfterID: "[0 256 512]", wantLists: 3},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("rows_%d", test.count), func(t *testing.T) {
			snapshot := backfillTestSnapshot(t, "0.000001")
			version := snapshot.ProviderVersion("openai")
			store := newFakeBackfillStore(test.count)
			backfiller := NewCostBackfiller(store, CostBackfillerConfig{
				Manager: pricing.NewManager(snapshot),
				Clock:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
			})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := backfiller.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := backfiller.Enqueue(ctx, snapshot, []pricing.ProviderActivation{{
				ProviderID: "openai",
				Version:    version,
			}}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			<-store.complete
			for range test.wantLists {
				<-store.listed
			}
			cancel()
			backfiller.Wait()

			store.mu.Lock()
			defer store.mu.Unlock()
			if fmt.Sprint(store.batchSizes) != test.want {
				t.Fatalf("batch sizes = %v, want %s", store.batchSizes, test.want)
			}
			if fmt.Sprint(store.listAfterIDs) != test.wantAfterID {
				t.Fatalf("list cursors = %v, want %s", store.listAfterIDs, test.wantAfterID)
			}
			for _, candidate := range store.candidates {
				if candidate.PricingVersion != version {
					t.Fatalf("candidate %d version = %q, want %q", candidate.ID, candidate.PricingVersion, version)
				}
			}
		})
	}
}

// Break caught: an unavailable model could be retried repeatedly against the
// same catalog instead of recording that attempt and waiting for a new version.
func TestCostBackfillerRetriesUnavailableEventOnlyForNewVersion(t *testing.T) {
	firstSnapshot := backfillTestSnapshot(t, "0.000001")
	secondSnapshot := backfillTestSnapshot(t, "0.000002")
	firstVersion := firstSnapshot.ProviderVersion("openai")
	secondVersion := secondSnapshot.ProviderVersion("openai")
	store := newFakeBackfillStore(1)
	store.candidates[0].ModelID = "missing-model"
	manager := pricing.NewManager(firstSnapshot)
	backfiller := NewCostBackfiller(store, CostBackfillerConfig{Manager: manager})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := backfiller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := []pricing.ProviderActivation{{ProviderID: "openai", Version: firstVersion}}
	if err := backfiller.Enqueue(ctx, firstSnapshot, first); err != nil {
		t.Fatalf("enqueue first version: %v", err)
	}
	if got := <-store.applied; got != firstVersion {
		t.Fatalf("first applied version = %q", got)
	}
	if err := backfiller.Enqueue(ctx, firstSnapshot, first); err != nil {
		t.Fatalf("enqueue duplicate version: %v", err)
	}
	if _, err := manager.Activate(ctx, secondSnapshot); err != nil {
		t.Fatalf("activate second version: %v", err)
	}
	if err := backfiller.Enqueue(ctx, secondSnapshot, []pricing.ProviderActivation{{
		ProviderID: "openai", Version: secondVersion,
	}}); err != nil {
		t.Fatalf("enqueue second version: %v", err)
	}
	if got := <-store.applied; got != secondVersion {
		t.Fatalf("second applied version = %q", got)
	}
	cancel()
	backfiller.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if fmt.Sprint(store.listVersions) != fmt.Sprintf("[%s %s]", firstVersion, secondVersion) {
		t.Fatalf("listed versions = %v, want each activated version once", store.listVersions)
	}
	if store.candidates[0].PricingVersion != secondVersion {
		t.Fatalf("unavailable candidate version = %q, want %q", store.candidates[0].PricingVersion, secondVersion)
	}
}

// Break caught: removing a job from the pending map before processing meant a
// transient store error permanently dropped that provider/version until a
// daemon restart or a newer catalog activation.
func TestCostBackfillerRetriesTransientFailureForSameVersion(t *testing.T) {
	snapshot := backfillTestSnapshot(t, "0.000001")
	version := snapshot.ProviderVersion("openai")
	store := newFakeBackfillStore(1)
	store.listFailures = 1
	retryDelays := make(chan time.Duration, 1)
	reported := make(chan error, 1)
	backfiller := NewCostBackfiller(store, CostBackfillerConfig{
		Manager: pricing.NewManager(snapshot),
		OnError: func(err error) { reported <- err },
		RetryWait: func(ctx context.Context, delay time.Duration) error {
			retryDelays <- delay
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := backfiller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := backfiller.Enqueue(ctx, snapshot, []pricing.ProviderActivation{{
		ProviderID: "openai", Version: version,
	}}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case err := <-reported:
		if err == nil || err.Error() != "transient list failure" {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transient error was not reported")
	}
	select {
	case delay := <-retryDelays:
		if delay != time.Minute {
			t.Fatalf("first retry delay = %s, want 1m", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("same-version retry was not scheduled")
	}
	select {
	case <-store.complete:
	case <-time.After(time.Second):
		t.Fatal("same-version retry did not finish the candidate")
	}
	cancel()
	backfiller.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if fmt.Sprint(store.listVersions) != fmt.Sprintf("[%s %s]", version, version) {
		t.Fatalf("listed versions = %v, want same version retried once", store.listVersions)
	}
	if store.candidates[0].PricingVersion != version || !store.totalKnown[1] {
		t.Fatalf("candidate was not priced by retry: %+v", store.candidates[0])
	}
}

// Break caught: a newer activation could wait behind every page of an older
// provider job instead of superseding it at the next transaction boundary.
func TestCostBackfillerSupersedesActiveJobBetweenBatches(t *testing.T) {
	oldSnapshot := backfillTestSnapshot(t, "0.000001")
	newSnapshot := backfillTestSnapshot(t, "0.000002")
	oldVersion := oldSnapshot.ProviderVersion("openai")
	newVersion := newSnapshot.ProviderVersion("openai")
	store := newFakeBackfillStore(513)
	store.firstApplyEntered = make(chan struct{})
	store.releaseFirstApply = make(chan struct{})
	manager := pricing.NewManager(oldSnapshot)
	backfiller := NewCostBackfiller(store, CostBackfillerConfig{Manager: manager})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := backfiller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := backfiller.Enqueue(ctx, oldSnapshot, []pricing.ProviderActivation{{
		ProviderID: "openai", Version: oldVersion,
	}}); err != nil {
		t.Fatalf("enqueue old version: %v", err)
	}
	<-store.firstApplyEntered
	activationAdmission := make(chan struct{})
	activationCtx := &backfillFenceAdmissionContext{Context: ctx, admitted: activationAdmission}
	activated := make(chan error, 1)
	go func() {
		_, activateErr := manager.Activate(activationCtx, newSnapshot)
		if activateErr == nil {
			activateErr = backfiller.Enqueue(ctx, newSnapshot, []pricing.ProviderActivation{{
				ProviderID: "openai", Version: newVersion,
			}})
		}
		activated <- activateErr
	}()
	<-activationAdmission
	select {
	case err := <-activated:
		t.Fatalf("activation crossed active backfill page: %v", err)
	default:
	}
	close(store.releaseFirstApply)
	if err := <-activated; err != nil {
		t.Fatalf("activate/enqueue new version: %v", err)
	}
	<-store.complete
	cancel()
	backfiller.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if fmt.Sprint(store.batchSizes) != "[256 256 1]" {
		t.Fatalf("batch sizes = %v, want old 256 then new 256/1", store.batchSizes)
	}
	if fmt.Sprint(store.batchVersions) != fmt.Sprintf("[%s %s %s]", oldVersion, newVersion, newVersion) {
		t.Fatalf("batch versions = %v", store.batchVersions)
	}
	for index, candidate := range store.candidates {
		want := newVersion
		if index < 256 {
			want = oldVersion
		}
		if candidate.PricingVersion != want {
			t.Fatalf("candidate %d version = %q, want %q", candidate.ID, candidate.PricingVersion, want)
		}
	}
}

// Break caught: shutdown could wait forever for a provider page whose storage
// read was still blocked, or apply work after the shared lifecycle canceled.
func TestCostBackfillerCancellationInterruptsPendingPage(t *testing.T) {
	snapshot := backfillTestSnapshot(t, "0.000001")
	version := snapshot.ProviderVersion("openai")
	store := newFakeBackfillStore(1)
	store.listEntered = make(chan struct{})
	store.releaseList = make(chan struct{})
	backfiller := NewCostBackfiller(store, CostBackfillerConfig{Manager: pricing.NewManager(snapshot)})
	ctx, cancel := context.WithCancel(context.Background())
	if err := backfiller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := backfiller.Enqueue(ctx, snapshot, []pricing.ProviderActivation{{
		ProviderID: "openai", Version: version,
	}}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	<-store.listEntered
	cancel()
	backfiller.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.batchSizes) != 0 || store.candidates[0].PricingVersion != "" {
		t.Fatalf("canceled worker mutated rows: batches=%v candidate=%+v", store.batchSizes, store.candidates[0])
	}
}

// Break caught: in-memory duplicate suppression could make a daemon restart
// forget durable total-null rows left after the previous worker stopped.
func TestCostBackfillerRestartResumesSameVersionFromDurableFacts(t *testing.T) {
	snapshot := backfillTestSnapshot(t, "0.000001")
	version := snapshot.ProviderVersion("openai")
	store := newFakeBackfillStore(513)
	store.firstApplyEntered = make(chan struct{})
	store.releaseFirstApply = make(chan struct{})

	firstManager := pricing.NewManager(snapshot)
	first := NewCostBackfiller(store, CostBackfillerConfig{Manager: firstManager})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	if err := first.Start(firstCtx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	activation := []pricing.ProviderActivation{{ProviderID: "openai", Version: version}}
	if err := first.Enqueue(firstCtx, snapshot, activation); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	<-store.firstApplyEntered
	cancelFirst()
	close(store.releaseFirstApply)
	first.Wait()
	if got := <-store.applied; got != version {
		t.Fatalf("first worker applied version = %q", got)
	}

	store.firstApplyEntered = nil
	store.releaseFirstApply = nil
	second := NewCostBackfiller(store, CostBackfillerConfig{Manager: pricing.NewManager(snapshot)})
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	if err := second.Start(secondCtx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := second.Enqueue(secondCtx, snapshot, activation); err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}
	<-store.complete
	cancelSecond()
	second.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if fmt.Sprint(store.batchSizes) != "[256 256 1]" {
		t.Fatalf("restart batch sizes = %v, want [256 256 1]", store.batchSizes)
	}
	for _, candidate := range store.candidates {
		if candidate.PricingVersion != version {
			t.Fatalf("candidate %d version = %q, want %q", candidate.ID, candidate.PricingVersion, version)
		}
	}
}

type fakeBackfillStore struct {
	mu                sync.Mutex
	candidates        []domain.UsageCostCandidate
	batchSizes        []int
	batchVersions     []string
	listVersions      []string
	listAfterIDs      []int64
	listed            chan int64
	complete          chan struct{}
	applied           chan string
	doneOnce          sync.Once
	totalKnown        map[int64]bool
	firstApplyEntered chan struct{}
	releaseFirstApply chan struct{}
	firstApplyOnce    sync.Once
	listEntered       chan struct{}
	releaseList       chan struct{}
	listOnce          sync.Once
	listFailures      int
}

type backfillFenceAdmissionContext struct {
	context.Context
	admitted chan struct{}
	once     sync.Once
}

func (c *backfillFenceAdmissionContext) Err() error {
	c.once.Do(func() { close(c.admitted) })
	return c.Context.Err()
}

func newFakeBackfillStore(count int) *fakeBackfillStore {
	store := &fakeBackfillStore{
		complete:   make(chan struct{}),
		listed:     make(chan int64, 16),
		applied:    make(chan string, 16),
		totalKnown: make(map[int64]bool),
	}
	for index := 0; index < count; index++ {
		store.candidates = append(store.candidates, domain.UsageCostCandidate{
			ID:                int64(index + 1),
			BindingID:         1,
			ProviderID:        domain.UsageProviderOpenAI,
			BillingProviderID: "OpenAI",
			ModelID:           "gpt-test",
			MeasurementKind:   domain.UsageMeasurementNativeReported,
			Tokens:            testUsageMetrics(1, 0, 1, 0),
			ProviderUsageJSON: `{"last_token_usage":{"cache_write_input_tokens":0}}`,
			SourceEventKey:    fmt.Sprintf("event-%03d", index),
		})
	}
	return store
}

func (s *fakeBackfillStore) ListUsageCostCandidates(
	ctx context.Context,
	providerID, version string,
	afterID int64,
) ([]domain.UsageCostCandidate, error) {
	if s.listEntered != nil {
		s.listOnce.Do(func() { close(s.listEntered) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.releaseList:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listVersions = append(s.listVersions, version)
	s.listAfterIDs = append(s.listAfterIDs, afterID)
	s.listed <- afterID
	if s.listFailures > 0 {
		s.listFailures--
		return nil, errors.New("transient list failure")
	}
	batch := make([]domain.UsageCostCandidate, 0, 256)
	for _, candidate := range s.candidates {
		if candidate.ID > afterID && !s.totalKnown[candidate.ID] && pricing.CanonicalProviderID(candidate.BillingProviderID) == providerID &&
			candidate.PricingVersion != version && len(batch) < 256 {
			batch = append(batch, candidate)
		}
	}
	return batch, nil
}

func (s *fakeBackfillStore) ApplyUsageCostUpdates(
	ctx context.Context,
	updates []domain.UsageCostUpdate,
	_ time.Time,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.firstApplyEntered != nil {
		s.firstApplyOnce.Do(func() {
			close(s.firstApplyEntered)
			<-s.releaseFirstApply
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchSizes = append(s.batchSizes, len(updates))
	if len(updates) > 0 {
		s.batchVersions = append(s.batchVersions, updates[0].Costs.PricingVersion)
	}
	applied := 0
	for _, update := range updates {
		for index := range s.candidates {
			candidate := &s.candidates[index]
			if candidate.ID != update.Candidate.ID || candidate.PricingVersion != update.Candidate.PricingVersion {
				continue
			}
			candidate.PricingVersion = update.Costs.PricingVersion
			s.totalKnown[candidate.ID] = update.Costs.EstimatedCostNanos != nil
			applied++
			break
		}
	}
	remaining := false
	if len(updates) > 0 {
		version := updates[0].Costs.PricingVersion
		for _, candidate := range s.candidates {
			if !s.totalKnown[candidate.ID] && candidate.PricingVersion != version {
				remaining = true
				break
			}
		}
	}
	if !remaining {
		s.doneOnce.Do(func() { close(s.complete) })
	}
	if len(updates) > 0 {
		s.applied <- updates[0].Costs.PricingVersion
	}
	return applied, nil
}

func backfillTestSnapshot(t *testing.T, openAIInputRate string) *pricing.Snapshot {
	t.Helper()
	type providerModel struct {
		provider string
		model    string
		input    string
	}
	models := []providerModel{
		{provider: "anthropic", model: "claude-test", input: "0"},
		{provider: "openai", model: "gpt-test", input: openAIInputRate},
		{provider: "zai", model: "glm-test", input: "0"},
	}
	providers := make(map[string][]byte, 3)
	refs := make([]map[string]any, 0, 3)
	for _, model := range models {
		blob, err := json.Marshal(map[string]any{
			"schemaVersion": 1,
			"providerId":    model.provider,
			"models": []any{map[string]any{
				"modelId": model.model,
				"rates": map[string]any{
					"uncachedInputUsdPerToken": model.input,
					"outputUsdPerToken":        "0",
				},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		blob = append(blob, '\n')
		digest := sha256.Sum256(blob)
		hash := hex.EncodeToString(digest[:])
		path := "providers/" + model.provider + "/" + hash + ".json"
		providers[path] = blob
		refs = append(refs, map[string]any{
			"providerId": model.provider,
			"version":    "ao-catalog:" + model.provider + ":sha256:" + hash,
			"sha256":     hash,
			"path":       path,
			"modelCount": 1,
		})
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"source": map[string]any{
			"repository": "BerriAI/litellm",
			"revision":   "0123456789abcdef0123456789abcdef01234567",
			"path":       "model_prices_and_context_window.json",
		},
		"providers": refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pricing.DecodeCandidate(append(manifest, '\n'), providers)
	if err != nil {
		t.Fatalf("DecodeCandidate fixture: %v", err)
	}
	return snapshot
}
