package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestParsePiUsageBearingEntries catches omitting Pi's nested tool/summarizer
// usage and attributing an assistant response to its requested model.
func TestParsePiUsageBearingEntries(t *testing.T) {
	source := usageSource(domain.UsageSourcePiSession)
	source.NativeRootID = "pi-session"
	records := []jsonlRecord{
		{Data: []byte(`{"type":"session","id":"pi-session","cwd":"/repo","version":3}`)},
		{Offset: 100, Data: []byte(`{"type":"message","id":"assistant-1","timestamp":"2026-08-29T10:00:00Z","message":{"role":"assistant","provider":"openai","model":"requested-model","responseModel":"responding-model","usage":{"input":11,"output":7,"cacheRead":5,"cacheWrite":3,"totalTokens":26,"cost":{"total":0.01}}}}`)},
		{Offset: 200, Data: []byte(`{"type":"message","id":"tool-1","timestamp":"2026-08-29T10:01:00Z","message":{"role":"toolResult","toolName":"nested-agent","usage":{"input":2,"output":1,"cacheRead":4,"cacheWrite":3,"totalTokens":10,"cost":{"total":0.02}}}}`)},
		{Offset: 300, Data: []byte(`{"type":"compaction","id":"compact-1","timestamp":"2026-08-29T10:02:00Z","usage":{"input":6,"output":2,"cacheRead":1,"cacheWrite":1,"totalTokens":10,"cost":{"total":0.03}}}`)},
		{Offset: 400, Data: []byte(`{"type":"branch_summary","id":"branch-1","timestamp":"2026-08-29T10:03:00Z","usage":{"input":8,"output":3,"cacheRead":2,"cacheWrite":1,"totalTokens":14,"cost":{"total":0.04}}}`)},
		{Offset: 500, Data: []byte(`{"type":"message","id":"user-1","message":{"role":"user","content":"ignored"}}`)},
	}

	result := parseRecords(source, records, 600, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 4 {
		t.Fatalf("result = %+v", result)
	}
	assistant := result.Events[0]
	if assistant.ProviderID != domain.UsageProviderOpenAI || assistant.BillingProviderID != "openai" ||
		assistant.BillingProviderSource != domain.UsageBillingProviderObserved ||
		assistant.ModelID != "responding-model" || assistant.MeasurementKind != domain.UsageMeasurementNativeReported ||
		assistant.CreatedAt != time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC) ||
		assistant.SourceEventKey == "" || assistant.ProviderUsageJSON == "" {
		t.Fatalf("assistant event = %+v", assistant)
	}
	assertPiTokens(t, assistant.Tokens, 19, 5, 14, 7)
	if providerUsageTokens(t, assistant.ProviderUsageJSON, "cacheWrite") != 3 {
		t.Fatalf("assistant provider usage = %s", assistant.ProviderUsageJSON)
	}

	wants := []domain.UsageTokenMetrics{
		{InputTokens: int64Ptr(9), CachedInputTokens: int64Ptr(4), UncachedInputTokens: int64Ptr(5), OutputTokens: int64Ptr(1)},
		{InputTokens: int64Ptr(8), CachedInputTokens: int64Ptr(1), UncachedInputTokens: int64Ptr(7), OutputTokens: int64Ptr(2)},
		{InputTokens: int64Ptr(11), CachedInputTokens: int64Ptr(2), UncachedInputTokens: int64Ptr(9), OutputTokens: int64Ptr(3)},
	}
	for index, event := range result.Events[1:] {
		if event.ProviderID != domain.UsageProviderOpenAI || event.BillingProviderID != "" ||
			event.BillingProviderSource != "" || event.ModelID != "Tools/summaries" ||
			event.MeasurementKind != domain.UsageMeasurementNativeReported || event.ProviderUsageJSON == "" ||
			event.SourceEventKey == "" {
			t.Fatalf("unattributed event %d = %+v", index, event)
		}
		assertPiTokenMetricsEqual(t, event.Tokens, wants[index])
	}
}

func TestParsePiAnthropicAssistantUsesResponseModel(t *testing.T) {
	source := usageSource(domain.UsageSourcePiSession)
	source.NativeRootID = "pi-session"
	record := jsonlRecord{Data: []byte(`{"type":"message","id":"assistant-1","message":{"role":"assistant","provider":"anthropic","model":"requested","responseModel":"claude-sonnet","usage":{"input":11,"output":7,"cacheRead":5,"cacheWrite":3,"totalTokens":26}}}`)}

	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	event := result.Events[0]
	if event.ProviderID != domain.UsageProviderAnthropic || event.BillingProviderID != "anthropic" ||
		event.BillingProviderSource != domain.UsageBillingProviderObserved || event.ModelID != "claude-sonnet" ||
		event.MeasurementKind != domain.UsageMeasurementNativeReported {
		t.Fatalf("event = %+v", event)
	}
	assertPiTokens(t, event.Tokens, 19, 5, 14, 7)
}

func TestParsePiRejectsInvalidUsage(t *testing.T) {
	source := usageSource(domain.UsageSourcePiSession)
	record := jsonlRecord{Data: []byte(`{"type":"message","id":"bad","message":{"role":"assistant","model":"m","usage":{"input":-1,"output":1}}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 ||
		result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("result = %+v", result)
	}
}

func assertPiTokens(t *testing.T, got domain.UsageTokenMetrics, input, cached, uncached, output int64) {
	t.Helper()
	assertPiTokenMetricsEqual(t, got, domain.UsageTokenMetrics{
		InputTokens: int64Ptr(input), CachedInputTokens: int64Ptr(cached),
		UncachedInputTokens: int64Ptr(uncached), OutputTokens: int64Ptr(output),
	})
}

func assertPiTokenMetricsEqual(t *testing.T, got, want domain.UsageTokenMetrics) {
	t.Helper()
	for name, values := range map[string][2]*int64{
		"input": {got.InputTokens, want.InputTokens}, "cached": {got.CachedInputTokens, want.CachedInputTokens},
		"uncached": {got.UncachedInputTokens, want.UncachedInputTokens}, "output": {got.OutputTokens, want.OutputTokens},
	} {
		if values[0] == nil || values[1] == nil || *values[0] != *values[1] {
			t.Fatalf("%s tokens = %v, want %v", name, values[0], values[1])
		}
	}
}
