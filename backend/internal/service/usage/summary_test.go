package usage

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type usageSummaryStoreStub struct {
	projectID  domain.ProjectID
	rows       []domain.CompactSessionUsage
	session    domain.SessionRecord
	found      bool
	incomplete bool
	models     []domain.UsageModelAggregate
	calls      [4]int
}

func (s *usageSummaryStoreStub) ListCompactSessionUsage(_ context.Context, id domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	s.projectID, s.calls[0] = id, s.calls[0]+1
	return s.rows, nil
}
func (s *usageSummaryStoreStub) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	s.calls[1]++
	return s.session, s.found, nil
}
func (s *usageSummaryStoreStub) ListUsageModelAggregates(context.Context, domain.SessionID) ([]domain.UsageModelAggregate, error) {
	s.calls[2]++
	return s.models, nil
}
func (s *usageSummaryStoreStub) GetUsageSessionIncomplete(context.Context, domain.SessionID) (bool, error) {
	s.calls[3]++
	return s.incomplete, nil
}

func TestSummaryReaderListCompactUsesOneBatchRead(t *testing.T) {
	store := &usageSummaryStoreStub{rows: []domain.CompactSessionUsage{
		{SessionID: "empty"},
		{SessionID: "used", TotalTokens: 120},
		{SessionID: "incomplete", TotalTokens: 60, Incomplete: true},
	}}
	got, err := NewSummaryReader(store).ListCompact(context.Background(), "reverb")
	mustNoError(t, err)
	if store.calls[0] != 1 || store.projectID != "reverb" || len(got) != 3 {
		t.Fatalf("read=%d project=%q items=%+v", store.calls[0], store.projectID, got)
	}
	if got[1].TotalTokens != 120 || got[1].Incomplete || !got[2].Incomplete {
		t.Fatalf("compact summaries = %+v", got)
	}
}

func TestSummaryReaderGetAggregatesModelsAndIntegrity(t *testing.T) {
	reasoning := int64(40)
	store := &usageSummaryStoreStub{
		found:      true,
		incomplete: true,
		session:    domain.SessionRecord{ID: "reverb-12", Harness: domain.HarnessCodex},
		models: []domain.UsageModelAggregate{
			{
				Harness: domain.HarnessCodex, ModelID: "gpt-5.6",
				Tokens:              domain.UsageTokenMetrics{InputTokens: 1000, UncachedInputTokens: 600, CacheReadTokens: 400, OutputTokens: 200, ReasoningTokens: &reasoning},
				ReasoningEventCount: 2,
			},
			{
				Harness: domain.HarnessClaudeCode, ModelID: "claude-sonnet",
				Tokens: domain.UsageTokenMetrics{InputTokens: 100, UncachedInputTokens: 80, CacheReadTokens: 20, OutputTokens: 25},
			},
		},
	}

	got, err := NewSummaryReader(store).Get(context.Background(), "reverb-12")
	mustNoError(t, err)
	if !got.Incomplete {
		t.Fatal("integrity failure did not mark usage incomplete")
	}
	if got.Totals.InputTokens == nil || *got.Totals.InputTokens != 1100 ||
		got.Totals.OutputTokens == nil || *got.Totals.OutputTokens != 225 ||
		got.Totals.ReasoningTokens != nil {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if len(got.Harnesses) != 2 || got.Harnesses[0].Models[0].ModelID != "gpt-5.6" {
		t.Fatalf("harnesses = %+v", got.Harnesses)
	}
	if store.calls != [4]int{0, 1, 1, 1} {
		t.Fatalf("store calls = %v", store.calls)
	}
}

func TestSummaryReaderGetReturnsUnavailableMetricsWithoutEvents(t *testing.T) {
	store := &usageSummaryStoreStub{found: true, session: domain.SessionRecord{ID: "empty"}}
	got, err := NewSummaryReader(store).Get(context.Background(), "empty")
	mustNoError(t, err)
	if got.Totals.InputTokens != nil || got.Totals.OutputTokens != nil || len(got.Harnesses) != 0 {
		t.Fatalf("empty usage = %+v", got)
	}
}

// TestSummaryReaderGetAppliesHarnessMetricCoverage catches treating a metric
// the native source never reports as a real zero. Native zeroes for supported
// metrics must remain distinguishable from unavailable metrics.
func TestSummaryReaderGetAppliesHarnessMetricCoverage(t *testing.T) {
	zero := int64(0)
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "coverage", Harness: domain.HarnessQwen},
		models: []domain.UsageModelAggregate{
			{
				Harness: domain.HarnessQwen, ModelID: "qwen3-coder",
				Tokens: domain.UsageTokenMetrics{
					InputTokens: 10, UncachedInputTokens: 4, CacheReadTokens: 6,
					OutputTokens: 2, ReasoningTokens: &zero,
				},
				ReasoningEventCount: 1,
			},
			{
				Harness: domain.HarnessKimi, ModelID: "kimi-for-coding",
				Tokens: domain.UsageTokenMetrics{
					InputTokens: 8, UncachedInputTokens: 3, CacheReadTokens: 4,
					CacheWriteTokens: 1, OutputTokens: 2,
				},
			},
		},
	}

	got, err := NewSummaryReader(store).Get(context.Background(), "coverage")
	mustNoError(t, err)
	if len(got.Harnesses) != 2 {
		t.Fatalf("harnesses = %+v", got.Harnesses)
	}
	if got.Totals.CacheWriteTokens != nil || got.Totals.ReasoningTokens != nil {
		t.Fatalf("mixed-harness totals expose partial metrics = %+v", got.Totals)
	}
	qwen := got.Harnesses[0].Totals
	if qwen.CacheWriteTokens != nil {
		t.Fatalf("qwen cache write = %v, want unavailable", *qwen.CacheWriteTokens)
	}
	if qwen.ReasoningTokens == nil || *qwen.ReasoningTokens != 0 {
		t.Fatalf("qwen reasoning = %v, want reported zero", qwen.ReasoningTokens)
	}
	kimi := got.Harnesses[1].Totals
	if kimi.CacheWriteTokens == nil || *kimi.CacheWriteTokens != 1 {
		t.Fatalf("kimi cache write = %v, want 1", kimi.CacheWriteTokens)
	}
	if kimi.ReasoningTokens != nil {
		t.Fatalf("kimi reasoning = %v, want unavailable", *kimi.ReasoningTokens)
	}
}
