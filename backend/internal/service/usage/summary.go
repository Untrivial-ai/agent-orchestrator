package usage

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type usageSummaryStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListCompactSessionUsage(context.Context, domain.ProjectID) ([]domain.CompactSessionUsage, error)
	ListUsageModelAggregates(context.Context, domain.SessionID) ([]domain.UsageModelAggregate, error)
	GetUsageSessionIncomplete(context.Context, domain.SessionID) (bool, error)
}

// SummaryReader derives token summaries from normalized usage events.
type SummaryReader struct{ store usageSummaryStore }

// NewSummaryReader constructs a token-usage summary reader.
func NewSummaryReader(store usageSummaryStore) *SummaryReader { return &SummaryReader{store: store} }

// ListCompact returns one batch suitable for dashboard cards.
func (r *SummaryReader) ListCompact(ctx context.Context, projectID domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("usage summary store is unavailable")
	}
	return r.store.ListCompactSessionUsage(ctx, projectID)
}

// Get returns detailed token telemetry for one session.
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
	incomplete, err := r.store.GetUsageSessionIncomplete(ctx, sessionID)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	return domain.SessionUsageSummary{
		SessionID: sessionID, Incomplete: incomplete,
		Totals: usageTotals(models), Harnesses: harnessUsageSummaries(models),
	}, nil
}

func usageTotals(models []domain.UsageModelAggregate) domain.UsageMetricTotals {
	if len(models) == 0 {
		return domain.UsageMetricTotals{}
	}
	var input, uncached, cacheRead, cacheWrite, output, reasoning, reasoningEvents int64
	uncachedAvailable, cacheReadAvailable, cacheWriteAvailable, reasoningAvailable := true, true, true, true
	for _, model := range models {
		coverage := MetricCoverage(model.Harness)
		input += model.Tokens.InputTokens
		uncached += model.Tokens.UncachedInputTokens
		cacheRead += model.Tokens.CacheReadTokens
		cacheWrite += model.Tokens.CacheWriteTokens
		output += model.Tokens.OutputTokens
		uncachedAvailable = uncachedAvailable && coverage.UncachedInput
		cacheReadAvailable = cacheReadAvailable && coverage.CacheRead
		cacheWriteAvailable = cacheWriteAvailable && coverage.CacheWrite
		reasoningAvailable = reasoningAvailable && coverage.Reasoning
		if model.Tokens.ReasoningTokens != nil {
			reasoning += *model.Tokens.ReasoningTokens
		}
		reasoningEvents += model.ReasoningEventCount
	}
	totals := domain.UsageMetricTotals{
		InputTokens: &input, OutputTokens: &output,
	}
	if uncachedAvailable {
		totals.UncachedInputTokens = &uncached
	}
	if cacheReadAvailable {
		totals.CacheReadTokens = &cacheRead
	}
	if cacheWriteAvailable {
		totals.CacheWriteTokens = &cacheWrite
	}
	if reasoningAvailable && reasoningEvents > 0 {
		totals.ReasoningTokens = &reasoning
	}
	return totals
}

func harnessUsageSummaries(models []domain.UsageModelAggregate) []domain.HarnessUsageSummary {
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
		summary := domain.HarnessUsageSummary{Harness: harness, Totals: usageTotals(rows)}
		for _, row := range rows {
			summary.Models = append(summary.Models, domain.ModelUsageSummary{
				ModelID: row.ModelID, Totals: usageTotals([]domain.UsageModelAggregate{row}),
			})
		}
		out = append(out, summary)
	}
	return out
}
