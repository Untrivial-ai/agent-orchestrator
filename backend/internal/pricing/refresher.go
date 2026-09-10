package pricing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// RefreshInterval is the successful catalog polling interval before jitter.
	RefreshInterval = 24 * time.Hour
	// RetryInitial is the first delay after a failed refresh.
	RetryInitial = time.Minute
	// RetryMaximum caps the exponential failure retry delay.
	RetryMaximum = time.Hour
)

// CatalogFetcher is the injected remote transport boundary.
type CatalogFetcher interface {
	Fetch(context.Context, string, bool) (FetchResult, error)
}

// RefreshConfig supplies runtime dependencies and observable activation hooks.
// Wait and Jitter are optional deterministic timing seams used by offline tests.
type RefreshConfig struct {
	Cache               *Cache
	Fetcher             CatalogFetcher
	Manager             *Manager
	OnActivate          func(context.Context, []ProviderActivation)
	AfterInitialAttempt func(context.Context)
	OnError             func(error)
	Wait                func(context.Context, time.Duration) error
	Jitter              func(time.Duration) time.Duration
}

// Refresher synchronously publishes LKG state, then owns one non-overlapping
// asynchronous remote refresh loop.
type Refresher struct {
	config  RefreshConfig
	started atomic.Bool
	done    chan struct{}

	mu   sync.Mutex
	etag string
}

// NewRefresher validates dependencies and applies production timing defaults.
func NewRefresher(config RefreshConfig) (*Refresher, error) {
	if config.Cache == nil || config.Fetcher == nil || config.Manager == nil {
		return nil, errors.New("pricing refresher requires cache, fetcher, and manager")
	}
	if config.OnActivate == nil {
		config.OnActivate = func(context.Context, []ProviderActivation) {}
	}
	if config.AfterInitialAttempt == nil {
		config.AfterInitialAttempt = func(context.Context) {}
	}
	if config.OnError == nil {
		config.OnError = func(error) {}
	}
	if config.Wait == nil {
		config.Wait = waitContext
	}
	if config.Jitter == nil {
		config.Jitter = func(interval time.Duration) time.Duration {
			return jitterInterval(interval, rand.Float64()) //nolint:gosec // Poll jitter is not security-sensitive.
		}
	}
	return &Refresher{config: config, done: make(chan struct{})}, nil
}

// Start synchronously loads and publishes a valid LKG snapshot, then launches
// the immediate remote attempt. LKG activation delivery remains pending until
// that first attempt completes.
func (r *Refresher) Start(ctx context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return errors.New("pricing refresher already started")
	}
	var pending []ProviderActivation
	cacheAvailable := false
	catalog, err := r.config.Cache.Load(ctx)
	if err == nil {
		pending, err = r.config.Manager.Activate(ctx, catalog.Snapshot())
		if err != nil {
			close(r.done)
			return err
		}
		cacheAvailable = true
	} else if !errors.Is(err, ErrNoCachedCatalog) {
		r.config.OnError(fmt.Errorf("load pricing LKG: %w", err))
	}
	go r.run(ctx, cacheAvailable, pending)
	return nil
}

// Wait blocks until cancellation has stopped the refresher goroutine.
func (r *Refresher) Wait() {
	if !r.started.Load() {
		return
	}
	<-r.done
}

func (r *Refresher) run(ctx context.Context, cacheAvailable bool, pending []ProviderActivation) {
	defer close(r.done)
	retryDelay := RetryInitial
	firstAttempt := true
	for {
		success, nowCached, remoteActivations, err := r.refresh(ctx, cacheAvailable)
		cacheAvailable = cacheAvailable || nowCached
		if firstAttempt {
			merged := mergeInitialActivations(pending, remoteActivations)
			if ctx.Err() == nil && len(merged) > 0 {
				r.config.OnActivate(ctx, merged)
			}
			pending = nil
			firstAttempt = false
			if ctx.Err() == nil {
				r.config.AfterInitialAttempt(ctx)
			}
		} else if ctx.Err() == nil && len(remoteActivations) > 0 {
			r.config.OnActivate(ctx, remoteActivations)
		}
		if err != nil && ctx.Err() == nil {
			r.config.OnError(err)
		}
		if ctx.Err() != nil {
			return
		}
		var delay time.Duration
		if success {
			retryDelay = RetryInitial
			delay = r.config.Jitter(RefreshInterval)
		} else {
			delay = retryDelay
			if retryDelay < RetryMaximum {
				retryDelay *= 2
				if retryDelay > RetryMaximum {
					retryDelay = RetryMaximum
				}
			}
		}
		if err := r.config.Wait(ctx, delay); err != nil {
			return
		}
	}
}

func (r *Refresher) refresh(ctx context.Context, cacheAvailable bool) (success, nowCached bool, activations []ProviderActivation, err error) {
	r.mu.Lock()
	etag := r.etag
	r.mu.Unlock()
	result, err := r.config.Fetcher.Fetch(ctx, etag, cacheAvailable)
	if err != nil {
		return false, false, nil, fmt.Errorf("refresh pricing catalog: %w", err)
	}
	if result.NotModified {
		return true, false, nil, nil
	}
	if result.Catalog == nil {
		return false, false, nil, errors.New("refresh pricing catalog returned no catalog")
	}
	if err := r.config.Cache.Install(ctx, result.Catalog); err != nil {
		return false, false, nil, fmt.Errorf("install pricing catalog: %w", err)
	}
	activations, err = r.config.Manager.Activate(ctx, result.Catalog.Snapshot())
	if err != nil {
		return false, true, nil, fmt.Errorf("activate pricing catalog: %w", err)
	}
	r.mu.Lock()
	r.etag = result.ETag
	r.mu.Unlock()
	return true, true, activations, nil
}

func mergeInitialActivations(cached, remote []ProviderActivation) []ProviderActivation {
	byProvider := make(map[string]ProviderActivation, len(cached)+len(remote))
	for _, activation := range cached {
		byProvider[activation.ProviderID] = activation
	}
	for _, activation := range remote {
		byProvider[activation.ProviderID] = activation
	}
	merged := make([]ProviderActivation, 0, len(byProvider))
	for _, providerID := range []string{"anthropic", "openai", "zai"} {
		if activation, ok := byProvider[providerID]; ok {
			merged = append(merged, activation)
		}
	}
	return merged
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func jitterInterval(interval time.Duration, sample float64) time.Duration {
	if sample < 0 {
		sample = 0
	} else if sample > 1 {
		sample = 1
	}
	factor := 0.95 + sample*0.10
	return time.Duration(float64(interval) * factor)
}
