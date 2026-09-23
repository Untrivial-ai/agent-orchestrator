package domain

import (
	"errors"
	"time"
)

// UsageSourceKind identifies the native artifact shape that produced usage
// facts. It is deliberately narrower than AgentHarness: only certified usage
// sources get persisted in the V1 usage pipeline.
type UsageSourceKind string

// UsageSourceKind values identify certified native usage artifact shapes.
const (
	UsageSourceClaudeMain     UsageSourceKind = "claude_main"
	UsageSourceClaudeSubagent UsageSourceKind = "claude_subagent"
	UsageSourceCodexRollout   UsageSourceKind = "codex_rollout"
	UsageSourceKimiWire       UsageSourceKind = "kimi_wire"
)

// UsageBindingState tracks the root native-session binding lifecycle.
type UsageBindingState string

// UsageBindingState values describe root native-session binding lifecycle.
const (
	UsageBindingDiscovering UsageBindingState = "discovering"
	UsageBindingActive      UsageBindingState = "active"
	UsageBindingFinalizing  UsageBindingState = "finalizing"
	UsageBindingComplete    UsageBindingState = "complete"
	UsageBindingPartial     UsageBindingState = "partial"
)

// UsageSourceState tracks one physical JSONL artifact generation.
type UsageSourceState string

// UsageSourceState values describe one physical source artifact lifecycle.
const (
	UsageSourcePending  UsageSourceState = "pending"
	UsageSourceActive   UsageSourceState = "active"
	UsageSourceComplete UsageSourceState = "complete"
	UsageSourceError    UsageSourceState = "error"
)

// Usage error code constants are safe storage/display identifiers for
// transcript discovery and ingestion failures.
const (
	UsageErrorSourceDiscoveryPending      = "source_discovery_pending"
	UsageErrorArtifactPathRejected        = "artifact_path_rejected"
	UsageErrorArtifactMissing             = "artifact_missing"
	UsageErrorArtifactReplaced            = "artifact_replaced"
	UsageErrorSourceReadFailed            = "source_read_failed"
	UsageErrorRecordTooLarge              = "record_too_large"
	UsageErrorMalformedJSONL              = "malformed_jsonl"
	UsageErrorUnsupportedSourceFormat     = "unsupported_source_format"
	UsageErrorSourceEventConflict         = "source_event_conflict"
	UsageErrorNonMonotonicCumulativeUsage = "non_monotonic_cumulative_usage"
	UsageErrorInvalidParserState          = "invalid_parser_state"
	UsageErrorUnresolvedSpawnCall         = "unresolved_spawn_call"
	UsageErrorCodexSourceBudgetExceeded   = "codex_source_budget_exceeded"
)

// Usage ingestion sentinel errors report replay and cursor conflicts.
var (
	ErrUsageSourceOffsetConflict   = errors.New("usage source cursor offset conflict")
	ErrUsageSourceRevisionConflict = errors.New("usage source revision conflict")
	ErrUsageSourceEventConflict    = errors.New("usage source event conflict")
)

// UsageBindingRecord binds one AO session to one native root session/thread.
type UsageBindingRecord struct {
	ID             int64
	SessionID      SessionID
	Harness        AgentHarness
	NativeRootID   string
	InitialModelID string
	ProviderHint   string
	State          UsageBindingState
	LastErrorCode  string
	UpdatedAt      time.Time
}

// UsageSourceRecord tracks one physical JSONL artifact generation and its
// durable read cursor.
type UsageSourceRecord struct {
	ID              int64
	BindingID       int64
	Kind            UsageSourceKind
	NativeSessionID string
	SubagentID      string
	ArtifactPath    string
	FileIdentity    string
	Generation      int64
	ByteOffset      int64
	ParserStateJSON string
	State           UsageSourceState
	FailureCount    int64
	AnomalyCount    int64
	NextRetryAt     *time.Time
	LastErrorCode   string
	UpdatedAt       time.Time
}

// UsageSourceContext is the source row plus immutable binding/session facts the
// ingestor needs while normalizing parser output.
type UsageSourceContext struct {
	Source         UsageSourceRecord
	SessionID      SessionID
	NativeRootID   string
	InitialModelID string
	ProviderHint   string
	BindingState   UsageBindingState
}

// UsageProviderID identifies the provider vocabulary normalized into a usage
// event. Provider-specific counters remain separate from the canonical totals.
type UsageProviderID string

// Usage provider identifiers.
const (
	UsageProviderOpenAI    UsageProviderID = "openai"
	UsageProviderAnthropic UsageProviderID = "anthropic"
)

// UsageMeasurementKind describes the trust source for a complete usage event.
// It replaces per-metric provenance: the nullable counters already say which
// metrics are known, so the only remaining question is where they came from.
type UsageMeasurementKind string

// Usage measurement kinds.
const (
	// UsageMeasurementNativeReported means the counters came from a native
	// provider or CLI usage record. Exact arithmetic over native counters —
	// subtracting Codex cumulative totals, summing Claude cache buckets — does
	// not make an event estimated.
	UsageMeasurementNativeReported UsageMeasurementKind = "native_reported"
	// UsageMeasurementAOEstimated means AO approximated counters without native ones.
	UsageMeasurementAOEstimated UsageMeasurementKind = "ao_estimated"
	// UsageMeasurementMixed means native counters and AO estimates were combined.
	UsageMeasurementMixed UsageMeasurementKind = "mixed"
	// UsageMeasurementUnknown means the origin cannot be established.
	UsageMeasurementUnknown UsageMeasurementKind = "unknown"
)

// UsageBillingProviderSource records how an event's billing provider was
// reached, which decides whether it can ever be revised.
type UsageBillingProviderSource string

const (
	// UsageBillingProviderObserved means the provider was named by the
	// transcript or by the trusted route hint. It is immutable, as is the cost
	// derived from it.
	UsageBillingProviderObserved UsageBillingProviderSource = "observed"
	// UsageBillingProviderInferred means nothing named the provider and it was
	// resolved from the model that answered. One later observation may replace
	// it, along with its cost.
	UsageBillingProviderInferred UsageBillingProviderSource = "inferred"
)

// ObservedBillingProviderSource pairs a provider the transcript or the
// binding's route hint named outright with the source that makes the
// attribution immutable. An empty provider carries no source: both columns are
// written together or not at all.
func ObservedBillingProviderSource(billingProviderID string) UsageBillingProviderSource {
	if billingProviderID == "" {
		return ""
	}
	return UsageBillingProviderObserved
}

// UsageTokenMetrics is the provider-neutral token vector stored on every usage
// event. Nil means unknown; a non-nil zero is a known zero. Cache writes are
// part of uncached input here; their provider-specific split stays in the
// bounded provider usage object.
type UsageTokenMetrics struct {
	InputTokens         *int64
	CachedInputTokens   *int64
	UncachedInputTokens *int64
	OutputTokens        *int64
}

// UsageEventCosts is the durable nano-USD estimate stored on one event. Every
// field is nil until the event has been priced; a non-nil total is immutable.
// InputCostNanos covers every non-cache-read input charge, cache writes
// included, so reasoning and cache-write subsets are never charged twice.
type UsageEventCosts struct {
	InputCostNanos       *int64
	CachedInputCostNanos *int64
	OutputCostNanos      *int64
	EstimatedCostNanos   *int64
	PricingVersion       string
}

// ModelUsageEvent is one append-only normalized usage fact.
//
// ProviderID identifies the provider vocabulary into which token counters were
// normalized. BillingProviderID identifies the exact catalog provider used for
// pricing and is empty until attribution proves it; the two differ whenever an
// Anthropic-vocabulary transcript is served by another provider such as z.ai.
//
// ProviderUsageJSON is the bounded usage object the CLI emitted, stored
// verbatim so optional and future provider fields survive. It is empty when the
// event predates the capture or the object exceeded its size bound.
type ModelUsageEvent struct {
	ProviderID            UsageProviderID
	BillingProviderID     string
	BillingProviderSource UsageBillingProviderSource
	ModelID               string
	MeasurementKind       UsageMeasurementKind
	Tokens                UsageTokenMetrics
	ProviderUsageJSON     string
	Costs                 UsageEventCosts
	CreatedAt             time.Time
	SourceEventKey        string
}

// UsageCostCandidate is one still-total-null event selected for an exact
// provider catalog attempt. Source facts remain immutable and are carried back
// to storage as compare-and-swap guards.
type UsageCostCandidate struct {
	ID                int64
	BindingID         int64
	ProviderID        UsageProviderID
	BillingProviderID string
	ModelID           string
	MeasurementKind   UsageMeasurementKind
	Tokens            UsageTokenMetrics
	ProviderUsageJSON string
	PricingVersion    string
	SourceEventKey    string
}

// UsageCostUpdate carries one candidate's immutable compare-and-swap facts and
// the result of attempting it against a newer provider catalog version.
type UsageCostUpdate struct {
	Candidate UsageCostCandidate
	Costs     UsageEventCosts
}

// LegacyUsageEvent is one open-attribution event selected for transcript
// attribution repair: never attributed, or attributed only by inference and so
// still replaceable by an observation. Its source and generic facts are
// immutable CAS guards.
type LegacyUsageEvent struct {
	ID                    int64
	BindingID             int64
	UsageSourceID         int64
	ProviderID            UsageProviderID
	BillingProviderID     string
	BillingProviderSource UsageBillingProviderSource
	ModelID               string
	MeasurementKind       UsageMeasurementKind
	Tokens                UsageTokenMetrics
	ProviderUsageJSON     string
	PricingVersion        string
	SourceEventKey        string
}

// LegacyUsageRepair carries transcript-derived attribution and the estimate
// made from the same fenced pricing snapshot.
type LegacyUsageRepair struct {
	Candidate               LegacyUsageEvent
	ExpectedFileIdentity    string
	ExpectedByteOffset      int64
	ExpectedParserStateJSON string
	ExpectedSourceUpdatedAt time.Time
	BillingProviderID       string
	BillingProviderSource   UsageBillingProviderSource
	ProviderUsageJSON       string
	Costs                   UsageEventCosts
}

// EstimatedCostCoverage describes how much of a usage scope has a durable
// estimate. Token collection integrity is reported separately.
type EstimatedCostCoverage string

const (
	// EstimatedCostCoverageComplete means every event in the scope has a stored total.
	EstimatedCostCoverageComplete EstimatedCostCoverage = "complete"
	// EstimatedCostCoveragePartial means the scope has a positive known lower bound.
	EstimatedCostCoveragePartial EstimatedCostCoverage = "partial"
)

// EstimatedCostProviderAttribution describes whether the billing providers
// behind an estimate were observed from routing evidence or inferred from the
// model catalog.
type EstimatedCostProviderAttribution string

const (
	// EstimatedCostProviderAttributionObserved means every contributing price
	// used a billing provider named by routing evidence.
	EstimatedCostProviderAttributionObserved EstimatedCostProviderAttribution = "observed"
	// EstimatedCostProviderAttributionInferred means every contributing price
	// used a billing provider inferred from model ownership.
	EstimatedCostProviderAttributionInferred EstimatedCostProviderAttribution = "inferred"
	// EstimatedCostProviderAttributionMixed means the estimate combines prices
	// from observed and inferred billing providers.
	EstimatedCostProviderAttributionMixed EstimatedCostProviderAttribution = "mixed"
)

// EstimatedCost is the user-facing nano-USD estimate for one usage scope.
// Components remain nullable when only part of that component is known.
type EstimatedCost struct {
	TotalNanos          int64
	InputNanos          *int64
	CachedInputNanos    *int64
	OutputNanos         *int64
	Coverage            EstimatedCostCoverage
	ProviderAttribution EstimatedCostProviderAttribution
}

// UsageCostAggregate contains the independent SQL sums and coverage counts
// needed to derive a scope estimate without double-counting priced events.
type UsageCostAggregate struct {
	EventCount             int64
	PricedEventCount       int64
	PricedTotalNanos       int64
	ObservedCostEventCount int64
	InferredCostEventCount int64

	KnownInputCount               int64
	KnownInputNanos               int64
	UnpricedKnownInputNanos       int64
	KnownCachedInputCount         int64
	KnownCachedInputNanos         int64
	UnpricedKnownCachedInputNanos int64
	KnownOutputCount              int64
	KnownOutputNanos              int64
	UnpricedKnownOutputNanos      int64
}

// UsageModelAggregate is the raw model-level aggregate read from storage before
// the service applies user-facing coverage rules.
type UsageModelAggregate struct {
	Harness AgentHarness
	ModelID string
	Tokens  UsageTokenMetrics
	Cost    UsageCostAggregate
}

// CompactSessionUsageAggregate is one batched storage row before checked token
// and cost derivation.
type CompactSessionUsageAggregate struct {
	SessionID       SessionID
	ProcessedTokens *int64
	Incomplete      bool
	Cost            UsageCostAggregate
}

// CompactSessionUsage is the dashboard usage read model.
type CompactSessionUsage struct {
	SessionID       SessionID
	ProcessedTokens *int64
	Incomplete      bool
	EstimatedCost   *EstimatedCost
}

// UsageMetricTotals is the aggregate metric block used by session, harness,
// and model summaries.
type UsageMetricTotals struct {
	InputTokens         *int64
	CachedInputTokens   *int64
	UncachedInputTokens *int64
	OutputTokens        *int64
	ProcessedTokens     *int64
	EstimatedCost       *EstimatedCost
}

// ModelUsageSummary is a per-model aggregate. The billing provider stays a
// pricing input: every event was costed against its own provider's rates before
// it reached this sum, so the total is exact without splitting the model apart.
type ModelUsageSummary struct {
	ModelID string
	Totals  UsageMetricTotals
}

// HarnessUsageSummary groups model summaries by AO harness.
type HarnessUsageSummary struct {
	Harness AgentHarness
	Totals  UsageMetricTotals
	Models  []ModelUsageSummary
}

// SessionUsageSummary is the read model returned by the session usage service.
type SessionUsageSummary struct {
	SessionID  SessionID
	Incomplete bool
	Totals     UsageMetricTotals
	Harnesses  []HarnessUsageSummary
}

// SourceCursorState is the durable source state to commit after parsing a
// chunk. ApplyUsageChunk writes it atomically with the emitted events.
type SourceCursorState struct {
	ByteOffset      int64
	State           UsageSourceState
	ParserStateJSON string
	FailureCount    int64
	AnomalyCount    int64
	NextRetryAt     *time.Time
	LastErrorCode   string
	UpdatedAt       time.Time
}
