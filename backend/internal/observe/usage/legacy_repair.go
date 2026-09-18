package usage

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

const (
	defaultChunkBytes    = 8 << 20
	defaultRecordBytes   = 1 << 20
	defaultRaceRetry     = 30 * time.Second
	maxRaceRetry         = 5 * time.Minute
	defaultStartupSettle = 30 * time.Second
)

type legacyRepairStore interface {
	ListLegacyUsageSources(context.Context) ([]domain.UsageSourceContext, error)
}

// LegacyRepairerConfig controls bounded replay and lifecycle diagnostics.
type LegacyRepairerConfig struct {
	ChunkBytes  int64
	RecordBytes int
	Clock       func() time.Time
	OnError     func(error)
	// RaceRetry is the first delay before retrying a pass that lost its cursor
	// guard to concurrent ingestion. It doubles up to maxRaceRetry.
	RaceRetry time.Duration
	// StartupSettle delays one follow-up pass after the first. Startup
	// ingestion begins after the repairer does, so the first pass can read the
	// table before the rows it exists to repair are in it.
	StartupSettle time.Duration
}

// LegacyRepairer is the dormant historical-attribution repairer. opencode has
// no certified transcript pipeline, so there are no transcript formats to
// replay for attribution: legacy provider rows left over from before the
// opencode-only transition are visited and left untouched. The lifecycle
// (startup pass, Repair triggers, non-overlapping passes, graceful Wait) is
// preserved so daemon wiring keeps a single, bounded repair schedule.
type LegacyRepairer struct {
	store   legacyRepairStore
	pricing *pricing.Manager
	config  LegacyRepairerConfig
	started atomic.Bool
	trigger chan struct{}
	done    chan struct{}
}

// NewLegacyRepairer constructs a historical attribution repairer.
func NewLegacyRepairer(
	store legacyRepairStore,
	manager *pricing.Manager,
	config LegacyRepairerConfig,
) *LegacyRepairer {
	if config.ChunkBytes <= 0 {
		config.ChunkBytes = defaultChunkBytes
	}
	if config.RecordBytes <= 0 {
		config.RecordBytes = defaultRecordBytes
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.OnError == nil {
		config.OnError = func(error) {}
	}
	if config.RaceRetry <= 0 {
		config.RaceRetry = defaultRaceRetry
	}
	if config.StartupSettle <= 0 {
		config.StartupSettle = defaultStartupSettle
	}
	return &LegacyRepairer{
		store: store, pricing: manager, config: config,
		trigger: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

// Start runs the first repair pass and then serves Repair requests until the
// context is cancelled. Passes never overlap, so one long scan cannot be
// stacked on top of itself by a burst of hooks.
func (r *LegacyRepairer) Start(ctx context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return errors.New("legacy usage repairer already started")
	}
	if r.store == nil {
		return errors.New("legacy usage repairer requires store")
	}
	go func() {
		defer close(r.done)
		var settleTimer *time.Timer
		defer func() {
			if settleTimer != nil {
				settleTimer.Stop()
			}
		}()
		retry := r.config.RaceRetry
		startupPending := true
		for {
			raced, err := r.run(ctx)
			if err != nil && ctx.Err() == nil {
				r.config.OnError(err)
			}
			if settleTimer != nil {
				settleTimer.Stop()
				settleTimer = nil
			}
			// A pass that lost its cursor guard is not finished, it was
			// outrun. Backing off lets a busy source settle instead of
			// spinning on it; a clean pass resets the delay.
			var settle <-chan time.Time
			switch {
			case raced && ctx.Err() == nil:
				settleTimer = time.NewTimer(retry)
				settle = settleTimer.C
				retry *= 2
				if retry > maxRaceRetry {
					retry = maxRaceRetry
				}
			case startupPending && ctx.Err() == nil:
				startupPending = false
				settleTimer = time.NewTimer(r.config.StartupSettle)
				settle = settleTimer.C
				retry = r.config.RaceRetry
			default:
				retry = r.config.RaceRetry
			}
			select {
			case <-ctx.Done():
				return
			case <-r.trigger:
			case <-settle:
			}
		}
	}()
	return nil
}

// Repair asks for another pass because new attribution evidence exists. It
// never blocks: a pass already queued absorbs this request, so a busy session
// cannot queue one scan per hook.
func (r *LegacyRepairer) Repair() {
	if r == nil {
		return
	}
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Wait joins the repair loop. Callers cancel the start context first.
func (r *LegacyRepairer) Wait() {
	if !r.started.Load() {
		return
	}
	<-r.done
}

// Run performs one synchronous repair pass.
func (r *LegacyRepairer) Run(ctx context.Context) error {
	_, err := r.run(ctx)
	return err
}

// run visits legacy provider rows. There is no certified transcript pipeline
// to replay them against, so they are deliberately left untouched.
func (r *LegacyRepairer) run(ctx context.Context) (raced bool, err error) {
	if r.store == nil {
		return false, errors.New("legacy usage repairer requires store")
	}
	sources, err := r.store.ListLegacyUsageSources(ctx)
	if err != nil {
		return false, err
	}
	_ = sources
	return false, nil
}