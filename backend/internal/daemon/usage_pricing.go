package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	usagepipeline "github.com/aoagents/agent-orchestrator/backend/internal/observe/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type usagePricingRuntimeConfig struct {
	DataDir string
	Store   *sqlite.Store
	Fetcher pricing.CatalogFetcher
	Logger  *slog.Logger
}

type usagePricingRuntime struct {
	manager    *pricing.Manager
	backfiller *usagesvc.CostBackfiller
	repairer   *usagepipeline.LegacyRepairer
	refresher  *pricing.Refresher
	logger     *slog.Logger
	started    atomic.Bool
	cancel     context.CancelFunc
}

func newUsagePricingRuntime(config usagePricingRuntimeConfig) (*usagePricingRuntime, error) {
	if config.DataDir == "" || config.Store == nil {
		return nil, errors.New("usage pricing runtime requires data dir and store")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	fetcher := config.Fetcher
	if fetcher == nil {
		production, err := pricing.NewProductionFetcher(&http.Client{})
		if err != nil {
			return nil, err
		}
		fetcher = production
	}
	manager := pricing.NewManager(nil)
	backfiller := usagesvc.NewCostBackfiller(config.Store, usagesvc.CostBackfillerConfig{
		Manager: manager,
		OnError: func(err error) { logger.Warn("usage pricing backfill failed", "err", err) },
	})
	repairer := usagepipeline.NewLegacyRepairer(config.Store, manager, usagepipeline.LegacyRepairerConfig{
		OnError: func(err error) { logger.Warn("legacy usage pricing repair failed", "err", err) },
	})
	runtime := &usagePricingRuntime{manager: manager, backfiller: backfiller, repairer: repairer, logger: logger}
	refresher, err := pricing.NewRefresher(pricing.RefreshConfig{
		Cache:      pricing.NewCache(config.DataDir),
		Fetcher:    fetcher,
		Manager:    manager,
		OnActivate: runtime.onCatalogActivation,
		AfterInitialAttempt: func(ctx context.Context) {
			if err := repairer.Start(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("start legacy usage pricing repair", "err", err)
			}
		},
		OnError: func(err error) { logger.Warn("pricing catalog refresh failed", "err", err) },
	})
	if err != nil {
		return nil, err
	}
	runtime.refresher = refresher
	return runtime, nil
}

func (r *usagePricingRuntime) onCatalogActivation(ctx context.Context, activations []pricing.ProviderActivation) {
	// A later catalog can make a previously unknown model attributable. Those
	// rows are still provider-null, so the cost backfiller cannot select them;
	// replay attribution before relying on cost-only work.
	r.repairer.Repair()
	if err := r.backfiller.Enqueue(ctx, r.manager.Snapshot(), activations); err != nil && ctx.Err() == nil {
		r.logger.Warn("enqueue usage pricing backfill", "err", err)
	}
}

func (r *usagePricingRuntime) Manager() *pricing.Manager {
	if r == nil {
		return nil
	}
	return r.manager
}

// RepairLegacyAttribution asks for another historical repair pass because a
// binding just learned its billing route. Without it a Claude session collected
// before its first hook would stay unpriced until the next daemon start.
func (r *usagePricingRuntime) RepairLegacyAttribution() {
	if r == nil {
		return
	}
	r.repairer.Repair()
}

func (r *usagePricingRuntime) Start(ctx context.Context) error {
	if r == nil || r.manager == nil || r.backfiller == nil || r.repairer == nil || r.refresher == nil {
		return errors.New("usage pricing runtime is incomplete")
	}
	if !r.started.CompareAndSwap(false, true) {
		return errors.New("usage pricing runtime already started")
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	if err := r.backfiller.Start(runtimeCtx); err != nil {
		cancel()
		return err
	}
	// Start synchronously publishes a valid LKG before callers may construct or
	// start the transcript ingestion pipeline.
	if err := r.refresher.Start(runtimeCtx); err != nil {
		cancel()
		r.backfiller.Wait()
		return err
	}
	return nil
}

func (r *usagePricingRuntime) Wait() {
	if r == nil || !r.started.Load() {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	// Stop activation production before joining consumers and storage teardown.
	r.refresher.Wait()
	r.repairer.Wait()
	r.backfiller.Wait()
}
