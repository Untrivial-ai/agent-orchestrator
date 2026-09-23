package pricing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefresherPublishesLKGImmediatelyButDelaysActivationsUntilRemoteAttempt(t *testing.T) {
	dataDir := t.TempDir()
	cache := NewCache(dataDir)
	oldModels := testBaseModels("0.1")
	oldFixture := testCandidate(t, oldModels)
	oldCatalog, err := ParseCatalog(oldFixture.manifest, oldFixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), oldCatalog); err != nil {
		t.Fatal(err)
	}

	newModels := testBaseModels("0.1")
	newModels["openai"] = []testModel{{ID: "gpt-test", Input: "0.2", Output: "0.2"}}
	newFixture := testCandidate(t, newModels)
	newCatalog, err := ParseCatalog(newFixture.manifest, newFixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	releaseFetch := make(chan struct{})
	fetcher := &fakeCatalogFetcher{fetch: func(ctx context.Context, _ string, cacheAvailable bool) (FetchResult, error) {
		if !cacheAvailable {
			t.Error("first refresh did not report loaded LKG")
		}
		select {
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		case <-releaseFetch:
			return FetchResult{Catalog: newCatalog, ETag: `"new"`}, nil
		}
	}}
	manager := NewManager(nil)
	delivered := make(chan []ProviderActivation, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresher, err := NewRefresher(RefreshConfig{
		Cache: cache, Fetcher: fetcher, Manager: manager,
		OnActivate: func(_ context.Context, activations []ProviderActivation) { delivered <- activations },
		Wait:       blockUntilCanceled,
		Jitter:     func(interval time.Duration) time.Duration { return interval },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().ProviderVersion("anthropic"); got != oldFixture.versions["anthropic"] {
		t.Fatalf("snapshot before remote attempt = %q, want LKG", got)
	}
	select {
	case got := <-delivered:
		t.Fatalf("cached activations delivered before remote attempt: %#v", got)
	default:
	}
	close(releaseFetch)
	var activations []ProviderActivation
	select {
	case activations = <-delivered:
	case <-time.After(time.Second):
		t.Fatal("activation delivery timed out")
	}
	assertActivationVersions(t, activations, map[string]string{
		"anthropic": oldFixture.versions["anthropic"],
		"openai":    newFixture.versions["openai"],
		"zai":       oldFixture.versions["zai"],
	})
	for _, activation := range activations {
		if activation.ProviderID == "openai" && activation.PreviousVersion != oldFixture.versions["openai"] {
			t.Fatalf("remote openai activation = %#v, cached activation was not superseded", activation)
		}
	}
	cancel()
	refresher.Wait()
}

// Break caught: legacy repair could start as soon as the LKG was published and
// permanently price historical rows before the first remote refresh activated
// a newer reviewed catalog.
func TestRefresherRunsAfterInitialAttemptOnlyAfterActivationDelivery(t *testing.T) {
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	fetched := make(chan struct{})
	fetcher := &fakeCatalogFetcher{fetch: func(context.Context, string, bool) (FetchResult, error) {
		close(fetched)
		return FetchResult{Catalog: catalog, ETag: `"new"`}, nil
	}}
	var activated atomic.Bool
	afterInitial := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{
		Cache: NewCache(t.TempDir()), Fetcher: fetcher, Manager: NewManager(nil),
		OnActivate: func(context.Context, []ProviderActivation) {
			activated.Store(true)
		},
		AfterInitialAttempt: func(context.Context) {
			if !activated.Load() {
				t.Error("post-initial hook ran before remote activation delivery")
			}
			close(afterInitial)
		},
		Wait: blockUntilCanceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-afterInitial:
	case <-time.After(time.Second):
		t.Fatal("post-initial hook did not run")
	}
	select {
	case <-fetched:
	default:
		t.Fatal("post-initial hook ran before the remote fetch")
	}
	cancel()
	refresher.Wait()
}

func TestRefresherFailureReleasesCachedActivationsAndRetriesExponentially(t *testing.T) {
	cache := NewCache(t.TempDir())
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int64
	fetcher := &fakeCatalogFetcher{fetch: func(context.Context, string, bool) (FetchResult, error) {
		attempt := attempts.Add(1)
		if attempt <= 3 {
			return FetchResult{}, errors.New("offline")
		}
		return FetchResult{NotModified: true, ETag: `"same"`}, nil
	}}
	delivered := make(chan []ProviderActivation, 1)
	durations := make(chan time.Duration, 4)
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{
		Cache: cache, Fetcher: fetcher, Manager: NewManager(nil),
		OnActivate: func(_ context.Context, activations []ProviderActivation) { delivered <- activations },
		Wait: func(ctx context.Context, duration time.Duration) error {
			durations <- duration
			if duration == RefreshInterval {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		Jitter: func(interval time.Duration) time.Duration { return interval },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case activations := <-delivered:
		if len(activations) != 3 {
			t.Fatalf("cached activations = %#v", activations)
		}
	case <-time.After(time.Second):
		t.Fatal("cached activations not released after failure")
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, RefreshInterval}
	for _, expected := range want {
		select {
		case got := <-durations:
			if got != expected {
				t.Fatalf("wait = %s, want %s", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("wait %s timed out", expected)
		}
	}
	cancel()
	refresher.Wait()
}

func TestRefresherReportsInternalDeadlineWhileParentContextIsLive(t *testing.T) {
	fetcher := &fakeCatalogFetcher{fetch: func(context.Context, string, bool) (FetchResult, error) {
		return FetchResult{}, context.DeadlineExceeded
	}}
	errorsReported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{
		Cache: NewCache(t.TempDir()), Fetcher: fetcher, Manager: NewManager(nil),
		OnError: func(err error) { errorsReported <- err },
		Wait:    blockUntilCanceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case reported := <-errorsReported:
		if !errors.Is(reported, context.DeadlineExceeded) {
			t.Fatalf("reported error = %v, want deadline exceeded", reported)
		}
	case <-time.After(time.Second):
		t.Fatal("internal deadline was not reported")
	}
	cancel()
	refresher.Wait()
}

func TestRefresherRetryCapsAndSuccessResetsBackoff(t *testing.T) {
	var attempts atomic.Int64
	fetcher := &fakeCatalogFetcher{fetch: func(context.Context, string, bool) (FetchResult, error) {
		attempt := attempts.Add(1)
		switch {
		case attempt <= 8:
			return FetchResult{}, errors.New("offline")
		case attempt == 9:
			return FetchResult{NotModified: true}, nil
		default:
			return FetchResult{}, errors.New("offline again")
		}
	}}
	durations := make(chan time.Duration, 10)
	var waits atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{
		Cache: NewCache(t.TempDir()), Fetcher: fetcher, Manager: NewManager(nil),
		Wait: func(ctx context.Context, duration time.Duration) error {
			durations <- duration
			if waits.Add(1) == 10 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		Jitter: func(interval time.Duration) time.Duration { return interval },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
		RefreshInterval, time.Minute,
	}
	for _, expected := range want {
		select {
		case got := <-durations:
			if got != expected {
				t.Fatalf("wait = %s, want %s", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("wait %s timed out", expected)
		}
	}
	cancel()
	refresher.Wait()
}

func TestRefresherNeverOverlapsAndStopsBlockedFetchOnShutdown(t *testing.T) {
	var active, maximum atomic.Int64
	started := make(chan struct{})
	fetcher := &fakeCatalogFetcher{fetch: func(ctx context.Context, _ string, _ bool) (FetchResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		close(started)
		<-ctx.Done()
		return FetchResult{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{Cache: NewCache(t.TempDir()), Fetcher: fetcher, Manager: NewManager(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup fetch did not begin")
	}
	cancel()
	done := make(chan struct{})
	go func() { refresher.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresher shutdown timed out")
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent fetches = %d", got)
	}
}

func TestRefresherShutdownDoesNotDeliverPendingCachedActivation(t *testing.T) {
	cache := NewCache(t.TempDir())
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	fetcher := &fakeCatalogFetcher{fetch: func(ctx context.Context, _ string, _ bool) (FetchResult, error) {
		close(started)
		<-ctx.Done()
		return FetchResult{}, ctx.Err()
	}}
	delivered := make(chan []ProviderActivation, 1)
	ctx, cancel := context.WithCancel(context.Background())
	refresher, err := NewRefresher(RefreshConfig{
		Cache: cache, Fetcher: fetcher, Manager: NewManager(nil),
		OnActivate: func(_ context.Context, activations []ProviderActivation) { delivered <- activations },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	refresher.Wait()
	select {
	case activations := <-delivered:
		t.Fatalf("activation delivered during shutdown: %#v", activations)
	default:
	}
}

func TestRefreshJitterStaysWithinFivePercent(t *testing.T) {
	for _, tc := range []struct {
		sample float64
		want   time.Duration
	}{{0, 22*time.Hour + 48*time.Minute}, {0.5, 24 * time.Hour}, {1, 25*time.Hour + 12*time.Minute}} {
		if got := jitterInterval(RefreshInterval, tc.sample); got != tc.want {
			t.Fatalf("jitterInterval(%v) = %s, want %s", tc.sample, got, tc.want)
		}
	}
}

type fakeCatalogFetcher struct {
	mu    sync.Mutex
	fetch func(context.Context, string, bool) (FetchResult, error)
}

func (f *fakeCatalogFetcher) Fetch(ctx context.Context, etag string, cacheAvailable bool) (FetchResult, error) {
	f.mu.Lock()
	fn := f.fetch
	f.mu.Unlock()
	return fn(ctx, etag, cacheAvailable)
}

func blockUntilCanceled(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func assertActivationVersions(t *testing.T, got []ProviderActivation, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("activations = %#v", got)
	}
	for _, activation := range got {
		if want[activation.ProviderID] != activation.Version {
			t.Fatalf("activation = %#v, want version %q", activation, want[activation.ProviderID])
		}
	}
}
