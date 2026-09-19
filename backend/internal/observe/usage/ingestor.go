package usage

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

type ingestorStore interface {
	GetUsageSourceForIngestion(context.Context, int64) (domain.UsageSourceContext, bool, error)
}

// IngestorConfig retains the bounded-processing knobs of the transcript
// ingestor. opencode has no certified transcript pipeline, so no source is ever
// ingested; the fields are kept so wiring (pricing, legacy repair triggers)
// stays source-compatible.
type IngestorConfig struct {
	ChunkBytes       int64
	RecordBytes      int
	FinalizationWait time.Duration
	Clock            func() time.Time
	Pricing          *pricing.Manager
	OnPricingError   func(error)
	// RequestAttributionRepair asks for a legacy repair pass. See
	// notifyLateRouteEvidence for the one window it closes.
	RequestAttributionRepair func()
}

// IngestResult tells the coordinator whether another immediate chunk, source
// inventory refresh, or delayed retry is required.
type IngestResult struct {
	More                bool
	Refresh             bool
	SyncWatch           bool
	Reconcile           bool
	RetryAt             *time.Time
	ReplacementSourceID int64
}

// Ingestor always reports that a source is current: opencode has no certified
// transcript pipeline, so usage sources are never registered and there is
// nothing to ingest. Legacy provider sources left over from before the
// opencode-only transition are deliberately left untouched rather than replayed
// against a pipeline that no longer understands their format.
type Ingestor struct {
	store          ingestorStore
	now            func() time.Time
	pricing        *pricing.Manager
	onPricingError func(error)
	requestRepair  func()
}

// NewIngestor constructs the dormant transcript ingestor.
func NewIngestor(store ingestorStore, cfg IngestorConfig) *Ingestor {
	return &Ingestor{
		store:          store,
		now:            cfg.Clock,
		pricing:        cfg.Pricing,
		onPricingError: cfg.OnPricingError,
		requestRepair:  cfg.RequestAttributionRepair,
	}
}

// Ingest is a no-op for the opencode-only pipeline.
func (i *Ingestor) Ingest(ctx context.Context, sourceID int64) (IngestResult, error) {
	if err := ctx.Err(); err != nil {
		return IngestResult{}, err
	}
	if _, ok, err := i.store.GetUsageSourceForIngestion(ctx, sourceID); err != nil {
		return IngestResult{}, err
	} else if !ok {
		return IngestResult{}, nil
	}
	return IngestResult{}, nil
}
