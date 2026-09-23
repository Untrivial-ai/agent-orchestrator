package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
)

type legacyRepairStore interface {
	ListLegacyUsageSources(context.Context) ([]domain.UsageSourceContext, error)
	ListLegacyUsageEvents(context.Context, int64) ([]domain.LegacyUsageEvent, error)
	ApplyLegacyUsageRepairs(context.Context, []domain.LegacyUsageRepair, time.Time) (int, error)
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

const (
	defaultRaceRetry     = 30 * time.Second
	maxRaceRetry         = 5 * time.Minute
	defaultStartupSettle = 30 * time.Second
)

// LegacyRepairer repairs provider-null historical rows: once at startup, and
// again whenever Repair reports that new attribution evidence has arrived.
//
// The second trigger matters for Claude. A Claude transcript carries no
// provider of its own, so its billing identity comes only from the route hint
// on the binding, and that hint only appears once a hook runs. A session
// collected before the hint existed would otherwise stay unattributed until the
// next daemon start, however long that is.
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
	if r.store == nil || r.pricing == nil {
		return errors.New("legacy usage repairer requires store and pricing manager")
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
		// The daemon starts pricing before it starts ingesting, so the first
		// pass can read the table before startup ingestion has written to it.
		// A hookless source registered afterwards has no hook coming to fire a
		// trigger, so without this it waits for the next daemon start.
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

// Run performs one synchronous repair pass. Individual unverifiable sources
// are intentionally skipped without mutating their lifecycle or cursor.
func (r *LegacyRepairer) Run(ctx context.Context) error {
	_, err := r.run(ctx)
	return err
}

// run reports whether any repair lost its cursor guard to concurrent ingestion,
// which means the source still owes work that a later pass can finish.
func (r *LegacyRepairer) run(ctx context.Context) (raced bool, err error) {
	if r.store == nil || r.pricing == nil {
		return false, errors.New("legacy usage repairer requires store and pricing manager")
	}
	sources, err := r.store.ListLegacyUsageSources(ctx)
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return raced, err
		}
		if err := r.repairSource(ctx, source, &raced); err != nil {
			if ctx.Err() != nil {
				return raced, ctx.Err()
			}
			r.config.OnError(err)
		}
	}
	return raced, nil
}

func (r *LegacyRepairer) repairSource(ctx context.Context, source domain.UsageSourceContext, raced *bool) error {
	if source.Source.State == domain.UsageSourceComplete &&
		source.Source.LastErrorCode == domain.UsageErrorArtifactReplaced {
		return nil
	}
	persisted, err := decodeParserState(source.Source)
	if err != nil || source.Source.ByteOffset <= 0 || persisted.Integrity.Checkpoint == nil {
		return nil
	}
	candidates, err := r.store.ListLegacyUsageEvents(ctx, source.Source.ID)
	if err != nil || len(candidates) == 0 {
		return err
	}
	if !replayCanImprove(source, candidates) {
		return nil
	}
	file, err := os.Open(source.Source.ArtifactPath) //nolint:gosec // registered transcript path.
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	if !legacyArtifactMatches(file, source.Source, persisted.Integrity.Checkpoint) {
		return nil
	}

	state, err := newParserState(source.Source.Kind)
	if err != nil {
		return nil
	}
	if state.Codex != nil && persisted.Codex != nil {
		state.Codex.NativeSessionID = persisted.Codex.NativeSessionID
		state.Codex.DirectParentID = persisted.Codex.DirectParentID
	}
	replayed := newLegacyReplayIndex(candidates)
	ok, err := r.reparseDurablePrefix(ctx, file, source, state, replayed)
	if err != nil {
		return err
	}
	if !ok || !legacyArtifactMatches(file, source.Source, persisted.Integrity.Checkpoint) {
		return nil
	}

	matches := matchLegacyEvents(candidates, replayed)
	if len(matches) == 0 {
		return nil
	}
	return r.pricing.WithSnapshot(ctx, func(snapshot *pricing.Snapshot) error {
		repairs := make([]domain.LegacyUsageRepair, 0, min(len(matches), 256))
		flush := func() error {
			if len(repairs) == 0 {
				return nil
			}
			applied, err := r.store.ApplyLegacyUsageRepairs(ctx, repairs, r.config.Clock().UTC())
			// Every repair is guarded on the source cursor it was derived from,
			// so ingestion advancing mid-pass drops it. That is correct — the
			// prefix it read is no longer the prefix on disk — but silently
			// giving up would strand an always-active session forever.
			if applied < len(repairs) {
				*raced = true
			}
			repairs = repairs[:0]
			return err
		}
		for _, match := range matches {
			if err := ctx.Err(); err != nil {
				return err
			}
			event := match.event
			// The replay reads the binding's route hint too, so a nonempty
			// provider here was named outright by one of the two.
			billingProviderID := strings.TrimSpace(event.BillingProviderID)
			providerSource := domain.UsageBillingProviderObserved
			if billingProviderID == "" {
				// Inference cannot improve on inference: re-deriving the same
				// provider from the same model rewrites nothing and would churn
				// the row on every pass. Leave an already-inferred event alone
				// until an observation arrives to promote it.
				if match.candidate.BillingProviderSource == domain.UsageBillingProviderInferred {
					continue
				}
				// A hook that ran and could not name the route has already
				// answered the question the model would be used to guess: the
				// session is billed by something AO has no rates for. Guessing
				// anthropic from a bare claude-* name would price a proxy at
				// Anthropic list rates, and no observation is coming to fix it.
				if strings.TrimSpace(source.ProviderHint) == pricing.UnidentifiedBillingRoute {
					continue
				}
				// A session collected before its first hook has no route on the
				// binding either, so the served model name is the only evidence
				// left. It is still evidence: the provider answered with it.
				billingProviderID = snapshot.ProviderForModel(event.ModelID)
				providerSource = domain.UsageBillingProviderInferred
			}
			if billingProviderID == "" {
				continue
			}
			event.BillingProviderID = billingProviderID
			event.BillingProviderSource = providerSource
			repair := domain.LegacyUsageRepair{
				Candidate:               match.candidate,
				ExpectedFileIdentity:    source.Source.FileIdentity,
				ExpectedByteOffset:      source.Source.ByteOffset,
				ExpectedParserStateJSON: source.Source.ParserStateJSON,
				ExpectedSourceUpdatedAt: source.Source.UpdatedAt,
				BillingProviderID:       billingProviderID,
				BillingProviderSource:   providerSource,
				ProviderUsageJSON:       event.ProviderUsageJSON,
				Costs:                   domain.UsageEventCosts{PricingVersion: snapshot.ProviderVersion(billingProviderID)},
			}
			estimate, estimateErr := snapshot.Estimate(event)
			if estimateErr != nil {
				r.config.OnError(estimateErr)
			} else {
				repair.Costs = domain.UsageEventCosts{
					InputCostNanos:       estimate.InputNanos,
					CachedInputCostNanos: estimate.CachedInputNanos,
					OutputCostNanos:      estimate.OutputNanos,
					EstimatedCostNanos:   estimate.TotalNanos,
					PricingVersion:       estimate.PricingVersion,
				}
			}
			repairs = append(repairs, repair)
			if len(repairs) == 256 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return flush()
	})
}

// replayCanImprove reports whether reparsing this source could still change any
// of its open events, so a pass that cannot possibly gain anything never opens
// the transcript at all.
//
// An unattributed event is always worth a replay: the reparse is the only way it
// can gain the bounded provider object that makes it priceable. An event already
// attributed by inference is different. Inference reads the model name, which is
// the same on disk as it was at ingest, so the only input that can promote it to
// an observation is a route hint the binding did not have then.
func replayCanImprove(source domain.UsageSourceContext, candidates []domain.LegacyUsageEvent) bool {
	hint := strings.TrimSpace(source.ProviderHint)
	// The hook has already reported that this route has no name AO bills
	// against, so no replay and no inference can produce one.
	if hint == pricing.UnidentifiedBillingRoute {
		return false
	}
	if hint != "" {
		return true
	}
	for _, candidate := range candidates {
		if candidate.BillingProviderSource != domain.UsageBillingProviderInferred {
			return true
		}
	}
	return false
}

func legacyArtifactMatches(file *os.File, source domain.UsageSourceRecord, checkpoint *parserCheckpointV1) bool {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < source.ByteOffset {
		return false
	}
	identity, err := usagesvc.SourceIdentityFromFile(file)
	if err != nil || identity != source.FileIdentity {
		return false
	}
	sample, err := parserCheckpointSampleAt(file, source.ByteOffset)
	return err == nil && parserCheckpointsEqual(sample.checkpoint, checkpoint)
}

// legacyReplayIndex keeps only the replayed events a candidate could match.
//
// The chunk reader bounds each read, but a transcript prefix is unbounded, so
// retaining every parsed event would make this one-shot startup repair allocate
// in proportion to the whole file — exactly what the bounded ingestion design
// exists to prevent. Retention is instead proportional to the number of legacy
// rows on the one source being repaired.
type legacyReplayIndex struct {
	wanted     map[string]struct{}
	byKey      map[string]domain.ModelUsageEvent
	duplicates map[string]struct{}
}

func newLegacyReplayIndex(candidates []domain.LegacyUsageEvent) *legacyReplayIndex {
	index := &legacyReplayIndex{
		wanted:     make(map[string]struct{}, len(candidates)),
		byKey:      make(map[string]domain.ModelUsageEvent, len(candidates)),
		duplicates: make(map[string]struct{}),
	}
	for _, candidate := range candidates {
		index.wanted[candidate.SourceEventKey] = struct{}{}
	}
	return index
}

// observe records one chunk's events. A key repeated across the prefix is
// ambiguous and disqualifies its candidate, so both sightings are tracked even
// though only the last event is kept.
func (i *legacyReplayIndex) observe(events []domain.ModelUsageEvent) {
	for _, event := range events {
		if _, want := i.wanted[event.SourceEventKey]; !want {
			continue
		}
		if _, seen := i.byKey[event.SourceEventKey]; seen {
			i.duplicates[event.SourceEventKey] = struct{}{}
		}
		i.byKey[event.SourceEventKey] = event
	}
}

func (r *LegacyRepairer) reparseDurablePrefix(
	ctx context.Context,
	file *os.File,
	source domain.UsageSourceContext,
	state *parserStateEnvelope,
	replayed *legacyReplayIndex,
) (bool, error) {
	durableOffset := source.Source.ByteOffset
	offset := int64(0)
	discardingOversized := false
	attributionChecked := false
	for offset < durableOffset {
		chunk, err := readJSONLChunkFromSnapshot(
			ctx, file, durableOffset, offset,
			r.config.ChunkBytes, r.config.RecordBytes, discardingOversized,
		)
		if err != nil {
			return false, err
		}
		if chunk.readToEOF && len(chunk.trailing) > 0 {
			tail := bytes.TrimSpace(chunk.trailing)
			if len(tail) > 0 && json.Valid(tail) {
				chunk.records = append(chunk.records, jsonlRecord{
					Data: append([]byte(nil), tail...), Offset: chunk.trailingOffset,
				})
			}
			chunk.nextOffset = durableOffset
		}
		if !attributionChecked && len(chunk.records) > 0 {
			origin := source.Source
			origin.ByteOffset = 0
			if !codexChunkAttributionMatches(origin, state.Codex, chunk.records) {
				return false, nil
			}
			attributionChecked = true
		}
		parsed := parseRecordsWithState(source, chunk.records, chunk.nextOffset, r.config.Clock().UTC(), state)
		if parsed.err != nil {
			return false, parsed.err
		}
		replayed.observe(parsed.Events)
		discardingOversized = chunk.discardingOversizedRecord
		if chunk.nextOffset <= offset {
			return false, nil
		}
		offset = chunk.nextOffset
	}
	return true, nil
}

type legacyEventMatch struct {
	candidate domain.LegacyUsageEvent
	event     domain.ModelUsageEvent
}

func matchLegacyEvents(candidates []domain.LegacyUsageEvent, replayed *legacyReplayIndex) []legacyEventMatch {
	matches := make([]legacyEventMatch, 0, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := replayed.duplicates[candidate.SourceEventKey]; duplicate {
			continue
		}
		event, ok := replayed.byKey[candidate.SourceEventKey]
		// The stored provider usage object is deliberately not compared: filling
		// it in is what this repair exists to do, so a stored NULL must still match.
		if !ok || event.ProviderID != candidate.ProviderID || event.ModelID != candidate.ModelID ||
			event.MeasurementKind != candidate.MeasurementKind ||
			!genericTokensEqual(event.Tokens, candidate.Tokens) {
			continue
		}
		matches = append(matches, legacyEventMatch{candidate: candidate, event: event})
	}
	return matches
}

func genericTokensEqual(left, right domain.UsageTokenMetrics) bool {
	return optionalInt64Equal(left.InputTokens, right.InputTokens) &&
		optionalInt64Equal(left.CachedInputTokens, right.CachedInputTokens) &&
		optionalInt64Equal(left.UncachedInputTokens, right.UncachedInputTokens) &&
		optionalInt64Equal(left.OutputTokens, right.OutputTokens)
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
