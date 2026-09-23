package usage

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type usageSummaryStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListCompactSessionUsageAggregates(context.Context, domain.ProjectID) ([]domain.CompactSessionUsageAggregate, error)
	ListUsageModelAggregates(context.Context, domain.SessionID) ([]domain.UsageModelAggregate, error)
	GetUsageSessionIncomplete(context.Context, domain.SessionID) (bool, error)
}

// SummaryReader derives token and estimated-cost summaries from normalized
// usage events.
type SummaryReader struct{ store usageSummaryStore }

// NewSummaryReader constructs a usage summary reader.
func NewSummaryReader(store usageSummaryStore) *SummaryReader { return &SummaryReader{store: store} }

// ListCompact returns one batch suitable for dashboard cards.
func (r *SummaryReader) ListCompact(ctx context.Context, projectID domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("usage summary store is unavailable")
	}
	rows, err := r.store.ListCompactSessionUsageAggregates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CompactSessionUsage, 0, len(rows))
	for _, row := range rows {
		estimatedCost, err := estimatedCost(row.Cost)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.CompactSessionUsage{
			SessionID: row.SessionID, ProcessedTokens: row.ProcessedTokens,
			Incomplete: row.Incomplete, EstimatedCost: estimatedCost,
		})
	}
	return out, nil
}

// Get returns detailed token and estimated-cost telemetry for one session.
func (r *SummaryReader) Get(ctx context.Context, sessionID domain.SessionID) (domain.SessionUsageSummary, error) {
	if r == nil || r.store == nil {
		return domain.SessionUsageSummary{}, fmt.Errorf("usage summary store is unavailable")
	}
	if _, ok, err := r.store.GetSession(ctx, sessionID); err != nil {
		return domain.SessionUsageSummary{}, err
	} else if !ok {
		return domain.SessionUsageSummary{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}

	models, err := r.store.ListUsageModelAggregates(ctx, sessionID)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	visibleModels := make([]domain.UsageModelAggregate, 0, len(models))
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.ModelID), "<synthetic>") {
			continue
		}
		visibleModels = append(visibleModels, model)
	}
	models = visibleModels
	incomplete, err := r.store.GetUsageSessionIncomplete(ctx, sessionID)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	totals, err := usageTotals(models)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	harnesses, err := harnessUsageSummaries(models)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	return domain.SessionUsageSummary{
		SessionID: sessionID, Incomplete: incomplete, Totals: totals, Harnesses: harnesses,
	}, nil
}

func usageTotals(models []domain.UsageModelAggregate) (domain.UsageMetricTotals, error) {
	if len(models) == 0 {
		return domain.UsageMetricTotals{}, nil
	}
	var costs domain.UsageCostAggregate
	for _, model := range models {
		if err := mergeUsageCostAggregate(&costs, model.Cost); err != nil {
			return domain.UsageMetricTotals{}, err
		}
	}
	estimate, err := estimatedCost(costs)
	if err != nil {
		return domain.UsageMetricTotals{}, err
	}
	input := aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.InputTokens })
	output := aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.OutputTokens })
	totals := domain.UsageMetricTotals{
		InputTokens:       input,
		CachedInputTokens: aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.CachedInputTokens }),
		UncachedInputTokens: aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 {
			return model.Tokens.UncachedInputTokens
		}),
		OutputTokens:  output,
		EstimatedCost: estimate,
	}
	if input != nil && output != nil {
		processed := *input + *output
		totals.ProcessedTokens = &processed
	}
	return totals, nil
}

// aggregateMetric sums one metric across models. One uncollected counter makes
// the whole sum unknown rather than silently under-reporting it.
func aggregateMetric(models []domain.UsageModelAggregate, selectMetric func(domain.UsageModelAggregate) *int64) *int64 {
	var total int64
	for _, model := range models {
		value := selectMetric(model)
		if value == nil {
			return nil
		}
		total += *value
	}
	return &total
}

func harnessUsageSummaries(models []domain.UsageModelAggregate) ([]domain.HarnessUsageSummary, error) {
	order := make([]domain.AgentHarness, 0)
	grouped := make(map[domain.AgentHarness][]domain.UsageModelAggregate)
	for _, model := range models {
		if _, ok := grouped[model.Harness]; !ok {
			order = append(order, model.Harness)
		}
		grouped[model.Harness] = append(grouped[model.Harness], model)
	}
	out := make([]domain.HarnessUsageSummary, 0, len(order))
	for _, harness := range order {
		rows := grouped[harness]
		totals, err := usageTotals(rows)
		if err != nil {
			return nil, err
		}
		summary := domain.HarnessUsageSummary{Harness: harness, Totals: totals}
		for _, row := range rows {
			modelTotals, err := usageTotals([]domain.UsageModelAggregate{row})
			if err != nil {
				return nil, err
			}
			summary.Models = append(summary.Models, domain.ModelUsageSummary{
				ModelID: row.ModelID, Totals: modelTotals,
			})
		}
		out = append(out, summary)
	}
	return out, nil
}

func estimatedCost(raw domain.UsageCostAggregate) (*domain.EstimatedCost, error) {
	if err := validateUsageCostAggregate(raw); err != nil {
		return nil, err
	}
	if raw.EventCount == 0 {
		return nil, nil
	}
	coverage := domain.EstimatedCostCoverageComplete
	total := raw.PricedTotalNanos
	if raw.PricedEventCount != raw.EventCount {
		coverage = domain.EstimatedCostCoveragePartial
		var err error
		for _, component := range []struct {
			name  string
			value int64
		}{
			{"input cost", raw.UnpricedKnownInputNanos},
			{"cached input cost", raw.UnpricedKnownCachedInputNanos},
			{"output cost", raw.UnpricedKnownOutputNanos},
		} {
			total, err = checkedUsageAdd(component.name, total, component.value)
			if err != nil {
				return nil, err
			}
		}
		if total == 0 {
			return nil, nil
		}
	}
	providerAttribution, err := estimatedCostProviderAttribution(raw)
	if err != nil {
		return nil, err
	}
	return &domain.EstimatedCost{
		TotalNanos:          total,
		InputNanos:          knownComponent(raw.EventCount, raw.KnownInputCount, raw.KnownInputNanos),
		CachedInputNanos:    knownComponent(raw.EventCount, raw.KnownCachedInputCount, raw.KnownCachedInputNanos),
		OutputNanos:         knownComponent(raw.EventCount, raw.KnownOutputCount, raw.KnownOutputNanos),
		Coverage:            coverage,
		ProviderAttribution: providerAttribution,
	}, nil
}

func estimatedCostProviderAttribution(raw domain.UsageCostAggregate) (domain.EstimatedCostProviderAttribution, error) {
	switch {
	case raw.ObservedCostEventCount > 0 && raw.InferredCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionMixed, nil
	case raw.InferredCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionInferred, nil
	case raw.ObservedCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionObserved, nil
	default:
		return "", fmt.Errorf("usage estimated cost has no provider attribution")
	}
}

func knownComponent(eventCount, knownCount, value int64) *int64 {
	if eventCount == knownCount {
		return &value
	}
	return nil
}

func mergeUsageCostAggregate(dst *domain.UsageCostAggregate, src domain.UsageCostAggregate) error {
	if err := validateUsageCostAggregate(src); err != nil {
		return err
	}
	fields := []struct {
		name string
		dst  *int64
		src  int64
	}{
		{"cost event count", &dst.EventCount, src.EventCount},
		{"priced event count", &dst.PricedEventCount, src.PricedEventCount},
		{"priced total cost", &dst.PricedTotalNanos, src.PricedTotalNanos},
		{"observed cost event count", &dst.ObservedCostEventCount, src.ObservedCostEventCount},
		{"inferred cost event count", &dst.InferredCostEventCount, src.InferredCostEventCount},
		{"known input count", &dst.KnownInputCount, src.KnownInputCount},
		{"known input cost", &dst.KnownInputNanos, src.KnownInputNanos},
		{"unpriced known input cost", &dst.UnpricedKnownInputNanos, src.UnpricedKnownInputNanos},
		{"known cached input count", &dst.KnownCachedInputCount, src.KnownCachedInputCount},
		{"known cached input cost", &dst.KnownCachedInputNanos, src.KnownCachedInputNanos},
		{"unpriced known cached input cost", &dst.UnpricedKnownCachedInputNanos, src.UnpricedKnownCachedInputNanos},
		{"known output count", &dst.KnownOutputCount, src.KnownOutputCount},
		{"known output cost", &dst.KnownOutputNanos, src.KnownOutputNanos},
		{"unpriced known output cost", &dst.UnpricedKnownOutputNanos, src.UnpricedKnownOutputNanos},
	}
	for _, field := range fields {
		value, err := checkedUsageAdd(field.name, *field.dst, field.src)
		if err != nil {
			return err
		}
		*field.dst = value
	}
	return nil
}

func validateUsageCostAggregate(raw domain.UsageCostAggregate) error {
	values := []struct {
		name  string
		value int64
	}{
		{"event count", raw.EventCount}, {"priced event count", raw.PricedEventCount}, {"priced total cost", raw.PricedTotalNanos},
		{"observed cost event count", raw.ObservedCostEventCount}, {"inferred cost event count", raw.InferredCostEventCount},
		{"known input count", raw.KnownInputCount}, {"known input cost", raw.KnownInputNanos}, {"unpriced known input cost", raw.UnpricedKnownInputNanos},
		{"known cached input count", raw.KnownCachedInputCount}, {"known cached input cost", raw.KnownCachedInputNanos}, {"unpriced known cached input cost", raw.UnpricedKnownCachedInputNanos},
		{"known output count", raw.KnownOutputCount}, {"known output cost", raw.KnownOutputNanos}, {"unpriced known output cost", raw.UnpricedKnownOutputNanos},
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("usage %s must be nonnegative", item.name)
		}
	}
	if raw.PricedEventCount > raw.EventCount || raw.KnownInputCount > raw.EventCount ||
		raw.KnownCachedInputCount > raw.EventCount || raw.KnownOutputCount > raw.EventCount ||
		raw.ObservedCostEventCount > raw.EventCount || raw.InferredCostEventCount > raw.EventCount ||
		raw.InferredCostEventCount > raw.EventCount-raw.ObservedCostEventCount {
		return fmt.Errorf("usage cost coverage count exceeds event count")
	}
	if raw.UnpricedKnownInputNanos > raw.KnownInputNanos ||
		raw.UnpricedKnownCachedInputNanos > raw.KnownCachedInputNanos ||
		raw.UnpricedKnownOutputNanos > raw.KnownOutputNanos {
		return fmt.Errorf("usage unpriced component cost exceeds known component cost")
	}
	return nil
}

func checkedUsageAdd(label string, left, right int64) (int64, error) {
	if left < 0 || right < 0 {
		return 0, fmt.Errorf("usage %s must be nonnegative", label)
	}
	if left > math.MaxInt64-right {
		return 0, fmt.Errorf("usage %s overflows int64", label)
	}
	return left + right, nil
}
